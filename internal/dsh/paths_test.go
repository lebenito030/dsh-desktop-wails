package dsh

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 目录布局是壳与 DSH 之间最「脆」的契约之一：supervisor 靠它找 node.exe 与 bin.js，
// bootstrap 靠它落包。改路径常量必须连这里一起改。
func TestResolvePathsLayout(t *testing.T) {
	p := ResolvePaths(filepath.Join("data", "root"))

	want := map[string]string{
		"NodeExe":        filepath.Join("data", "root", "runtime", "node", "node.exe"),
		"NpmCmd":         filepath.Join("data", "root", "runtime", "node", "npm.cmd"),
		"DshDir":         filepath.Join("data", "root", "runtime", "dsh"),
		"DshBin":         filepath.Join("data", "root", "runtime", "dsh", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
		"DshPackageJSON": filepath.Join("data", "root", "runtime", "dsh", "node_modules", "@deepseek-ai", "dsh", "package.json"),
		"ConfigFile":     filepath.Join("data", "root", "config.json"),
	}
	got := map[string]string{
		"NodeExe":        p.NodeExe,
		"NpmCmd":         p.NpmCmd,
		"DshDir":         p.DshDir,
		"DshBin":         p.DshBin,
		"DshPackageJSON": p.DshPackageJSON,
		"ConfigFile":     p.ConfigFile,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestResolveDataDirUsesHintVerbatim(t *testing.T) {
	hint := filepath.Join(t.TempDir(), "custom-data")
	got, portable, err := ResolveDataDir(hint)
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	if got != hint {
		t.Errorf("dirHint = %q, want %q", got, hint)
	}
	if portable {
		t.Errorf("显式 hint 不应被当作便携模式")
	}
	if fi, err := os.Stat(hint); err != nil || !fi.IsDir() {
		t.Errorf("hint 目录应被创建，stat = %v, err = %v", fi, err)
	}
}

// LocalVersion 只从 package.json 里抠 version 字段，格式容错值得钉住。
func TestLocalVersion(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	if v := p.LocalVersion(); v != "" {
		t.Errorf("未安装时应返回空串，got %q", v)
	}

	if err := os.MkdirAll(filepath.Dir(p.DshPackageJSON), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "name": "@deepseek-ai/dsh",
  "version": "0.1.5-rc.1",
  "description": "DSH"
}`
	if err := os.WriteFile(p.DshPackageJSON, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := p.LocalVersion(); v != "0.1.5-rc.1" {
		t.Errorf("LocalVersion = %q, want 0.1.5-rc.1", v)
	}
}

// isWritable 用「建目录 + 写删探针文件」探测，这条路在三个平台上都要走得通。
func TestIsWritable(t *testing.T) {
	if !isWritable(t.TempDir()) {
		t.Fatal("临时目录应可写")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows 才有受保护的系统目录可拿来当反例")
	}
	if isWritable(filepath.Join(`C:\Windows`, "System32", "definitely-not-writable-probe")) {
		t.Log("System32 竟然可写（可能以管理员运行），跳过断言")
	}
}
