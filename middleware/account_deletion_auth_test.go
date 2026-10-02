package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 删除墓碑是确定的凭据失效，而查库失败必须保留 500，不能混淆两种状态。
func TestDeletedAccountTokenAuthenticationStatus(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, mode := range []struct {
		name       string
		middleware gin.HandlerFunc
	}{
		{"readonly", TokenAuthReadOnly()},
		{"relay", TokenAuth()},
	} {
		for _, unavailable := range []bool{false, true} {
			name := mode.name + "/revoked"
			if unavailable {
				name = mode.name + "/database-unavailable"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "synthetic-auth-status-test")
				db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				previousDB, previousRedis := model.DB, common.RedisEnabled
				model.DB, common.RedisEnabled = db, false
				t.Cleanup(func() {
					model.DB, common.RedisEnabled = previousDB, previousRedis
					_ = sqlDB.Close()
				})
				require.NoError(t, db.AutoMigrate(&model.NewmaxRevokedKey{}))
				key := "syntheticrevokedkey"
				digest := sha256.Sum256([]byte(key))
				require.NoError(t, db.Create(&model.NewmaxRevokedKey{KeyHash: hex.EncodeToString(digest[:]), UID: 991}).Error)
				if unavailable {
					require.NoError(t, sqlDB.Close())
				}
				router := gin.New()
				called := false
				router.GET("/probe", mode.middleware, func(c *gin.Context) { called = true; c.Status(http.StatusOK) })
				request := httptest.NewRequest(http.MethodGet, "/probe", nil)
				request.Header.Set("Authorization", "Bearer sk-"+key)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				want := http.StatusUnauthorized
				if unavailable {
					want = http.StatusInternalServerError
				}
				assert.Equal(t, want, response.Code)
				assert.False(t, called, "无效凭据和数据库故障均不得进入业务处理器")
			})
		}
	}
}
