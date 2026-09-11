//go:build windows

package dsh

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow 阻止 GUI 子系统父进程派生控制台子进程时弹出新终端窗口：
// 不加此标志，Windows 会为 node.exe 分配一个新控制台（表现为闪现的黑窗）。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
