package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWriter 必须在超过上限后停止缓冲并置 truncated，且转发给底层 writer 的字节
// 保持完整（截断只影响留存的副本，绝不影响真正发给客户端的响应）。
func TestCaptureWriterTruncatesCopyNotClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	rec := httptest.NewRecorder()
	c.Writer = &fakeWriter{ResponseWriter: c.Writer, rec: rec}

	w := &captureWriter{ResponseWriter: c.Writer, body: bytes.NewBuffer(nil), maxSize: 10}

	// 分两次写共 16 字节，上限 10。
	_, err := w.Write([]byte("abcdefgh")) // 8 字节，未超
	require.NoError(t, err)
	_, err = w.Write([]byte("XXXXXXXX")) // 再 8 字节，跨过上限
	require.NoError(t, err)

	assert.True(t, w.truncated, "跨上限应置 truncated")
	assert.Equal(t, 10, w.body.Len(), "留存副本应恰好截到上限")
	assert.Equal(t, "abcdefghXX", w.body.String())
	assert.Equal(t, "abcdefghXXXXXXXX", rec.Body.String(), "客户端收到的字节必须完整")
}

// maxSize<=0 表示不限，全部缓冲、不置 truncated。
func TestCaptureWriterUnlimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	rec := httptest.NewRecorder()
	c.Writer = &fakeWriter{ResponseWriter: c.Writer, rec: rec}

	w := &captureWriter{ResponseWriter: c.Writer, body: bytes.NewBuffer(nil), maxSize: 0}
	_, err := w.Write(bytes.Repeat([]byte("y"), 1000))
	require.NoError(t, err)

	assert.False(t, w.truncated)
	assert.Equal(t, 1000, w.body.Len())
}

// fakeWriter 把 Write 落到独立 recorder，便于断言"发给客户端的完整字节"。
type fakeWriter struct {
	gin.ResponseWriter
	rec *httptest.ResponseRecorder
}

func (f *fakeWriter) Write(b []byte) (int, error)       { return f.rec.Write(b) }
func (f *fakeWriter) WriteString(s string) (int, error) { return f.rec.WriteString(s) }
