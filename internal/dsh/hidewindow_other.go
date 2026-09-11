//go:build !windows

package dsh

import "os/exec"

// hideWindow 非 Windows 平台无控制台窗口问题，no-op。
func hideWindow(cmd *exec.Cmd) {}
