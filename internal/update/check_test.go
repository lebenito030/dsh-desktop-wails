package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"dsh-desktop-wails/internal/dsh"
)

// newer 的语义：npm 语义版本按数值比；预发布比同号正式版低。
// 这套比较直接决定「要不要弹更新提示」，错了要么骚扰用户要么漏更新。
func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.5", "0.1.4", true},
		{"0.1.5", "0.1.5", false},
		{"0.1.4", "0.1.5", false},
		{"0.2.0", "0.1.99", true},
		{"1.0.0", "0.99.99", true},
		{"10.0.0", "9.0.0", true},
		{"9.10.0", "9.9.0", true}, // 数值比较，不是字符串比较
		{"0.1.5-rc.2", "0.1.5-rc.1", true},
		{"0.1.5-rc.1", "0.1.5", false},   // 预发布 < 同号正式版
		{"0.1.5", "0.1.5-rc.1", true},
		{"0.1.5-rc.1", "0.1.4", true},    // 预发布仍高于更低的正式版
		{"v1.2.3", "1.2.2", true},        // 容忍 v 前缀
		{"not-a-version", "0.1.4", true}, // 解析失败退回字符串不等比较
		{"same", "same", false},
	}
	for _, tc := range cases {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	if s, ok := parseSemver("0.1.5-rc.1"); !ok || s.pre != "rc.1" || s.nums != [3]int{0, 1, 5} {
		t.Errorf("parseSemver(0.1.5-rc.1) = %+v, ok=%v", s, ok)
	}
	if _, ok := parseSemver("1.2"); ok {
		t.Error("两段版本号应判为不可解析")
	}
	if _, ok := parseSemver("1.2.x"); ok {
		t.Error("非数字段应判为不可解析")
	}
}

// Latest 只取 registry 响应里的 version 字段，这里用 httptest 顶掉真实网络。
func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@deepseek-ai%2Fdsh/latest" && r.URL.Path != "/@deepseek-ai/dsh/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"@deepseek-ai/dsh","version":"0.1.5-rc.1"}`))
	}))
	defer srv.Close()

	c := Checker{Registry: srv.URL, Package: "@deepseek-ai/dsh"}
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got != "0.1.5-rc.1" {
		t.Errorf("Latest = %q", got)
	}
}

func TestLatestNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	c := Checker{Registry: srv.URL, Package: "@deepseek-ai/dsh"}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("非 200 响应应当报错")
	}
}

// 本地未安装（版本为空）时不得提示更新——首装是 bootstrap 的事，不是更新的事。
func TestCheckNoLocalVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	paths := dsh.ResolvePaths(dir) // 未写 package.json → LocalVersion 为空
	c := Checker{Registry: srv.URL, Package: "@deepseek-ai/dsh"}
	res, err := c.Check(context.Background(), paths)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Available {
		t.Errorf("本地未安装时不应提示更新: %+v", res)
	}
	if res.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q", res.LatestVersion)
	}
}
