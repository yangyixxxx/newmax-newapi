package model

import "gorm.io/gorm/clause"

// 在创建文件之前保存精确目录归属，覆盖任务行尚未提交时进程崩溃的窗口。
// TaskID 为随机内部目录名，不能是数据库中任意路径。
type NewmaxImageTaskDirectory struct {
	TaskID  string `gorm:"primaryKey;size:128;column:task_id"`
	TokenID int    `gorm:"index;column:token_id"`
}

func (NewmaxImageTaskDirectory) TableName() string { return "newmax_image_task_directories" }

func RegisterNewmaxImageTaskDirectory(taskID string, tokenID int) error {
	if !NewmaxAccountDeletionEnabled() {
		return nil
	}
	release, err := BeginNewmaxTokenOperation(tokenID)
	if err != nil {
		return err
	}
	defer release()
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&NewmaxImageTaskDirectory{TaskID: taskID, TokenID: tokenID}).Error
}
