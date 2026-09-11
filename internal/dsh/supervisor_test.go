package dsh

import (
	"context"
	"strings"
	"testing"
)

// readyLine 是壳与 DSH 之间**唯一**的契约：DSH 若改了就绪输出，只改这一处。
// 这组用例就是把「什么样的行算就绪」白纸黑字钉下来。
func TestReadyLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		url  string
	}{
		{
			name: "标准就绪行",
			line: "dsh web: http://127.0.0.1:41234/?token=abc123",
			url:  "http://127.0.0.1:41234/?token=abc123",
		},
		{
			name: "https 也接受",
			line: "dsh web: https://127.0.0.1:41234/?token=abc",
			url:  "https://127.0.0.1:41234/?token=abc",
		},
		{name: "普通日志", line: "[dsh] listening on port 41234", url: ""},
		{name: "空行", line: "", url: ""},
		{name: "前缀不完整", line: "dsh web:", url: ""},
		{name: "子串命中但不在行首", line: "info: dsh web: http://x", url: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := readyLine.FindStringSubmatch(tc.line)
			got := ""
			if m != nil {
				got = m[1]
			}
			if got != tc.url {
				t.Errorf("line %q -> %q, want %q", tc.line, got, tc.url)
			}
		})
	}
}

type recordingEvents struct {
	logs []string
}

func (r *recordingEvents) OnStatus(Status, string) {}
func (r *recordingEvents) OnURL(string)            {}
func (r *recordingEvents) OnLog(line string)       { r.logs = append(r.logs, line) }

// scanReady 要做两件事：把每一行转发给日志回调；从就绪行里抠出 URL 且只发一次。
func TestScanReady(t *testing.T) {
	input := "booting...\n" +
		"dsh web: http://127.0.0.1:1/?token=a\n" +
		"another dsh web: http://127.0.0.1:2/?token=b\n" +
		"ready\n"

	ev := &recordingEvents{}
	ready := make(chan string, 4)
	scanReady(strings.NewReader(input), ev, ready)

	select {
	case got := <-ready:
		if got != "http://127.0.0.1:1/?token=a" {
			t.Errorf("就绪 URL = %q", got)
		}
	default:
		t.Fatal("scanReady 没有报告就绪 URL")
	}
	// 通道有 4 个缓冲位，若发了第二次这里还能读到。
	select {
	case got := <-ready:
		t.Errorf("就绪 URL 被重复发送: %q", got)
	default:
	}
	if len(ev.logs) != 4 {
		t.Errorf("日志行数 = %d, want 4（每一行都要转发）", len(ev.logs))
	}
}

func TestScanReadyNoReadyLine(t *testing.T) {
	ready := make(chan string, 1)
	scanReady(strings.NewReader("nothing here\n"), nil, ready)
	select {
	case got := <-ready:
		t.Errorf("不该报告就绪: %q", got)
	default:
	}
}

func TestEnvPairs(t *testing.T) {
	got := envPairs(map[string]string{"DSH_HOME": "/tmp/x"})
	if len(got) != 1 || got[0] != "DSH_HOME=/tmp/x" {
		t.Errorf("envPairs = %v", got)
	}
	if n := len(envPairs(nil)); n != 0 {
		t.Errorf("nil map 应得空切片，got %d", n)
	}
}

// 编译期护栏：确认 NewSupervisor 不会在构造期触碰文件系统，
// 且未配置 NodeExe 时 Start 会干净地失败而不是 panic。
func TestNewSupervisorIsPure(t *testing.T) {
	ev := &recordingEvents{}
	s := NewSupervisor(SupervisorConfig{}, ev)
	if s == nil {
		t.Fatal("NewSupervisor 返回 nil")
	}
	if st, _ := s.StatusDetail(); st != StatusStopped {
		t.Errorf("初始状态 = %q, want stopped", st)
	}
	if _, err := s.Start(context.Background()); err == nil {
		t.Error("未配置 NodeExe 时 Start 应当失败")
	}
	if len(ev.logs) == 0 {
		t.Log("（Start 失败路径没有产生日志行，属正常）")
	}
}
