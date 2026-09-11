//go:build windows

package bootstrap

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideChildWindow 阻止 GUI 父进程派生控制台子进程时弹出终端窗口。
func hideChildWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
