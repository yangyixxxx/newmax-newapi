package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNewmaxAccountDeleted = errors.New("account_deleted")

// UID 是控制面账号，而非所有影子 token 共用的 New API root user_id。
type NewmaxAccountOwnership struct {
	UID             int64 `gorm:"primaryKey;autoIncrement:false"`
	HistoryVerified bool
	Deleted         bool
}

func (NewmaxAccountOwnership) TableName() string { return "newmax_account_ownership" }

type NewmaxOwnedToken struct {
	TokenID int   `gorm:"primaryKey;autoIncrement:false"`
	UID     int64 `gorm:"index"`
}

func (NewmaxOwnedToken) TableName() string { return "newmax_owned_tokens" }

type NewmaxAccountOperation struct {
	ID         string `gorm:"primaryKey;size:64"`
	UID        int64  `gorm:"index"`
	OwnerScope string
	OwnerPID   int `gorm:"column:owner_pid"`
}

func (NewmaxAccountOperation) TableName() string { return "newmax_account_operations" }

type NewmaxRevokedKey struct {
	KeyHash string `gorm:"primaryKey;size:64"`
	UID     int64  `gorm:"index"`
}

func (NewmaxRevokedKey) TableName() string { return "newmax_revoked_keys" }

func NewmaxAccountDeletionEnabled() bool { return os.Getenv("NEWMAX_ACCOUNT_DELETION_KEY") != "" }
func newmaxKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// BeginNewmaxTokenOperation 持久登记可能晚写私人数据的操作。删除先封禁再等待计数归零。
// 操作失败也必须释放；进程崩溃留下的登记不自动超时，避免慢请求被错误认定已经停止。
func BeginNewmaxTokenOperation(tokenID int) (func() error, error) {
	noop := func() error { return nil }
	if !NewmaxAccountDeletionEnabled() || tokenID == 0 {
		return noop, nil
	}
	var operation NewmaxAccountOperation
	err := DB.Transaction(func(tx *gorm.DB) error {
		var owner NewmaxOwnedToken
		err := tx.Where("token_id = ?", tokenID).First(&owner).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var account NewmaxAccountOwnership
		if err = lockForUpdate(tx).Where("uid = ?", owner.UID).First(&account).Error; err != nil {
			return err
		}
		if account.Deleted {
			return ErrNewmaxAccountDeleted
		}
		operation = NewmaxAccountOperation{ID: common.NewRequestId(), UID: account.UID, OwnerScope: newmaxProcessScope(), OwnerPID: os.Getpid()}
		return tx.Create(&operation).Error
	})
	if err != nil {
		return nil, err
	}
	if operation.ID == "" {
		return noop, nil
	}
	return func() error { return DB.Where("id = ?", operation.ID).Delete(&NewmaxAccountOperation{}).Error }, nil
}

func FreezeNewmaxAccount(uid int64) error {
	if uid <= 0 {
		return errors.New("invalid uid")
	}
	var historyVerified bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		var account NewmaxAccountOwnership
		if err := lockForUpdate(tx).Where("uid = ?", uid).First(&account).Error; err != nil {
			return err
		}
		historyVerified = account.HistoryVerified
		var tokens []Token
		if err := tx.Unscoped().Where("id IN (?)", tx.Model(&NewmaxOwnedToken{}).Select("token_id").Where("uid = ?", uid)).Find(&tokens).Error; err != nil {
			return err
		}
		for _, token := range tokens {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&NewmaxRevokedKey{KeyHash: newmaxKeyHash(token.Key), UID: uid}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&account).Update("deleted", true).Error
	})
	if err != nil {
		return err
	}
	// 历史不完整也必须先冻结已知凭据；但不能据此声称已清理全部历史。
	if !historyVerified {
		return errors.New("token_history_ownership_unverified")
	}
	return nil
}
func NewmaxAccountQuiescent(uid int64) error {
	var operations []NewmaxAccountOperation
	if err := DB.Where("uid = ?", uid).Find(&operations).Error; err != nil {
		return err
	}
	scope := newmaxProcessScope()
	for _, operation := range operations {
		if newmaxOperationOwnerStopped(operation, scope) {
			if err := DB.Where("id = ? AND uid = ? AND owner_scope = ? AND owner_pid = ?", operation.ID, uid, scope, operation.OwnerPID).Delete(&NewmaxAccountOperation{}).Error; err != nil {
				return err
			}
		}
	}
	var n int64
	if err := DB.Model(&NewmaxAccountOperation{}).Where("uid = ?", uid).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return errors.New("account_operations_pending")
	}
	return nil
}

// ValidateNewmaxTokenKey 每次读取缓存前查询持久墓碑，缓存晚回填也无法恢复访问。
func ValidateNewmaxTokenKey(key string) error {
	if !NewmaxAccountDeletionEnabled() {
		return nil
	}
	var n int64
	if err := DB.Model(&NewmaxRevokedKey{}).Where("key_hash = ?", newmaxKeyHash(key)).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return ErrNewmaxAccountDeleted
	}
	return nil
}
