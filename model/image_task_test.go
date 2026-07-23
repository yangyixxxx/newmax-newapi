package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateOrGetImageTaskIsIdempotentPerUserAndRequest(t *testing.T) {
	truncateTables(t)
	const workers = 8
	results := make(chan *ImageTask, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task, _, err := CreateOrGetImageTask(&ImageTask{
				RequestID:   "req-idempotent",
				UserID:      42,
				TokenID:     7,
				Endpoint:    "/images/generations",
				ContentType: "application/json",
				RequestPath: "/tmp/request.bin",
			})
			require.NoError(t, err)
			results <- task
		}()
	}
	wait.Wait()
	close(results)

	var taskID string
	for task := range results {
		if taskID == "" {
			taskID = task.TaskID
		}
		require.Equal(t, taskID, task.TaskID)
	}
	var count int64
	require.NoError(t, DB.Model(&ImageTask{}).
		Where("token_id = ? AND request_id = ?", 7, "req-idempotent").
		Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestImageTaskLookupIsScopedToToken(t *testing.T) {
	truncateTables(t)
	task, created, err := CreateOrGetImageTask(&ImageTask{
		RequestID: "req-private",
		UserID:    42,
		TokenID:   7,
	})
	require.NoError(t, err)
	require.True(t, created)

	_, exists, err := GetImageTaskByTaskID(99, task.TaskID)
	require.NoError(t, err)
	require.False(t, exists)
	_, exists, err = GetImageTaskByRequestID(99, task.RequestID)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestStaleImageTaskIsFailedInsteadOfRequeued(t *testing.T) {
	truncateTables(t)
	task, _, err := CreateOrGetImageTask(&ImageTask{
		RequestID: "req-interrupted",
		UserID:    42,
		TokenID:   7,
	})
	require.NoError(t, err)
	claimed, exists, err := ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, task.TaskID, claimed.TaskID)

	require.NoError(t, FailStaleImageTasks(claimed.StartedAt-1))
	stillProcessing, exists, err := GetImageTaskByTaskID(7, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, ImageTaskStatusProcessing, stillProcessing.Status)

	require.NoError(t, FailStaleImageTasks(claimed.StartedAt+1))
	reloaded, exists, err := GetImageTaskByTaskID(7, task.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, ImageTaskStatusFailed, reloaded.Status)
	require.Contains(t, reloaded.ErrorMessage, "not retried")

	_, exists, err = ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.False(t, exists)
}

func TestCompleteImageTaskReportsStateConflict(t *testing.T) {
	truncateTables(t)
	task, _, err := CreateOrGetImageTask(&ImageTask{
		RequestID: "req-complete-conflict",
		UserID:    42,
		TokenID:   7,
	})
	require.NoError(t, err)
	claimed, exists, err := ClaimNextQueuedImageTask()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, task.TaskID, claimed.TaskID)
	require.NoError(t, FailImageTask(task.TaskID, "already failed", task.RequestID))

	err = CompleteImageTask(task.TaskID, "/tmp/orphan-result.json", task.RequestID, 123)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrImageTaskStateConflict))
}

func TestExpireQueuedImageTasksUsesStatusCAS(t *testing.T) {
	truncateTables(t)
	expired, _, err := CreateOrGetImageTask(&ImageTask{
		RequestID:   "req-queued-expired",
		UserID:      42,
		TokenID:     7,
		RequestPath: "/tmp/expired-request.bin",
	})
	require.NoError(t, err)
	fresh, _, err := CreateOrGetImageTask(&ImageTask{
		RequestID:   "req-queued-fresh",
		UserID:      42,
		TokenID:     7,
		RequestPath: "/tmp/fresh-request.bin",
	})
	require.NoError(t, err)
	require.NoError(t, DB.Model(&ImageTask{}).Where("id = ?", expired.ID).Update("created_at", 100).Error)
	require.NoError(t, DB.Model(&ImageTask{}).Where("id = ?", fresh.ID).Update("created_at", 200).Error)

	tasks, err := ExpireQueuedImageTasks(150)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, expired.TaskID, tasks[0].TaskID)

	reloadedExpired, exists, err := GetImageTaskByTaskID(7, expired.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, ImageTaskStatusFailed, reloadedExpired.Status)
	reloadedFresh, exists, err := GetImageTaskByTaskID(7, fresh.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, ImageTaskStatusQueued, reloadedFresh.Status)
}
