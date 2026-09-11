//go:build !windows

// job_unix.go —— 类 Unix 平台用「进程组」代替 Windows 的 Job Object。
//
// 子进程以自己的进程组启动（见 configureProcessTree），因此杀组即杀整棵树，
// 效果等价于 Windows 的 kill-on-close。
//
// **平台能力差异要说清楚**：Windows 的 Job Object 带
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE，壳**被强杀**时由内核自动回收整棵树；
// 进程组只在壳主动 terminate 时生效——壳被 SIGKILL 掉的话，DSH 及其子树会被
// 重新挂到 init 上成为孤儿。这是平台能力差异，不是遗漏。
//
// Linux 上的 Pdeathsig 看似能补上这一段，但它按「父**线程**」判定，而 Go 调度器
// 会在运行时销毁线程——用它有可能在壳还活着的时候把 DSH 误杀，所以这里不用。
package dsh

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
)

// jobObject 持有一个进程组。名字沿用 Windows 侧，语义是「进程树容器」。
type jobObject struct {
	mu   sync.Mutex
	pgid int // 0 表示尚未挂载进程
	seen map[int]struct{}
}

func newJobObject() (*jobObject, error) {
	return &jobObject{seen: make(map[int]struct{})}, nil
}

// configureProcessTree 让子进程成为新进程组的组长（pgid == pid）。
// 没有它，kill(-pgid) 会打到壳自己所在的进程组，把壳一起杀掉。
func configureProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// assign 记录进程组 ID。Setpgid 已保证 pgid == pid，重复挂同一 PID 无害。
func (j *jobObject) assign(pid uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.seen[int(pid)]; ok {
		return nil
	}
	if j.pgid == 0 {
		j.pgid = int(pid)
	}
	j.seen[int(pid)] = struct{}{}
	return nil
}

// terminate 杀掉整个进程组（pgid 取负即「按组」语义）。
func (j *jobObject) terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.pgid == 0 {
		return nil
	}
	err := syscall.Kill(-j.pgid, syscall.SIGKILL)
	j.pgid = 0
	// ESRCH = 组里已经没有活进程，属正常情况（比如 DSH 已自行退出）。
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// close 在 Unix 上没有需要释放的内核句柄：回收已由 terminate 完成，
// 进程自然退出时也没有句柄泄漏。留着是为了与 Windows 实现保持同一套调用序列。
func (j *jobObject) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pgid = 0
	return nil
}
