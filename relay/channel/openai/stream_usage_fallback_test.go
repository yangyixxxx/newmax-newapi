package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// /v1/messages（RelayFormatClaude）转发到 OpenAI 兼容上游时 RelayMode 为 Unknown，
// 本地 token 兜底统计必须照常累计，否则上游不回 usage 时 completion_tokens 记 0（漏计费）。
func TestProcessTokenDataAccumulatesForUnknownRelayMode(t *testing.T) {
	var builder strings.Builder
	var toolCount int

	chunks := []string{
		`{"choices":[{"delta":{"reasoning_content":"想一想"},"index":0}]}`,
		`{"choices":[{"delta":{"content":"你好"},"index":0}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"{\"city\":"}}]},"index":0}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"北京\"}"}}]},"index":0}]}`,
	}
	for _, chunk := range chunks {
		require.NoError(t, processTokenData(relayconstant.RelayModeUnknown, chunk, &builder, &toolCount))
	}

	acc := builder.String()
	assert.Contains(t, acc, "想一想")
	assert.Contains(t, acc, "你好")
	assert.Contains(t, acc, "get_weather")
	assert.Contains(t, acc, "北京")
	assert.Equal(t, 1, toolCount)
}

// 客户端在上游返回首个有效 SSE 片段前断开时，不能把完整输入上下文当作
// 已消费 usage 本地结算；必须返回不可重试的失败，让外层走预扣费退款。
func TestOaiStreamHandlerReturnsFailureBeforeFirstChunkOnClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	requestContext, cancel := context.WithCancel(context.Background())
	upstreamReader, upstreamWriter := io.Pipe()
	t.Cleanup(func() {
		_ = upstreamReader.Close()
		_ = upstreamWriter.Close()
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)

	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
	}
	info.SetEstimatePromptTokens(114_776)

	cancel()
	usage, apiErr := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       upstreamReader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	assert.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeEmptyResponse, apiErr.GetErrorCode())
	assert.Equal(t, 499, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.False(t, types.IsRecordErrorLog(apiErr))
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)
	assert.Zero(t, info.ReceivedResponseCount)
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

// 正常收到有效片段、但上游没有返回 usage 时，仍需保留本地 token 兜底，
// 避免修复断连退款时把正常请求也变成零计费。
func TestOaiStreamHandlerStillEstimatesUsageForCompletedStreamWithoutUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	streamBody := strings.Join([]string{
		`data: {"id":"chatcmpl-1","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","created":1,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsStream:    true,
		DisablePing: true,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
	}
	info.SetEstimatePromptTokens(100)

	usage, apiErr := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(streamBody)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	})

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 100, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 2, info.ReceivedResponseCount)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
}

// SupportStreamOptions 白名单渠道（如火山方舟 VolcEngine）的 include_usage 不能被
// ConvertOpenAIRequest 抹掉，否则上游不发尾部 usage-only chunk。
func TestConvertOpenAIRequestKeepsStreamOptionsForSupportedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	adaptor := Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeVolcEngine,
			UpstreamModelName:    "deepseek-v4-flash-260425",
			SupportStreamOptions: true,
		},
	}
	req := &dto.GeneralOpenAIRequest{
		Model:         "deepseek-v4-flash-260425",
		StreamOptions: &dto.StreamOptions{IncludeUsage: true},
	}

	out, err := adaptor.ConvertOpenAIRequest(c, info, req)
	require.NoError(t, err)
	converted, ok := out.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.NotNil(t, converted.StreamOptions)
	assert.True(t, converted.StreamOptions.IncludeUsage)
}

func TestConvertOpenAIRequestStripsStreamOptionsForUnsupportedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	adaptor := Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeBaidu,
			UpstreamModelName:    "ernie-x",
			SupportStreamOptions: false,
		},
	}
	req := &dto.GeneralOpenAIRequest{
		Model:         "ernie-x",
		StreamOptions: &dto.StreamOptions{IncludeUsage: true},
	}

	out, err := adaptor.ConvertOpenAIRequest(c, info, req)
	require.NoError(t, err)
	converted, ok := out.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	assert.Nil(t, converted.StreamOptions)
}
