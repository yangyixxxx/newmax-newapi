package openai

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"

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
