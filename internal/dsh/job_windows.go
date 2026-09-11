//go:build windows

// job_windows.go 用 Windows Job Object（kill-on-close）托管 DSH 进程树：
// 壳崩溃/被杀时操作系统自动回收全部子进程，避免孤儿 node.exe。
package dsh

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type jobObject struct {
	mu   sync.Mutex
	job  windows.Handle
	seen map[uint32]struct{} // 已挂载的 PID，防重复 Assign
}

func newJobObject() (*jobObject, error) {
	li := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	li.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	if _, err = windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&li)), uint32(unsafe.Sizeof(li))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return &jobObject{job: job, seen: make(map[uint32]struct{})}, nil
}

// configureProcessTree 在 Windows 上无事可做：进程树由 Job Object 托管
// （子进程创建后 assign 进 Job 即可），不需要给子进程加任何 SysProcAttr。
// 它存在的意义是让 supervisor 能写一句平台无关的调用，见 job_unix.go 的对应实现。
func configureProcessTree(cmd *exec.Cmd) {}

// assign 把进程挂进 Job。重复挂同一 PID 是无害的，这里仍去重以省一次系统调用。
func (j *jobObject) assign(pid uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.seen[pid]; ok {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(j.job, h); err != nil {
		// 进程可能已退出（ERROR_ACCESS_DENIED/ERROR_NOT_FOUND），不视为致命。
		return fmt.Errorf("AssignProcessToJobObject(%d): %w", pid, err)
	}
	j.seen[pid] = struct{}{}
	return nil
}

// terminate 杀掉 Job 内全部进程。句柄关闭（进程退出）由 close 负责。
func (j *jobObject) terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.job, 1)
}

func (j *jobObject) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job == 0 {
		return nil
	}
	err := windows.CloseHandle(j.job)
	j.job = 0
	return err
}
