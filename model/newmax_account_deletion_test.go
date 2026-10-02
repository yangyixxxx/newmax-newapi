package model

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os/exec"
	"testing"
)

func TestNewmaxOperationCrashRecoveryRequiresStoppedLocalProcess(t *testing.T) {
	t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "synthetic-test-admin")
	require.NoError(t, DB.AutoMigrate(&NewmaxAccountOperation{}))
	scope := newmaxProcessScope()
	require.NotEmpty(t, scope)
	child := exec.Command("sleep", "60")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
		DB.Where("uid = ?", 703).Delete(&NewmaxAccountOperation{})
	})
	require.NoError(t, DB.Create(&NewmaxAccountOperation{ID: "crash-recovery", UID: 703, OwnerScope: scope, OwnerPID: child.Process.Pid}).Error)
	require.Error(t, NewmaxAccountQuiescent(703))
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait())
	require.NoError(t, NewmaxAccountQuiescent(703))
	// 不同命名空间和旧版无归属的登记都不能凭本机 PID 或超时清除。
	require.NoError(t, DB.Create(&NewmaxAccountOperation{ID: "remote", UID: 703, OwnerScope: "other-namespace", OwnerPID: child.Process.Pid}).Error)
	require.NoError(t, DB.Create(&NewmaxAccountOperation{ID: "legacy", UID: 703}).Error)
	require.Error(t, NewmaxAccountQuiescent(703))
	var count int64
	require.NoError(t, DB.Model(&NewmaxAccountOperation{}).Where("uid = ?", 703).Count(&count).Error)
	assert.EqualValues(t, 2, count)
}

func TestNewmaxCleanupRejectsOtherAccountTokenIDs(t *testing.T) {
	t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "synthetic-test-admin")
	require.NoError(t, DB.AutoMigrate(&NewmaxAccountOwnership{}, &NewmaxOwnedToken{}, &NewmaxAccountOperation{}, &RelayBody{}, &Log{}, &Task{}, &ImageTask{}, &Token{}))
	require.NoError(t, DB.Create(&NewmaxAccountOwnership{UID: 704, HistoryVerified: true, Deleted: true}).Error)
	require.NoError(t, DB.Create(&NewmaxOwnedToken{UID: 704, TokenID: 804}).Error)
	t.Cleanup(func() {
		DB.Where("uid = ?", 704).Delete(&NewmaxOwnedToken{})
		DB.Where("uid = ?", 704).Delete(&NewmaxAccountOwnership{})
	})
	require.Error(t, DeleteNewmaxAccountRecords(704, []int{805}))
}

func TestNewmaxUnverifiedHistoryStillRevokesKnownCredentials(t *testing.T) {
	t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "synthetic-test-admin")
	require.NoError(t, DB.AutoMigrate(&NewmaxAccountOwnership{}, &NewmaxOwnedToken{}, &NewmaxRevokedKey{}))
	require.NoError(t, DB.Create(&NewmaxAccountOwnership{UID: 705}).Error)
	require.NoError(t, DB.Create(&NewmaxOwnedToken{UID: 705, TokenID: 805}).Error)
	require.NoError(t, DB.Create(&Token{Id: 805, Key: "synthetic-unverified"}).Error)
	t.Cleanup(func() {
		DB.Where("uid = ?", 705).Delete(&NewmaxRevokedKey{})
		DB.Where("uid = ?", 705).Delete(&NewmaxOwnedToken{})
		DB.Where("uid = ?", 705).Delete(&NewmaxAccountOwnership{})
		DB.Unscoped().Where("id = ?", 805).Delete(&Token{})
	})
	require.Error(t, FreezeNewmaxAccount(705))
	require.ErrorIs(t, ValidateNewmaxTokenKey("synthetic-unverified"), ErrNewmaxAccountDeleted)
	_, err := BeginNewmaxTokenOperation(805)
	require.ErrorIs(t, err, ErrNewmaxAccountDeleted)
}

func TestNewmaxDeletedAccountRejectsTokenOperations(t *testing.T) {
	t.Setenv("NEWMAX_ACCOUNT_DELETION_KEY", "synthetic-test-admin")
	require.NoError(t, DB.AutoMigrate(&NewmaxAccountOwnership{}, &NewmaxOwnedToken{}, &NewmaxAccountOperation{}, &NewmaxRevokedKey{}))
	t.Cleanup(func() {
		DB.Exec("DELETE FROM newmax_account_operations")
		DB.Exec("DELETE FROM newmax_revoked_keys")
		DB.Exec("DELETE FROM newmax_owned_tokens")
		DB.Exec("DELETE FROM newmax_account_ownership")
	})
	require.NoError(t, DB.Create(&NewmaxAccountOwnership{UID: 701, HistoryVerified: true}).Error)
	require.NoError(t, DB.Create(&NewmaxOwnedToken{TokenID: 801, UID: 701}).Error)
	release, err := BeginNewmaxTokenOperation(801)
	require.NoError(t, err)
	require.NoError(t, FreezeNewmaxAccount(701))
	require.Error(t, NewmaxAccountQuiescent(701))
	_, err = BeginNewmaxTokenOperation(801)
	require.Error(t, err)
	require.NoError(t, release())
	require.NoError(t, NewmaxAccountQuiescent(701))
	release, err = BeginNewmaxTokenOperation(999)
	require.NoError(t, err)
	require.NoError(t, release())
}
