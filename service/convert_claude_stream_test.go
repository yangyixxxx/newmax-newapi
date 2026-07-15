package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 复现火山方舟 deepseek 场景：上游最后一个数据 chunk 就是 finish_reason chunk（usage=null），
// 后面没有 usage-only chunk。HandleFinalResponse 重放该 chunk 时 ClaudeConvertInfo.Usage
// 已被本地兜底 usage 填充，此时必须补齐 content_block_stop / message_delta / message_stop，
// 否则客户端拿到残缺 Claude 流，tool_use 永不执行（"回复着就停了"）。

func newClaudeConvertInfo(lastType string, index int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		SendResponseCount: 5, // 非首个响应
		ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{
			LastMessagesType:       lastType,
			Index:                  index,
			ToolCallBaseIndex:      index,
			ToolCallMaxIndexOffset: 0,
		},
	}
}

func parseChunk(t *testing.T, data string) *dto.ChatCompletionsStreamResponse {
	t.Helper()
	var resp dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal([]byte(data), &resp))
	return &resp
}

func eventTypes(resps []*dto.ClaudeResponse) []string {
	types := make([]string, 0, len(resps))
	for _, r := range resps {
		types = append(types, r.Type)
	}
	return types
}

func findByType(resps []*dto.ClaudeResponse, typ string) *dto.ClaudeResponse {
	for _, r := range resps {
		if r.Type == typ {
			return r
		}
	}
	return nil
}

func TestStreamClaudeFinishChunkWithoutUsageClosesWithFallbackUsage(t *testing.T) {
	info := newClaudeConvertInfo(relaycommon.LastMessageTypeTools, 1)
	// HandleFinalResponse 已注入本地兜底 usage
	info.ClaudeConvertInfo.Usage = &dto.Usage{PromptTokens: 8004, CompletionTokens: 67, TotalTokens: 8071}

	chunk := parseChunk(t, `{"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":"tool_calls","index":0}],"usage":null}`)
	resps := StreamResponseOpenAI2Claude(chunk, info)

	assert.Equal(t, []string{"content_block_stop", "message_delta", "message_stop"}, eventTypes(resps))
	assert.True(t, info.ClaudeConvertInfo.Done)

	delta := findByType(resps, "message_delta")
	require.NotNil(t, delta)
	require.NotNil(t, delta.Delta)
	require.NotNil(t, delta.Delta.StopReason)
	assert.Equal(t, "tool_use", *delta.Delta.StopReason)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 8004, delta.Usage.InputTokens)
	assert.Equal(t, 67, delta.Usage.OutputTokens)
}

func TestStreamClaudeFinishChunkDefersWhenNoUsageAvailable(t *testing.T) {
	info := newClaudeConvertInfo(relaycommon.LastMessageTypeTools, 1)

	finishChunk := parseChunk(t, `{"choices":[{"delta":{"content":"","role":"assistant"},"finish_reason":"tool_calls","index":0}],"usage":null}`)
	resps := StreamResponseOpenAI2Claude(finishChunk, info)

	// 中途收到 finish_reason 但拿不到任何 usage：等 usage-only chunk，先不收尾
	assert.Nil(t, findByType(resps, "message_stop"))
	assert.False(t, info.ClaudeConvertInfo.Done)
	assert.Equal(t, "tool_calls", info.FinishReason)

	usageChunk := parseChunk(t, `{"choices":[],"usage":{"prompt_tokens":282,"completion_tokens":67,"total_tokens":349}}`)
	resps = StreamResponseOpenAI2Claude(usageChunk, info)

	assert.Equal(t, []string{"content_block_stop", "message_delta", "message_stop"}, eventTypes(resps))
	assert.True(t, info.ClaudeConvertInfo.Done)

	delta := findByType(resps, "message_delta")
	require.NotNil(t, delta)
	require.NotNil(t, delta.Delta.StopReason)
	assert.Equal(t, "tool_use", *delta.Delta.StopReason)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 282, delta.Usage.InputTokens)
	assert.Equal(t, 67, delta.Usage.OutputTokens)
}

func TestStreamClaudeFinishChunkWithContentEmitsContentBeforeClosing(t *testing.T) {
	info := newClaudeConvertInfo(relaycommon.LastMessageTypeText, 0)
	info.ClaudeConvertInfo.Usage = &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}

	chunk := parseChunk(t, `{"choices":[{"delta":{"content":"再见","role":"assistant"},"finish_reason":"stop","index":0}],"usage":null}`)
	resps := StreamResponseOpenAI2Claude(chunk, info)

	assert.Equal(t, []string{"content_block_delta", "content_block_stop", "message_delta", "message_stop"}, eventTypes(resps))

	textDelta := findByType(resps, "content_block_delta")
	require.NotNil(t, textDelta)
	require.NotNil(t, textDelta.Delta)
	require.NotNil(t, textDelta.Delta.Text)
	assert.Equal(t, "再见", *textDelta.Delta.Text)

	delta := findByType(resps, "message_delta")
	require.NotNil(t, delta)
	require.NotNil(t, delta.Delta.StopReason)
	assert.Equal(t, "end_turn", *delta.Delta.StopReason)
}

func TestFinalizeClaudeStreamClosesDanglingMessage(t *testing.T) {
	// 上游连 finish_reason 都没给（流被掐断/协议不规范）：流结束时必须强制补收尾
	info := newClaudeConvertInfo(relaycommon.LastMessageTypeTools, 2)
	info.ClaudeConvertInfo.ToolCallMaxIndexOffset = 1 // 两个并行 tool_use 块
	info.ClaudeConvertInfo.Usage = &dto.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60}

	resps := FinalizeClaudeStream(info)

	assert.Equal(t, []string{"content_block_stop", "content_block_stop", "message_delta", "message_stop"}, eventTypes(resps))
	assert.True(t, info.ClaudeConvertInfo.Done)

	delta := findByType(resps, "message_delta")
	require.NotNil(t, delta)
	require.NotNil(t, delta.Delta.StopReason)
	assert.Equal(t, "end_turn", *delta.Delta.StopReason)

	// 已收尾的流再 finalize 不应重复发事件
	assert.Empty(t, FinalizeClaudeStream(info))
}
