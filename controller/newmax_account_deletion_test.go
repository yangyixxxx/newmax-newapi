package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewmaxDeletionRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, key, auth, body string
		want                  int
	}{
		{"empty config", "", "", `{"uid":701}`, 503},
		{"whitespace config", " ", "Bearer  ", `{"uid":701}`, 503},
		{"missing auth", "test-key", "", `{"uid":701}`, 401},
		{"wrong auth", "test-key", "Bearer wrong", `{"uid":701}`, 401},
		{"negative", "test-key", "Bearer test-key", `{"uid":-1}`, 400},
		{"fraction", "test-key", "Bearer test-key", `{"uid":1.5}`, 400},
		{"unknown target", "test-key", "Bearer test-key", `{"uid":701,"tokenIDs":[999]}`, 400},
		{"trailing document", "test-key", "Bearer test-key", `{"uid":701} {"uid":999}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", tc.key)
			db := openTokenControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.NewmaxAccountOwnership{}))
			require.NoError(t, db.Create(&model.NewmaxAccountOwnership{UID: 701, HistoryVerified: false}).Error)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/internal/newmax/accounts/delete", strings.NewReader(tc.body))
			c.Request.Header.Set("Authorization", tc.auth)
			DeleteNewmaxAccount(c)
			assert.Equal(t, tc.want, w.Code, w.Body.String())
			var owner model.NewmaxAccountOwnership
			require.NoError(t, db.First(&owner, "uid = ?", 701).Error)
			assert.False(t, owner.Deleted)
		})
	}
}

func newmaxDeletionFixture(t *testing.T) {
	t.Helper()
	t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "test-key")
	t.Setenv("IMAGE_TASK_STORAGE_DIR", t.TempDir())
	db := openTokenControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.NewmaxAccountOwnership{}, &model.NewmaxOwnedToken{}, &model.NewmaxAccountOperation{}, &model.NewmaxRevokedKey{}, &model.NewmaxImageTaskDirectory{}, &model.Token{}, &model.Log{}, &model.RelayBody{}, &model.Task{}, &model.ImageTask{}))
	for _, uid := range []int64{701, 702} {
		require.NoError(t, db.Create(&model.NewmaxAccountOwnership{UID: uid, HistoryVerified: true}).Error)
		token := model.Token{Id: int(uid + 100), UserId: 1, Key: "synthetic-key-" + string(rune(uid)), Name: "private-email", Status: common.TokenStatusEnabled, ExpiredTime: -1}
		require.NoError(t, db.Create(&token).Error)
		require.NoError(t, db.Create(&model.NewmaxOwnedToken{UID: uid, TokenID: token.Id}).Error)
	}
}

func requestNewmaxDeletion(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/internal/newmax/accounts/delete", strings.NewReader(`{"uid":701}`))
	c.Request.Header.Set("Authorization", "Bearer test-key")
	DeleteNewmaxAccount(c)
	return w
}

func TestNewmaxDeletionCleansCrashOrphansAndRejectsUnknownFiles(t *testing.T) {
	newmaxDeletionFixture(t)
	require.NoError(t, model.DB.AutoMigrate(&model.NewmaxImageTaskDirectory{}))
	require.NoError(t, model.RegisterNewmaxImageTaskDirectory("crash-owned", 801))
	require.NoError(t, model.RegisterNewmaxImageTaskDirectory("other-owned", 802))
	for _, id := range []string{"crash-owned", "other-owned", "unknown-legacy"} {
		require.NoError(t, os.MkdirAll(filepath.Join(imageTaskStorageDir(), id), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(imageTaskStorageDir(), id, "request.bin"), []byte("synthetic private payload"), 0600))
	}
	pending := requestNewmaxDeletion(t)
	assert.Equal(t, 503, pending.Code, pending.Body.String())
	assert.NotContains(t, pending.Body.String(), `"accountDeleted":true`)
	// 未知归属必须阻塞，不能扩大删除范围；目标注册信息也必须保留供重试。
	require.FileExists(t, filepath.Join(imageTaskStorageDir(), "other-owned", "request.bin"))
	require.NoError(t, os.RemoveAll(filepath.Join(imageTaskStorageDir(), "unknown-legacy")))
	terminal := requestNewmaxDeletion(t)
	assert.Equal(t, 200, terminal.Code, terminal.Body.String())
	assert.NoDirExists(t, filepath.Join(imageTaskStorageDir(), "crash-owned"))
	assert.FileExists(t, filepath.Join(imageTaskStorageDir(), "other-owned", "request.bin"))
}

func TestNewmaxDeletionTerminalAndRetryIsolation(t *testing.T) {
	newmaxDeletionFixture(t)
	release, err := model.BeginNewmaxTokenOperation(801)
	require.NoError(t, err)
	w := requestNewmaxDeletion(t)
	assert.Equal(t, 503, w.Code)
	assert.NotContains(t, w.Body.String(), `"accountDeleted":true`)
	require.ErrorIs(t, model.ValidateNewmaxTokenKey("synthetic-key-"+string(rune(701))), model.ErrNewmaxAccountDeleted)
	require.NoError(t, model.ValidateNewmaxTokenKey("synthetic-key-"+string(rune(702))))
	require.NoError(t, release())
	for i := 0; i < 2; i++ {
		w = requestNewmaxDeletion(t)
		assert.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"accountDeleted":true`)
		var count int64
		require.NoError(t, model.DB.Unscoped().Model(&model.Token{}).Where("id = ?", 801).Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", 802).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	}
}
