package controller

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestDisabledImageTaskWorkerDoesNotInitializeStorageOrAccessDatabase(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })
	t.Setenv("IMAGE_TASK_WORKER_ENABLED", "false")
	storage := filepath.Join(t.TempDir(), "must-not-be-created")
	t.Setenv("IMAGE_TASK_STORAGE_DIR", storage)

	require.NotPanics(t, func() {
		StartImageTaskWorker("http://127.0.0.1:3000/v1")
	}, "禁用节点不得启动领取、过期隔离或文件清理，甚至不能访问共享数据库")
	require.NoDirExists(t, storage)
}
