package ali

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func oaiFormEdit2WanxImageEdit(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (*AliImageRequest, error) {
	var err error
	var imageRequest AliImageRequest
	imageRequest.Model = request.Model
	imageRequest.ResponseFormat = request.ResponseFormat
	wanInput := WanImageInput{
		Prompt: request.Prompt,
	}

	if err := common.UnmarshalBodyReusable(c, &wanInput); err != nil {
		return nil, err
	}
	if wanInput.Images, err = getImageBase64sFromForm(c, "image"); err != nil {
		return nil, fmt.Errorf("get image base64s from form failed: %w", err)
	}
	//wanParams := WanImageParameters{
	//	N: int(request.N),
	//}
	imageRequest.Input = wanInput
	imageRequest.Parameters = AliImageParameters{
		N: int(lo.FromPtrOr(request.N, uint(1))),
	}
	info.PriceData.AddOtherRatio("n", float64(imageRequest.Parameters.N))

	return &imageRequest, nil
}

func isOldWanModel(modelName string) bool {
	return strings.Contains(modelName, "wan") &&
		!lo.SomeBy([]string{"wan2.6", "wan2.7"}, func(v string) bool { return strings.Contains(modelName, v) })
}

func isWanModel(modelName string) bool {
	return strings.Contains(modelName, "wan")
}

// isWanGenModel 命中新版万相图像生成模型（wan2.6/wan2.7，含 wan2.7-image-pro 等变体）。
// 这类模型虽然会被 IsSyncImageModel 命中（sync 白名单含 "wan2.6"/"wan2.7"），但走的是万相
// image-generation/generation 端点、只接受 prompt 输入；若按 sync 多模态模型发 messages 输入
// 会被上游 400 拒（messages 格式不兼容）。与 RelayModeImagesEdits 的 wan 处理保持一致。
func isWanGenModel(modelName string) bool {
	return isWanModel(modelName) && !isOldWanModel(modelName)
}
