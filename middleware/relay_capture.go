package middleware

import (
	"bytes"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// relayBodyCaptureEnabled 总开关（默认开；设 RELAY_BODY_CAPTURE=false 可关）。
var relayBodyCaptureEnabled = common.GetEnvOrDefaultBool("RELAY_BODY_CAPTURE", true)

// relayBodyMaxBytes 单个 body（请求或响应）落库上限，防止超大响应（图像 base64、
// 视频回包）把整条流缓冲进内存拖垮宿主机。0 或负数视为不限（不建议）。默认 16MB。
var relayBodyMaxBytes = common.GetEnvOrDefault("RELAY_BODY_MAX_BYTES", 16<<20)

// captureWriter 包装 gin.ResponseWriter：转发写出的同时把响应体 tee 一份到有限缓冲区。
// 复用 audit.go 里 auditResponseWriter 的思路——嵌入接口，Flush/Hijack/CloseNotify 等
// 方法自动提升，流式响应照常边写边刷，只是多留一份副本。
type captureWriter struct {
	gin.ResponseWriter
	body      *bytes.Buffer
	maxSize   int
	truncated bool
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.tee(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.tee([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *captureWriter) tee(b []byte) {
	if w.maxSize > 0 && w.body.Len() >= w.maxSize {
		w.truncated = true
		return
	}
	if w.maxSize > 0 && w.body.Len()+len(b) > w.maxSize {
		w.body.Write(b[:w.maxSize-w.body.Len()])
		w.truncated = true
		return
	}
	w.body.Write(b)
}

// RelayBodyCapture 捕获中转请求的完整请求体与响应体，异步写入 relay_bodies。
// 只对有 body 的写请求生效；只读请求（如 /v1/models）直接放行不包装。
// best-effort：任何一步失败都不影响主请求。
func RelayBodyCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !relayBodyCaptureEnabled || c.Request.Method == "GET" || c.Request.Method == "DELETE" {
			c.Next()
			return
		}

		writer := &captureWriter{
			ResponseWriter: c.Writer,
			body:           bytes.NewBuffer(nil),
			maxSize:        relayBodyMaxBytes,
		}
		c.Writer = writer

		c.Next()

		// 请求体：从缓存的 BodyStorage 取全量原文（鉴权/中转链路已读过并缓存）。
		var reqBody string
		reqTruncated := false
		if bs, err := common.GetBodyStorage(c); err == nil {
			if raw, berr := bs.Bytes(); berr == nil {
				if relayBodyMaxBytes > 0 && len(raw) > relayBodyMaxBytes {
					raw = raw[:relayBodyMaxBytes]
					reqTruncated = true
				}
				reqBody = string(raw)
			}
		}

		rb := &model.RelayBody{
			RequestId:    c.GetString(common.RequestIdKey),
			UserId:       c.GetInt("id"),
			TokenId:      c.GetInt("token_id"),
			TokenName:    c.GetString("token_name"),
			ChannelId:    common.GetContextKeyInt(c, constant.ContextKeyChannelId),
			ModelName:    c.GetString("original_model"),
			RequestPath:  c.Request.URL.Path,
			IsStream:     writer.Header().Get("Content-Type") == "text/event-stream" || bytes.Contains(writer.body.Bytes(), []byte("data:")),
			StatusCode:   writer.Status(),
			RequestBody:  reqBody,
			ResponseBody: writer.body.String(),
			Truncated:    writer.truncated || reqTruncated,
			CreatedAt:    time.Now().Unix(),
		}

		gopool.Go(func() {
			model.RecordRelayBody(rb)
		})
	}
}
