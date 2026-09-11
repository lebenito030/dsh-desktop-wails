//go:build !windows

package bootstrap

import "os/exec"

// hideChildWindow 非 Windows 平台无控制台窗口问题，no-op。
func hideChildWindow(cmd *exec.Cmd) {}
