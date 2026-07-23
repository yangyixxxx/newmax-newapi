package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ImageTaskStatus string

const (
	ImageTaskStatusQueued     ImageTaskStatus = "queued"
	ImageTaskStatusProcessing ImageTaskStatus = "processing"
	ImageTaskStatusSucceeded  ImageTaskStatus = "succeeded"
	ImageTaskStatusFailed     ImageTaskStatus = "failed"
)

var ErrImageTaskStateConflict = errors.New("image task state changed before update")

// ImageTask persists one Gateway-owned image request. The token/request pair is
// unique so retrying an async submission can only ever create one billable job.
// TokenID is the ownership boundary because NewMax customer shadow tokens may
// share one New API root user.
type ImageTask struct {
	ID               int64           `json:"-" gorm:"primaryKey;autoIncrement"`
	CreatedAt        int64           `json:"created_at" gorm:"index"`
	UpdatedAt        int64           `json:"updated_at"`
	TaskID           string          `json:"task_id" gorm:"type:varchar(64);uniqueIndex"`
	RequestID        string          `json:"request_id" gorm:"type:varchar(64);uniqueIndex:idx_image_tasks_token_request"`
	UserID           int             `json:"-" gorm:"index"`
	TokenID          int             `json:"-" gorm:"uniqueIndex:idx_image_tasks_token_request;index"`
	Endpoint         string          `json:"-" gorm:"type:varchar(64)"`
	ContentType      string          `json:"-" gorm:"type:varchar(255)"`
	Status           ImageTaskStatus `json:"status" gorm:"type:varchar(20);index"`
	ErrorMessage     string          `json:"-" gorm:"type:text"`
	RequestPath      string          `json:"-" gorm:"type:text"`
	ResultPath       string          `json:"-" gorm:"type:text"`
	BillingRequestID string          `json:"billing_request_id,omitempty" gorm:"type:varchar(64);index"`
	StartedAt        int64           `json:"started_at,omitempty"`
	FinishedAt       int64           `json:"finished_at,omitempty"`
	ExpiresAt        int64           `json:"expires_at,omitempty" gorm:"index"`
}

func CreateOrGetImageTask(task *ImageTask) (*ImageTask, bool, error) {
	if task == nil || task.TokenID == 0 || task.RequestID == "" {
		return nil, false, errors.New("token_id and request_id are required")
	}
	now := time.Now().Unix()
	task.CreatedAt = now
	task.UpdatedAt = now
	if task.TaskID == "" {
		task.TaskID = GenerateTaskID()
	}
	if task.Status == "" {
		task.Status = ImageTaskStatusQueued
	}
	result := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token_id"}, {Name: "request_id"}},
		DoNothing: true,
	}).Create(task)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return task, true, nil
	}
	existing, exists, err := GetImageTaskByRequestID(task.TokenID, task.RequestID)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, errors.New("image task conflict without existing row")
	}
	return existing, false, nil
}

