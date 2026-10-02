package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// 冻结且在途操作结束后调用。先核实整个固定存储根的归属，再只删目标账号目录。
func deleteNewmaxImageTaskFiles(ids []int) error {
	ownedTokens := make(map[int]bool, len(ids))
	for _, id := range ids {
		ownedTokens[id] = true
	}
	var tasks []model.ImageTask
	if err := model.DB.Select("task_id", "token_id").Find(&tasks).Error; err != nil {
		return err
	}
	var directories []model.NewmaxImageTaskDirectory
	if err := model.DB.Find(&directories).Error; err != nil {
		return err
	}
	known := make(map[string]int, len(tasks)+len(directories))
	for _, task := range tasks {
		known[task.TaskID] = task.TokenID
	}
	for _, directory := range directories {
		if token, exists := known[directory.TaskID]; exists && token != directory.TokenID {
			return errors.New("image_directory_ownership_conflict")
		}
		known[directory.TaskID] = directory.TokenID
	}
	entries, err := os.ReadDir(imageTaskStorageDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if _, exists := known[entry.Name()]; !exists || !entry.IsDir() {
			return errors.New("legacy_image_directory_ownership_required")
		}
	}
	for id, token := range known {
		if !ownedTokens[token] {
			continue
		}
		if !newmaxTaskDirectoryPattern.MatchString(id) || strings.Contains(id, "..") {
			return errors.New("file_target_invalid")
		}
		if err := os.RemoveAll(filepath.Join(imageTaskStorageDir(), id)); err != nil {
			return err
		}
	}
	return nil
}
