package model

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// 只在同一次内核启动、同一 PID 命名空间内判断进程消失；不凭年龄或心跳猜测。
// 读取失败、权限不足、其他机器/容器、旧版无归属及 PID 重用一律保持 pending。
func newmaxProcessScope() string {
	switch runtime.GOOS {
	case "linux":
		boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return ""
		}
		namespace, err := os.Readlink("/proc/self/ns/pid")
		if err != nil {
			return ""
		}
		return "linux:" + strings.TrimSpace(string(boot)) + ":" + namespace
	case "darwin":
		boot, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.bootsessionuuid").Output()
		if err != nil {
			return ""
		}
		return "darwin:" + strings.TrimSpace(string(boot))
	default:
		return ""
	}
}

func newmaxOperationOwnerStopped(operation NewmaxAccountOperation, scope string) bool {
	if scope == "" || operation.OwnerScope != scope || operation.OwnerPID <= 0 {
		return false
	}
	process, err := os.FindProcess(operation.OwnerPID)
	if err != nil {
		return false
	}
	defer process.Release()
	err = process.Signal(syscall.Signal(0))
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
