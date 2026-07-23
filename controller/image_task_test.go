package controller

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type imageTaskRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn imageTaskRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestProcessImageTaskPersistsResultAndCorrelationID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-worker?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}, &model.Token{}))
	require.NoError(t, db.Create(&model.Token{
		Id: 9, UserId: 42, Key: "shadow-token",
	}).Error)

	root := t.TempDir()
	requestPath := filepath.Join(root, "request.bin")
	require.NoError(t, os.WriteFile(requestPath, []byte(`{"model":"gpt-image-2"}`), 0o600))
	task, _, err := model.CreateOrGetImageTask(&model.ImageTask{
		RequestID:   "img_test_request_1234",
		UserID:      42,
		TokenID:     9,
		Endpoint:    "/images/generations",
		ContentType: "application/json",
		RequestPath: requestPath,
	})
	require.NoError(t, err)
	claimed, exists, err := model.ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, task.TaskID, claimed.TaskID)

	var receivedRequestID string
	client := &http.Client{Transport: imageTaskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		receivedRequestID = request.Header.Get(common.RequestIdKey)
		require.Equal(t, "Bearer sk-shadow-token", request.Header.Get("Authorization"))
		require.Equal(t, "1", request.Header.Get("X-NewMax-Internal-Image-Task"))
		body, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)
		require.JSONEq(t, `{"model":"gpt-image-2"}`, string(body))
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				common.RequestIdKey: []string{receivedRequestID},
				"Content-Type":      []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`)),
		}, nil
	})}

	processImageTask(context.Background(), "http://internal.test", claimed, client)

	completed, exists, err := model.GetImageTaskByTaskID(9, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, model.ImageTaskStatusSucceeded, completed.Status)
	require.Equal(t, "img_test_request_1234", completed.BillingRequestID)
	require.Equal(t, "img_test_request_1234", receivedRequestID)
	result, err := os.ReadFile(completed.ResultPath)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":[{"b64_json":"aW1hZ2U="}]}`, string(result))
	require.NoFileExists(t, requestPath)
}

func TestProcessImageTaskRemovesStoredRequestAfterAmbiguousFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-worker-failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}, &model.Token{}))
	require.NoError(t, db.Create(&model.Token{
		Id: 10, UserId: 42, Key: "shadow-token",
	}).Error)

	taskDir := t.TempDir()
	requestPath := filepath.Join(taskDir, "request.bin")
	require.NoError(t, os.WriteFile(requestPath, []byte(`{"model":"gpt-image-2"}`), 0o600))
	task, _, err := model.CreateOrGetImageTask(&model.ImageTask{
		RequestID:   "img_failed_request_1234",
		UserID:      42,
		TokenID:     10,
		Endpoint:    "/images/generations",
		ContentType: "application/json",
		RequestPath: requestPath,
	})
	require.NoError(t, err)
	claimed, exists, err := model.ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)

	client := &http.Client{Transport: imageTaskRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset after upstream accepted request")
	})}
	processImageTask(context.Background(), "http://internal.test", claimed, client)

	failed, exists, err := model.GetImageTaskByTaskID(10, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, model.ImageTaskStatusFailed, failed.Status)
	require.Contains(t, failed.ErrorMessage, "not retried")
	require.NoFileExists(t, requestPath)
}

func TestCleanupExpiredImageTaskResultRemovesFileBeforeClearingDatabasePath(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-cleanup?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}))

	taskDir := filepath.Join(t.TempDir(), "task")
	require.NoError(t, os.MkdirAll(taskDir, 0o700))
	resultPath := filepath.Join(taskDir, "result.json")
	require.NoError(t, os.WriteFile(resultPath, []byte(`{"data":[]}`), 0o600))
	task, _, err := model.CreateOrGetImageTask(&model.ImageTask{
		RequestID: "img_expired_result_1234",
		UserID:    42,
		TokenID:   11,
	})
	require.NoError(t, err)
	claimed, exists, err := model.ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, model.CompleteImageTask(claimed.TaskID, resultPath, task.RequestID, 100))

	cleanupExpiredImageTaskResultsAt(time.Unix(101, 0))

	require.NoFileExists(t, resultPath)
	require.NoDirExists(t, taskDir)
	reloaded, exists, err := model.GetImageTaskByTaskID(11, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Empty(t, reloaded.ResultPath)
}

func TestCleanupExpiredImageTaskResultKeepsDatabasePathWhenFileRemovalFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-cleanup-failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}))

	resultPath := filepath.Join(t.TempDir(), "result-directory")
	require.NoError(t, os.MkdirAll(resultPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(resultPath, "still-present"), []byte("x"), 0o600))
	task, _, err := model.CreateOrGetImageTask(&model.ImageTask{
		RequestID: "img_cleanup_retry_1234",
		UserID:    42,
		TokenID:   12,
	})
	require.NoError(t, err)
	claimed, exists, err := model.ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, model.CompleteImageTask(claimed.TaskID, resultPath, task.RequestID, 100))

	cleanupExpiredImageTaskResultsAt(time.Unix(101, 0))

	reloaded, exists, err := model.GetImageTaskByTaskID(12, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, resultPath, reloaded.ResultPath)
}

func TestCleanupTerminalImageTaskRequestKeepsDatabasePathUntilRemovalSucceeds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-request-cleanup-retry?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}))

	requestPath := filepath.Join(t.TempDir(), "request-path")
	require.NoError(t, os.MkdirAll(requestPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(requestPath, "still-present"), []byte("x"), 0o600))
	task, _, err := model.CreateOrGetImageTask(&model.ImageTask{
		RequestID:   "img_request_cleanup_retry_1234",
		UserID:      42,
		TokenID:     13,
		RequestPath: requestPath,
	})
	require.NoError(t, err)
	claimed, exists, err := model.ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, model.FailImageTask(claimed.TaskID, "failed", task.RequestID))

	cleanupImageTaskRequestFiles()

	reloaded, exists, err := model.GetImageTaskByTaskID(13, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, requestPath, reloaded.RequestPath)

	require.NoError(t, os.Remove(filepath.Join(requestPath, "still-present")))
	require.NoError(t, os.Remove(requestPath))
	cleanupImageTaskRequestFiles()
	reloaded, exists, err = model.GetImageTaskByTaskID(13, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Empty(t, reloaded.RequestPath)
}

func TestSubmitImageTaskReturnsSameTaskForDuplicateRequestID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:image-task-submit?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.ImageTask{}))
	t.Setenv("IMAGE_TASK_STORAGE_DIR", t.TempDir())

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("token_id", 9)
		c.Set(common.RequestIdKey, "img_submit_12345678")
		c.Next()
		common.CleanupBodyStorage(c)
	})
	engine.POST("/v1/images/generations/async", SubmitImageTask)

	submit := func() (int, map[string]any) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/images/generations/async",
			bytes.NewBufferString(`{"model":"gpt-image-2"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "img_submit_12345678")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		var payload map[string]any
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		return response.Code, payload
	}

	firstStatus, first := submit()
	secondStatus, second := submit()

	require.Equal(t, http.StatusAccepted, firstStatus)
	require.Equal(t, http.StatusOK, secondStatus)
	require.Equal(t, first["task_id"], second["task_id"])
	task, exists, err := model.GetImageTaskByRequestID(9, "img_submit_12345678")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "/images/generations", task.Endpoint)
	var count int64
	require.NoError(t, db.Model(&model.ImageTask{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
