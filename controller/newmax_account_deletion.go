package controller

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var newmaxTaskDirectoryPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DeleteNewmaxAccount 仅供控制面服务调用，不复用 root 用户的浏览器会话。
func DeleteNewmaxAccount(c *gin.Context) {
	key := os.Getenv("NEWMAX_ACCOUNT_DELETION_KEY")
	if strings.TrimSpace(key) == "" {
		c.JSON(503, gin.H{"success": false})
		return
	}
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+key)) != 1 {
		c.JSON(401, gin.H{"success": false})
		return
	}
	var target struct {
		UID int64 `json:"uid"`
	}
	body, readErr := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	var fields map[string]json.RawMessage
	// Unmarshal 验证整个受限正文，不忽略 decoder 缓冲中的尾随文档。
	if readErr != nil || common.Unmarshal(body, &fields) != nil || len(fields) != 1 || fields["uid"] == nil {
		c.JSON(400, gin.H{"success": false})
		return
	}
	if err := common.Unmarshal(fields["uid"], &target.UID); err != nil || target.UID <= 0 {
		c.JSON(400, gin.H{"success": false})
		return
	}
	if err := model.FreezeNewmaxAccount(target.UID); err != nil {
		c.JSON(503, gin.H{"success": false, "stage": "ownership_or_freeze_pending"})
		return
	}
	if err := model.NewmaxAccountQuiescent(target.UID); err != nil {
		c.JSON(503, gin.H{"success": false, "stage": "operations_pending"})
		return
	}
	ids, err := model.NewmaxAccountTokenIDs(target.UID)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "stage": "cleanup_pending"})
		return
	}
	if err = deleteNewmaxImageTaskFiles(ids); err != nil {
		c.JSON(503, gin.H{"success": false, "stage": "files_pending"})
		return
	}
	if err = model.DeleteNewmaxAccountRecords(target.UID, ids); err != nil {
		c.JSON(503, gin.H{"success": false, "stage": "cleanup_pending"})
		return
	}
	c.JSON(200, gin.H{"success": true, "accountDeleted": true, "uid": target.UID})
}
