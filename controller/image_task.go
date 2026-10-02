package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const (
	defaultImageTaskResultTTLSeconds = 24 * 60 * 60
	defaultImageTaskQueueTTLSeconds  = 24 * 60 * 60
	defaultImageTaskWorkerCount      = 4
	imageTaskExecutionTimeout        = 10 * time.Minute
	imageTaskStaleGrace              = time.Minute
)

var (
	imageTaskRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)
	imageTaskWorkerOnce       sync.Once
	imageTaskHTTPClient       = &http.Client{Timeout: imageTaskExecutionTimeout}
)

func imageTaskStorageDir() string {
	return common.GetEnvOrDefaultString(
		"IMAGE_TASK_STORAGE_DIR",
		filepath.Join(os.TempDir(), "new-api-image-tasks"),
	)
}

func imageTaskResultTTL() time.Duration {
	seconds := common.GetEnvOrDefault("IMAGE_TASK_RESULT_TTL_SECONDS", defaultImageTaskResultTTLSeconds)
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func imageTaskQueueTTL() time.Duration {
	seconds := common.GetEnvOrDefault("IMAGE_TASK_QUEUE_TTL_SECONDS", defaultImageTaskQueueTTLSeconds)
	if seconds < 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func imageTaskError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "image_task_error",
		},
	})
}

func imageTaskRequestID(c *gin.Context) (string, error) {
	requestID := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.GetHeader(common.RequestIdKey))
	}
	if requestID == "" {
		requestID = c.GetString(common.RequestIdKey)
	}
	if requestID == "" {
		requestID = model.NewImageTaskRequestID()
	}
	if !imageTaskRequestIDPattern.MatchString(requestID) {
		return "", fmt.Errorf("request_id must be 8-64 characters containing only letters, numbers, '.', '_', ':' or '-'")
	}
	return requestID, nil
}

func SubmitImageTask(c *gin.Context) {
	release, guardErr := model.BeginNewmaxTokenOperation(c.GetInt("token_id"))
	if guardErr != nil {
		imageTaskError(c, http.StatusUnauthorized, "Account unavailable")
		return
	}
	defer release()
	requestID, err := imageTaskRequestID(c)
	if err != nil {
		imageTaskError(c, http.StatusBadRequest, err.Error())
		return
	}
	userID := c.GetInt("id")
	tokenID := c.GetInt("token_id")
	if existing, exists, queryErr := model.GetImageTaskByRequestID(tokenID, requestID); queryErr != nil {
		imageTaskError(c, http.StatusInternalServerError, "Failed to query image task")
		return
	} else if exists {
		writeImageTaskResponse(c, existing, http.StatusOK)
		return
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		imageTaskError(c, http.StatusBadRequest, fmt.Sprintf("Failed to read image request: %v", err))
		return
	}
	if _, err = storage.Seek(0, io.SeekStart); err != nil {
		imageTaskError(c, http.StatusInternalServerError, "Failed to rewind image request")
		return
	}

	taskID := model.GenerateTaskID()
	if err = model.RegisterNewmaxImageTaskDirectory(taskID, tokenID); err != nil {
		imageTaskError(c, http.StatusServiceUnavailable, "Failed to persist image task ownership")
		return
	}
	taskDir := filepath.Join(imageTaskStorageDir(), taskID)
	if err = os.MkdirAll(taskDir, 0o700); err != nil {
		imageTaskError(c, http.StatusInternalServerError, "Failed to initialize image task storage")
		return
	}
	requestPath := filepath.Join(taskDir, "request.bin")
	requestFile, err := os.OpenFile(requestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(taskDir)
		imageTaskError(c, http.StatusInternalServerError, "Failed to persist image request")
		return
	}
	_, copyErr := io.Copy(requestFile, storage)
	closeErr := requestFile.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(taskDir)
		imageTaskError(c, http.StatusInternalServerError, "Failed to persist image request")
		return
	}

	endpoint := strings.TrimSuffix(c.FullPath(), "/async")
	endpoint = strings.TrimPrefix(endpoint, "/v1")
	task, created, err := model.CreateOrGetImageTask(&model.ImageTask{
		TaskID:      taskID,
		RequestID:   requestID,
		UserID:      userID,
		TokenID:     tokenID,
		Endpoint:    endpoint,
		ContentType: c.GetHeader("Content-Type"),
		RequestPath: requestPath,
	})
	if err != nil {
		_ = os.RemoveAll(taskDir)
		imageTaskError(c, http.StatusInternalServerError, "Failed to create image task")
		return
	}
	if !created {
		_ = os.RemoveAll(taskDir)
		writeImageTaskResponse(c, task, http.StatusOK)
		return
	}
	writeImageTaskResponse(c, task, http.StatusAccepted)
}

func GetImageTask(c *gin.Context) {
	task, exists, err := model.GetImageTaskByTaskID(c.GetInt("token_id"), c.Param("task_id"))
	if err != nil {
		imageTaskError(c, http.StatusInternalServerError, "Failed to query image task")
		return
	}
	if !exists {
		imageTaskError(c, http.StatusNotFound, "Image task not found")
		return
	}
	writeImageTaskResponse(c, task, http.StatusOK)
}

