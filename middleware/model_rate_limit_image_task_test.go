package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageTaskRequestsBypassModelRequestRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "submit generation", method: http.MethodPost, path: "/v1/images/generations/async"},
		{name: "submit edit", method: http.MethodPost, path: "/v1/images/edits/async"},
		{name: "poll by task", method: http.MethodGet, path: "/v1/images/tasks/:task_id"},
		{name: "recover by request", method: http.MethodGet, path: "/v1/images/tasks/by-request/:request_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := gin.New()
			var bypassed bool
			engine.Handle(test.method, test.path, func(context *gin.Context) {
				bypassed = bypassModelRequestRateLimit(context)
				context.Status(http.StatusNoContent)
			})
			requestPath := test.path
			requestPath = strings.Replace(requestPath, ":task_id", "task-123", 1)
			requestPath = strings.Replace(requestPath, ":request_id", "request-123", 1)
			request := httptest.NewRequest(test.method, requestPath, nil)
			engine.ServeHTTP(httptest.NewRecorder(), request)
			require.True(t, bypassed)
		})
	}
}

func TestOnlyLoopbackInternalImageWorkerBypassesModelRequestRateLimit(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	context.Request.RemoteAddr = "127.0.0.1:43210"
	context.Request.Header.Set("X-NewMax-Internal-Image-Task", "1")
	context.Request.Header.Set(common.RequestIdKey, "img_internal_12345678")
	require.True(t, bypassModelRequestRateLimit(context))

	publicContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	publicContext.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	publicContext.Request.RemoteAddr = "203.0.113.10:43210"
	publicContext.Request.Header.Set("X-NewMax-Internal-Image-Task", "1")
	publicContext.Request.Header.Set(common.RequestIdKey, "img_public_12345678")
	require.False(t, bypassModelRequestRateLimit(publicContext))
}
