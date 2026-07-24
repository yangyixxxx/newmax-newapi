package ali

import (
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
)

// wan2.6/2.7 图像生成模型（含 wan2.7-image-pro 变体）必须识别为「万相生成模型」，
// 老版 wanx 与非 wan 模型不得命中。
func TestIsWanGenModel(t *testing.T) {
	gen := []string{"wan2.7-image-pro", "wan2.7", "wan2.6", "wan2.7-t2v"}
	for _, m := range gen {
		if !isWanGenModel(m) {
			t.Errorf("应识别为万相生成模型: %s", m)
		}
	}
	notGen := []string{"wanx2.1-t2i-turbo", "qwen-image", "z-image", "gpt-image-2"}
	for _, m := range notGen {
		if isWanGenModel(m) {
			t.Errorf("不应识别为万相生成模型: %s", m)
		}
	}
}

// 回归：wan2.7-image-pro 走 images/generations 时必须路由到万相 image-generation 端点，
// 而不是 multimodal-generation（messages 输入）——后者会被上游 400 messages 不兼容。
func TestGetRequestURL_ImagesGenerations_WanRoutesToImageGeneration(t *testing.T) {
	a := &Adaptor{}
	cases := []struct {
		model    string
		wantPart string
	}{
		{"wan2.7-image-pro", "/aigc/image-generation/generation"},
		{"wan2.7", "/aigc/image-generation/generation"},
		{"qwen-image", "/aigc/multimodal-generation/generation"},
		{"flux-dev", "/aigc/text2image/image-synthesis"},
	}
	for _, tc := range cases {
		info := &relaycommon.RelayInfo{
			OriginModelName: tc.model,
			RelayMode:       constant.RelayModeImagesGenerations,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl: "https://dashscope.aliyuncs.com",
			},
		}
		url, err := a.GetRequestURL(info)
		if err != nil {
			t.Fatalf("%s: GetRequestURL 报错: %v", tc.model, err)
		}
		if !strings.Contains(url, tc.wantPart) {
			t.Errorf("%s: 期望路由包含 %q, 实际 %q", tc.model, tc.wantPart, url)
		}
	}
}
