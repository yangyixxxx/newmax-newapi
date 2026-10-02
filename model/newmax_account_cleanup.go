package model

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
)

// NewmaxAccountTokenIDs 返回经过迁移核实的不可变归属，不按邮箱或 root user_id 扩大范围。
func NewmaxAccountTokenIDs(uid int64) ([]int, error) {
	var ids []int
	err := DB.Model(&NewmaxOwnedToken{}).Where("uid = ?", uid).Pluck("token_id", &ids).Error
	return ids, err
}

func DeleteNewmaxAccountRecords(uid int64, ids []int) error {
	var account NewmaxAccountOwnership
	if err := DB.Where("uid = ?", uid).First(&account).Error; err != nil {
		return err
	}
	if !account.Deleted || !account.HistoryVerified {
		return errors.New("account_not_frozen")
	}
	if err := NewmaxAccountQuiescent(uid); err != nil {
		return err
	}
	verifiedIDs, err := NewmaxAccountTokenIDs(uid)
	if err != nil {
		return err
	}
	expected := make(map[int]bool, len(verifiedIDs))
	for _, id := range verifiedIDs {
		expected[id] = true
	}
	if len(ids) != len(verifiedIDs) {
		return errors.New("token_target_mismatch")
	}
	for _, id := range ids {
		if !expected[id] {
			return errors.New("token_target_mismatch")
		}
		delete(expected, id)
	}
	if len(expected) != 0 {
		return errors.New("token_target_mismatch")
	}
	// 已冻结后不得产生新的明文缓存；保留 token 行到缓存清理成功，便于故障重试。
	var tokens []Token
	if err := DB.Unscoped().Where("id IN ?", ids).Find(&tokens).Error; err != nil {
		return err
	}
	if common.RedisEnabled {
		for _, token := range tokens {
			if err := cacheDeleteToken(token.Key); err != nil {
				return err
			}
		}
	}
	// 必要用量保留，个人正文、IP、可归因名称和请求标识去除。
	if err := LOG_DB.Model(&Log{}).Where("token_id IN ?", ids).Updates(map[string]any{"token_name": "", "username": "", "content": "", "ip": "", "other": "{}", "request_id": "", "upstream_request_id": ""}).Error; err != nil {
		return err
	}
	if err := LOG_DB.Where("token_id IN ?", ids).Delete(&RelayBody{}).Error; err != nil {
		return err
	}
	// 通用异步任务的 token_id 位于结构化 private_data，逐行解析避免跨数据库 JSON 方言和误删 root 下其他用户。
	owned := make(map[int]bool, len(ids))
	for _, id := range ids {
		owned[id] = true
	}
	var tasks []Task
	if err := DB.Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		if owned[task.PrivateData.TokenId] {
			if err := DB.Where("id = ?", task.ID).Delete(&Task{}).Error; err != nil {
				return err
			}
		}
	}
	if err := DB.Where("token_id IN ?", ids).Delete(&ImageTask{}).Error; err != nil {
		return err
	}
	return DB.Unscoped().Where("id IN ?", ids).Delete(&Token{}).Error
}
