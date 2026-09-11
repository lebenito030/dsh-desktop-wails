package dsh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// readyLine 匹配 dsh web 打印的就绪行；URL 含登录 token，必须整体采用。
var readyLine = regexp.MustCompile(`^dsh web: (https?://\S+)`)

const (
	defaultReadyTimeout = 45 * time.Second
	defaultStopTimeout  = 8 * time.Second
)

// Status 是壳与前端共用的运行时状态。
type Status string

const (
	StatusStopped    Status = "stopped"
	StatusStarting   Status = "starting"
	StatusReady      Status = "ready"
	StatusRestarting Status = "restarting"
	StatusUpdating   Status = "updating"
	StatusError      Status = "error"
)

// Events 是监督器向上（app 层）回调的接口。全部在独立 goroutine 调用，
// 实现方自行保证线程安全。
type Events interface {
	OnStatus(s Status, detail string)
	OnURL(url string)
	OnLog(line string)
}

// Supervisor 托管一个 DSH web 进程及其 Job Object（全树回收）。
// 所有公开方法可从任意 goroutine 调用；Start/Stop/Restart 之间由
// opMu 串行化，后到的 Restart 等先到的一整轮做完。
type Supervisor struct {
	cfg SupervisorConfig
	ev  Events

	opMu sync.Mutex // 串行化 Start/Stop/Restart 整轮操作

	mu      sync.Mutex // 保护以下字段
	started bool
	url     string
	lastErr string

	job      *jobObject
	waitDone chan error // 一次 Start 对应一个 cmd.Wait 结果通道
}

// SupervisorConfig 描述如何启动 DSH。
type SupervisorConfig struct {
	NodeExe string // node.exe
	DshBin  string // dsh CLI bin.js
	// Args 是 web 子命令附加参数（含 --no-open --port 0）。
	Args []string
	// Env 是额外环境变量（如 DSH_HOME）；nil 表示不额外设置。
	Env map[string]string
	// Cwd 是子进程工作目录；空串用当前目录。
	Cwd string
}

// NewSupervisor 创建监督器。同一监督器同时至多托管一个 DSH 进程。
func NewSupervisor(cfg SupervisorConfig, ev Events) *Supervisor {
	return &Supervisor{cfg: cfg, ev: ev}
}

// URL 返回当前就绪 URL（含 token）；未就绪为空串。
func (s *Supervisor) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// StatusDetail 返回当前状态与附注（错误信息等）。
func (s *Supervisor) StatusDetail() (Status, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.started && s.lastErr != "":
		return StatusError, s.lastErr
	case !s.started:
		return StatusStopped, ""
	case s.url != "":
		return StatusReady, s.url
	default:
		return StatusStarting, ""
	}
}

// Start 启动 DSH 并阻塞到就绪（stdout 出现就绪行）。已在运行时返回错误。
// 就绪前进程退出或超时都视为失败，且进程已被回收。
func (s *Supervisor) Start(ctx context.Context) (string, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked(ctx)
}