func GetImageTaskByTaskID(tokenID int, taskID string) (*ImageTask, bool, error) {
	var task ImageTask
	err := DB.Where("token_id = ? AND task_id = ?", tokenID, taskID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &task, err == nil, err
}

func GetImageTaskByRequestID(tokenID int, requestID string) (*ImageTask, bool, error) {
	var task ImageTask
	err := DB.Where("token_id = ? AND request_id = ?", tokenID, requestID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &task, err == nil, err
}

func ClaimNextQueuedImageTask() (*ImageTask, bool, error) {
	for attempts := 0; attempts < 8; attempts++ {
		var task ImageTask
		query := DB.Where("status = ?", ImageTaskStatusQueued).
			Order("id ASC").
			Limit(1).
			Find(&task)
		if query.Error != nil {
			return nil, false, query.Error
		}
		if query.RowsAffected == 0 {
			return nil, false, nil
		}
		now := time.Now().Unix()
		result := DB.Model(&ImageTask{}).
			Where("id = ? AND status = ?", task.ID, ImageTaskStatusQueued).
			Updates(map[string]any{
				"status":     ImageTaskStatusProcessing,
				"started_at": now,
				"updated_at": now,
			})
		if result.Error != nil {
			return nil, false, result.Error
		}
		if result.RowsAffected == 1 {
			task.Status = ImageTaskStatusProcessing
			task.StartedAt = now
			task.UpdatedAt = now
			return &task, true, nil
		}
	}
	return nil, false, nil
}

func CompleteImageTask(taskID, resultPath, billingRequestID string, expiresAt int64) error {
	now := time.Now().Unix()
	result := DB.Model(&ImageTask{}).
		Where("task_id = ? AND status = ?", taskID, ImageTaskStatusProcessing).
		Updates(map[string]any{
			"status":             ImageTaskStatusSucceeded,
			"result_path":        resultPath,
			"billing_request_id": billingRequestID,
			"finished_at":        now,
			"updated_at":         now,
			"expires_at":         expiresAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImageTaskStateConflict
	}
	return nil
}

func FailImageTask(taskID, message, billingRequestID string) error {
	now := time.Now().Unix()
	result := DB.Model(&ImageTask{}).
		Where("task_id = ? AND status = ?", taskID, ImageTaskStatusProcessing).
		Updates(map[string]any{
			"status":             ImageTaskStatusFailed,
			"error_message":      message,
			"billing_request_id": billingRequestID,
			"finished_at":        now,
			"updated_at":         now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrImageTaskStateConflict
	}
	return nil
}

// A worker may die after the upstream accepted and charged the request but
// before the response was persisted. Stale processing tasks must never be
// automatically replayed, because replaying could charge the user a second time.
func FailStaleImageTasks(startedBefore int64) error {
	now := time.Now().Unix()
	return DB.Model(&ImageTask{}).
		Where("status = ? AND started_at > 0 AND started_at <= ?",
			ImageTaskStatusProcessing, startedBefore).
		Updates(map[string]any{
			"status":        ImageTaskStatusFailed,
			"error_message": "The image worker stopped while this request was in progress. The request was not retried to prevent duplicate billing; contact support with the request_id.",
			"finished_at":   now,
			"updated_at":    now,
		}).Error
}

func ListExpiredImageTaskFiles(now int64) ([]ImageTask, error) {
	var tasks []ImageTask
	err := DB.Where("status = ? AND expires_at > 0 AND expires_at <= ? AND result_path <> ''",
		ImageTaskStatusSucceeded, now).Find(&tasks).Error
	return tasks, err
}

func ClearImageTaskResultPath(taskID, resultPath string) error {
	return DB.Model(&ImageTask{}).
		Where("task_id = ? AND result_path = ?", taskID, resultPath).
		Update("result_path", "").Error
}

func ListStaleImageTasks(startedBefore int64) ([]ImageTask, error) {
	var tasks []ImageTask
	err := DB.Where("status = ? AND started_at > 0 AND started_at <= ?",
		ImageTaskStatusProcessing, startedBefore).Find(&tasks).Error
	return tasks, err
}

func ExpireQueuedImageTasks(createdBefore int64) ([]ImageTask, error) {
	var expired []ImageTask
	err := DB.Transaction(func(tx *gorm.DB) error {
		var candidates []ImageTask
		if err := tx.Where("status = ? AND created_at <= ?",
			ImageTaskStatusQueued, createdBefore).Order("id ASC").Find(&candidates).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, task := range candidates {
			result := tx.Model(&ImageTask{}).
				Where("id = ? AND status = ?", task.ID, ImageTaskStatusQueued).
				Updates(map[string]any{
					"status":        ImageTaskStatusFailed,
					"error_message": "The image task expired while waiting in the queue and was not submitted for generation.",
					"finished_at":   now,
					"updated_at":    now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				task.Status = ImageTaskStatusFailed
				task.ErrorMessage = "The image task expired while waiting in the queue and was not submitted for generation."
				task.FinishedAt = now
				task.UpdatedAt = now
				expired = append(expired, task)
			}
		}
		return nil
	})
	return expired, err
}

func ListTerminalImageTaskRequestFiles() ([]ImageTask, error) {
	var tasks []ImageTask
	err := DB.Where("status IN ? AND request_path <> ''",
		[]ImageTaskStatus{ImageTaskStatusSucceeded, ImageTaskStatusFailed}).Find(&tasks).Error
	return tasks, err
}

func ClearImageTaskRequestPath(taskID, requestPath string) error {
	return DB.Model(&ImageTask{}).
		Where("task_id = ? AND request_path = ?", taskID, requestPath).
		Update("request_path", "").Error
}

func NewImageTaskRequestID() string {
	return common.NewRequestId()
}
