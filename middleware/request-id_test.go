package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestIDPreservesInternalImageTaskCorrelationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequestId())
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(common.RequestIdKey))
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("X-NewMax-Internal-Image-Task", "1")
	request.Header.Set(common.RequestIdKey, "img_12345678-abcd")
	response := httptest.NewRecorder()

	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "img_12345678-abcd", response.Body.String())
	require.Equal(t, "img_12345678-abcd", response.Header().Get(common.RequestIdKey))
}

func TestRequestIDDoesNotTrustPublicClientCorrelationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequestId())
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(common.RequestIdKey))
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(common.RequestIdKey, "img_public_12345678")
	response := httptest.NewRecorder()

	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.NotEqual(t, "img_public_12345678", response.Body.String())
	require.NotEmpty(t, response.Body.String())
}