func (s *Supervisor) startLocked(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.started {
		url := s.url
		s.mu.Unlock()
		return url, errors.New("DSH 已在运行")
	}
	s.mu.Unlock()

	s.ev.OnStatus(StatusStarting, "")

	job, err := newJobObject()
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		s.ev.OnStatus(StatusError, err.Error())
		return "", err
	}

	cmd := exec.Command(s.cfg.NodeExe, append([]string{s.cfg.DshBin, "web"}, s.cfg.Args...)...)
	if s.cfg.Cwd != "" {
		cmd.Dir = s.cfg.Cwd
	}
	cmd.Env = append(os.Environ(), envPairs(s.cfg.Env)...)
	hideWindow(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		job.close()
		s.failStart(err)
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		job.close()
		s.failStart(err)
		return "", err
	}
	if err := cmd.Start(); err != nil {
		job.close()
		err = fmt.Errorf("启动 DSH 失败: %w", err)
		s.failStart(err)
		return "", err
	}
	if err := job.assign(uint32(cmd.Process.Pid)); err != nil {
		// 挂 Job 失败不致命（罕见竞态），记录到日志即可。
		s.ev.OnLog("job assign: " + err.Error())
	}

	s.mu.Lock()
	s.started = true
	s.url = ""
	s.lastErr = ""
	s.job = job
	s.mu.Unlock()

	ready := make(chan string, 1)
	go scanReady(stdout, s.ev, ready)
	go drain(stderr, s.ev)

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	timeout := time.NewTimer(defaultReadyTimeout)
	defer timeout.Stop()

	select {
	case url := <-ready:
		s.mu.Lock()
		s.url = url
		s.waitDone = waitDone
		s.mu.Unlock()
		s.ev.OnURL(url)
		s.ev.OnStatus(StatusReady, url)
		// 就绪后监控退出：意外退出才通知，正常 Stop 由 stopLocked 处理。
		go func() {
			err := <-waitDone
			s.mu.Lock()
			if !s.started { // stopLocked 已把它标记为非运行，属正常退出
				s.mu.Unlock()
				return
			}
			detail := "DSH 进程已退出"
			if err != nil {
				detail = fmt.Sprintf("DSH 进程意外退出: %v", err)
			}
			s.started = false
			s.url = ""
			s.lastErr = detail
			job := s.job
			s.job = nil
			if job != nil {
				job.close()
			}
			s.mu.Unlock()
			s.ev.OnStatus(StatusStopped, detail)
		}()
		return url, nil
	case err := <-waitDone:
		// 进程在就绪前退出。
		detail := fmt.Sprintf("DSH 在就绪前退出: %v", err)
		s.setStopped(detail, job)
		s.ev.OnStatus(StatusError, detail)
		return "", errors.New(detail)
	case <-timeout.C:
		detail := "DSH 未在 45 秒内就绪"
		job.terminate()
		<-waitDone
		s.setStopped(detail, job)
		s.ev.OnStatus(StatusError, detail)
		return "", errors.New(detail)
	case <-ctx.Done():
		detail := "DSH 启动被取消"
		job.terminate()
		<-waitDone
		s.setStopped(detail, job)
		s.ev.OnStatus(StatusError, detail)
		return "", ctx.Err()
	}
}

// failStart 记录启动失败并上报。
func (s *Supervisor) failStart(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
	s.ev.OnStatus(StatusError, err.Error())
}

// setStopped 清理运行态并关闭 Job 句柄。
func (s *Supervisor) setStopped(detail string, job *jobObject) {
	s.mu.Lock()
	s.started = false
	s.url = ""
	s.lastErr = detail
	s.job = nil
	s.waitDone = nil
	s.mu.Unlock()
	if job != nil {
		job.close()
	}
}

// Stop 终止 DSH（Job Object 全树）并等待退出；未运行时是 no-op。
// status 是停止后上报的状态（正常关闭用 StatusStopped，更新用 StatusUpdating）。
func (s *Supervisor) Stop(ctx context.Context, status Status) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.stopLocked(ctx, status)
}

func (s *Supervisor) stopLocked(ctx context.Context, status Status) error {
	s.mu.Lock()
	job := s.job
	waitDone := s.waitDone
	s.mu.Unlock()
	if job == nil && waitDone == nil {
		return nil
	}

	if job != nil {
		_ = job.terminate() // 杀整棵进程树
	}
	waitExit := func() {
		if waitDone != nil {
			select {
			case <-waitDone:
			case <-time.After(defaultStopTimeout):
			case <-ctx.Done():
			}
		}
	}
	waitExit()

	s.mu.Lock()
	s.started = false
	s.url = ""
	if status != StatusStopped {
		s.lastErr = ""
	}
	s.job = nil
	s.waitDone = nil
	s.mu.Unlock()
	if job != nil {
		job.close() // close 前 Job 已 terminate，进程树已回收
	}
	s.ev.OnStatus(status, "")
	return nil
}

// Restart = Stop + Start，整轮持 opMu：并发调用合并成一轮完整重启。
func (s *Supervisor) Restart(ctx context.Context, stopStatus Status) (string, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.ev.OnStatus(StatusRestarting, "")
	if err := s.stopLocked(ctx, stopStatus); err != nil {
		return "", err
	}
	return s.startLocked(ctx)
}

// scanReady 逐行扫 stdout，发现就绪行时向 ready 发送 URL（仅一次）。
func scanReady(r io.Reader, ev Events, ready chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if ev != nil {
			ev.OnLog(line)
		}
		if m := readyLine.FindStringSubmatch(line); m != nil {
			select {
			case ready <- m[1]:
			default:
			}
		}
	}
}

// drain 把 stderr 全部转发到日志。
func drain(r io.Reader, ev Events) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if ev != nil {
			ev.OnLog(sc.Text())
		}
	}
}

func envPairs(m map[string]string) []string {
	pairs := make([]string, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}
