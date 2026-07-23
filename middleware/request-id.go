package middleware

import (
	"context"
	"net"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

var clientRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)

func acceptedClientRequestID(c *gin.Context) string {
	if c.GetHeader("X-NewMax-Internal-Image-Task") != "1" {
		return ""
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return ""
	}
	id := strings.TrimSpace(c.GetHeader(common.RequestIdKey))
	if clientRequestIDPattern.MatchString(id) {
		return id
	}
	return ""
}

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		id := acceptedClientRequestID(c)
		if id == "" {
			id = common.NewRequestId()
		}
		c.Set(common.RequestIdKey, id)
		ctx := context.WithValue(c.Request.Context(), common.RequestIdKey, id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(common.RequestIdKey, id)
		c.Next()
	}
}