func GetImageTaskByRequestID(c *gin.Context) {
	task, exists, err := model.GetImageTaskByRequestID(c.GetInt("token_id"), c.Param("request_id"))
	if err != nil {
		imageTaskError(c, http.StatusInternalServerError, "Failed to query image task")
		return
	}
	if !exists {
		imageTaskError(c, http.StatusNotFound, "Image task not found")
		return
	}
	writeImageTaskResponse(c, task, http.StatusOK)
}

func writeImageTaskResponse(c *gin.Context, task *model.ImageTask, status int) {
	response := map[string]any{
		"task_id":    task.TaskID,
		"request_id": task.RequestID,
		"status":     task.Status,
		"created_at": task.CreatedAt,
	}
	if task.BillingRequestID != "" {
		response["billing_request_id"] = task.BillingRequestID
	}
	if task.Status == model.ImageTaskStatusFailed {
		response["error"] = map[string]any{"message": task.ErrorMessage, "type": "image_task_failed"}
	}
	if task.Status == model.ImageTaskStatusSucceeded {
		if task.ExpiresAt > 0 && task.ExpiresAt <= time.Now().Unix() {
			response["status"] = "expired"
			response["error"] = map[string]any{
				"message": "The temporarily stored image result has expired.",
				"type":    "image_task_result_expired",
			}
			c.JSON(http.StatusGone, response)
			return
		}
		resultBytes, err := os.ReadFile(task.ResultPath)
		if err != nil {
			imageTaskError(c, http.StatusInternalServerError,
				"Image task succeeded but its stored result is unavailable")
			return
		}
		var result map[string]any
		if err := common.Unmarshal(resultBytes, &result); err != nil {
			imageTaskError(c, http.StatusInternalServerError, "Stored image result is invalid")
			return
		}
		for key, value := range result {
			response[key] = value
		}
		response["task_id"] = task.TaskID
		response["request_id"] = task.RequestID
		response["status"] = model.ImageTaskStatusSucceeded
		response["billing_request_id"] = task.BillingRequestID
		response["expires_at"] = task.ExpiresAt
	}
	c.JSON(status, response)
}

func StartImageTaskWorker(internalBaseURL string) {
	imageTaskWorkerOnce.Do(func() {
		if err := os.MkdirAll(imageTaskStorageDir(), 0o700); err != nil {
			common.SysError(fmt.Sprintf("failed to initialize image task storage: %v", err))
			return
		}
		quarantineStaleImageTasks()
		cleanupImageTaskRequestFiles()
		workerCount := common.GetEnvOrDefault("IMAGE_TASK_WORKERS", defaultImageTaskWorkerCount)
		if workerCount < 1 {
			workerCount = 1
		}
		internalBaseURL = strings.TrimSuffix(internalBaseURL, "/")
		for worker := 0; worker < workerCount; worker++ {
			go runImageTaskWorker(internalBaseURL)
		}
		go cleanupExpiredImageTaskResults()
		go monitorStaleImageTasks()
		common.SysLog(fmt.Sprintf("image task worker started with %d workers", workerCount))
	})
}

func quarantineStaleImageTasks() {
	startedBefore := time.Now().Add(-imageTaskExecutionTimeout - imageTaskStaleGrace).Unix()
	tasks, err := model.ListStaleImageTasks(startedBefore)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to list stale image tasks: %v", err))
		return
	}
	if err := model.FailStaleImageTasks(startedBefore); err != nil {
		common.SysError(fmt.Sprintf("failed to quarantine stale image tasks: %v", err))
		return
	}
	for _, task := range tasks {
		cleanupImageTaskRequest(task.TaskID, task.RequestPath)
	}
	expiredQueued, err := model.ExpireQueuedImageTasks(time.Now().Add(-imageTaskQueueTTL()).Unix())
	if err != nil {
		common.SysError(fmt.Sprintf("failed to expire queued image tasks: %v", err))
		return
	}
	for _, task := range expiredQueued {
		cleanupImageTaskRequest(task.TaskID, task.RequestPath)
	}
}

func monitorStaleImageTasks() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		quarantineStaleImageTasks()
		cleanupImageTaskRequestFiles()
	}
}

func runImageTaskWorker(internalBaseURL string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		task, exists, err := model.ClaimNextQueuedImageTask()
		if err != nil {
			common.SysError(fmt.Sprintf("failed to claim image task: %v", err))
			continue
		}
		if !exists {
			continue
		}
		processImageTask(context.Background(), internalBaseURL, task, imageTaskHTTPClient)
	}
}

