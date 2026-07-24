package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
)

// 生成类模型必须被识别（护栏据此在对话端点拦截），对话模型必须放行。
func TestIsImageOrVideoGenModel(t *testing.T) {
	blocked := []string{"gpt-image-2", "GPT-Image-2", "dall-e-3", "imagen-3", "seedream-3", "kling-v2", "jimeng-video", "vidu-2", "seedance-1"}
	for _, m := range blocked {
		assert.True(t, IsImageOrVideoGenModel(m), "应识别为生成类模型: %s", m)
	}
	allowed := []string{"doubao-seed-2.1-pro", "deepseek-v4-pro", "glm-5.2", "gpt-4o", "claude-sonnet-5", "gemini-2.5-flash-image-preview"}
	for _, m := range allowed {
		assert.False(t, IsImageOrVideoGenModel(m), "对话/多模态模型不应被拦: %s", m)
	}
}

// 只有对话补全类文本端点才拦截；真正的图像端点必须放行。
func TestIsChatCompletionFormat(t *testing.T) {
	assert.True(t, IsChatCompletionFormat(types.RelayFormatOpenAI))
	assert.True(t, IsChatCompletionFormat(types.RelayFormatClaude))
	assert.False(t, IsChatCompletionFormat(types.RelayFormatOpenAIImage), "图像端点不能被当对话端点拦截")
	assert.False(t, IsChatCompletionFormat(types.RelayFormatEmbedding))
}
