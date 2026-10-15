//go:build windows

package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// 稳定入口可能被正在退出的旧版托盘或 Windows 文件扫描短暂占用。
// Installer Engine 只重试明确的共享冲突，不放宽权限错误，也不替换其他进程的文件。
func copyWindowsStableFile(source, target string, mode os.FileMode) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	// Shim 的内容没有变化时不必覆盖正在使用的可执行文件。
	if installed, err := os.ReadFile(target); err == nil && bytes.Equal(installed, content) {
		return nil
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		err := os.WriteFile(target, content, mode)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
			!errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 Windows 释放 AgentDock 稳定入口 %s 超时: %w", target, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