func processImageTask(
	ctx context.Context,
	internalBaseURL string,
	task *model.ImageTask,
	client *http.Client,
) {
	release, guardErr := model.BeginNewmaxTokenOperation(task.TokenID)
	if guardErr != nil {
		return
	}
	defer release()
	defer cleanupImageTaskRequest(task.TaskID, task.RequestPath)
	requestFile, err := os.Open(task.RequestPath)
	if err != nil {
		_ = model.FailImageTask(task.TaskID, "Stored image request is unavailable", "")
		return
	}
	defer requestFile.Close()

	token, err := model.GetTokenById(task.TokenID)
	if err != nil {
		_ = model.FailImageTask(task.TaskID, "Image task token is unavailable", "")
		return
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		internalBaseURL+task.Endpoint,
		requestFile,
	)
	if err != nil {
		_ = model.FailImageTask(task.TaskID, "Failed to build internal image request", "")
		return
	}
	request.Header.Set("Authorization", "Bearer sk-"+token.GetFullKey())
	request.Header.Set("Content-Type", task.ContentType)
	request.Header.Set(common.RequestIdKey, task.RequestID)
	request.Header.Set("Idempotency-Key", task.RequestID)
	request.Header.Set("X-NewMax-Internal-Image-Task", "1")

	response, err := client.Do(request)
	if err != nil {
		_ = model.FailImageTask(
			task.TaskID,
			"Internal image request failed and was not retried to prevent duplicate billing: "+err.Error(),
			task.RequestID,
		)
		return
	}
	defer response.Body.Close()
	resultBytes, readErr := io.ReadAll(response.Body)
	billingRequestID := response.Header.Get(common.RequestIdKey)
	if billingRequestID == "" {
		billingRequestID = task.RequestID
	}
	if readErr != nil {
		_ = model.FailImageTask(
			task.TaskID,
			"Image response could not be stored and was not retried to prevent duplicate billing",
			billingRequestID,
		)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(resultBytes))
		if message == "" {
			message = response.Status
		}
		_ = model.FailImageTask(task.TaskID, message, billingRequestID)
		return
	}
	var parsed map[string]any
	if err := common.Unmarshal(resultBytes, &parsed); err != nil {
		_ = model.FailImageTask(
			task.TaskID,
			"Image response was not valid JSON and was not retried to prevent duplicate billing",
			billingRequestID,
		)
		return
	}
	taskDir := filepath.Dir(task.RequestPath)
	resultPath := filepath.Join(taskDir, "result.json")
	temporaryPath := resultPath + ".tmp"
	if err := os.WriteFile(temporaryPath, resultBytes, 0o600); err != nil {
		_ = model.FailImageTask(
			task.TaskID,
			"Image response could not be persisted and was not retried to prevent duplicate billing",
			billingRequestID,
		)
		return
	}
	if err := os.Rename(temporaryPath, resultPath); err != nil {
		_ = os.Remove(temporaryPath)
		_ = model.FailImageTask(
			task.TaskID,
			"Image response could not be finalized and was not retried to prevent duplicate billing",
			billingRequestID,
		)
		return
	}
	expiresAt := time.Now().Add(imageTaskResultTTL()).Unix()
	if err := model.CompleteImageTask(task.TaskID, resultPath, billingRequestID, expiresAt); err != nil {
		_ = os.Remove(resultPath)
		logger.LogError(ctx, fmt.Sprintf("failed to complete image task %s: %v", task.TaskID, err))
		return
	}
}

func cleanupImageTaskRequest(taskID, requestPath string) {
	if taskID == "" || requestPath == "" {
		return
	}
	if err := os.Remove(requestPath); err != nil && !os.IsNotExist(err) {
		common.SysError(fmt.Sprintf("failed to remove image task request %s: %v", taskID, err))
		return
	}
	if err := model.ClearImageTaskRequestPath(taskID, requestPath); err != nil {
		common.SysError(fmt.Sprintf("failed to clear image task request path %s: %v", taskID, err))
		return
	}
	_ = os.Remove(filepath.Dir(requestPath))
}

func cleanupImageTaskRequestFiles() {
	tasks, err := model.ListTerminalImageTaskRequestFiles()
	if err != nil {
		common.SysError(fmt.Sprintf("failed to list image task requests for cleanup: %v", err))
		return
	}
	for _, task := range tasks {
		cleanupImageTaskRequest(task.TaskID, task.RequestPath)
	}
}

func cleanupExpiredImageTaskResultsAt(now time.Time) {
	tasks, err := model.ListExpiredImageTaskFiles(now.Unix())
	if err != nil {
		common.SysError(fmt.Sprintf("failed to list expired image task results: %v", err))
		return
	}
	for _, task := range tasks {
		if task.ResultPath == "" {
			continue
		}
		if err := os.Remove(task.ResultPath); err != nil && !os.IsNotExist(err) {
			common.SysError(fmt.Sprintf("failed to remove expired image task result %s: %v", task.TaskID, err))
			continue
		}
		if err := model.ClearImageTaskResultPath(task.TaskID, task.ResultPath); err != nil {
			common.SysError(fmt.Sprintf("failed to clear expired image task result %s: %v", task.TaskID, err))
			continue
		}
		_ = os.Remove(filepath.Dir(task.ResultPath))
	}
}

func cleanupExpiredImageTaskResults() {
	cleanupExpiredImageTaskResultsAt(time.Now())
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for now := range ticker.C {
		cleanupExpiredImageTaskResultsAt(now)
	}
}
