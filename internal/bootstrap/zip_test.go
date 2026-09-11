package bootstrap

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// 造一个内存里的 zip 再落盘，供解压测试使用。
func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, body := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// Node 官方 zip 的顶层只有一个 node-vX-win-x64/ 目录，解压时必须剥掉它——
// 否则运行时路径会多出一层，supervisor 就找不到 node.exe。这是自举的关键步骤。
func TestExtractZipTopDirStripsSingleRoot(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "node.zip")
	writeZip(t, zipPath, map[string]string{
		"node-v22.20.0-win-x64/node.exe":        "fake-exe",
		"node-v22.20.0-win-x64/npm.cmd":         "fake-npm",
		"node-v22.20.0-win-x64/sub/readme.txt":  "hello",
	})

	dest := filepath.Join(dir, "runtime", "node")
	if err := ExtractZipTopDir(zipPath, dest); err != nil {
		t.Fatalf("ExtractZipTopDir: %v", err)
	}

	for _, rel := range []string{"node.exe", "npm.cmd", filepath.Join("sub", "readme.txt")} {
		body, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Errorf("%s 未被解出: %v", rel, err)
			continue
		}
		if rel == filepath.Join("sub", "readme.txt") && string(body) != "hello" {
			t.Errorf("内容不符: %q", body)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "node-v22.20.0-win-x64")); !os.IsNotExist(err) {
		t.Errorf("顶层目录应被剥掉，stat err = %v", err)
	}
}

// 顶层有多个条目时没有「唯一根目录」可剥，内容必须原样落地。
func TestExtractZipKeepsMultipleRoots(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "mixed.zip")
	writeZip(t, zipPath, map[string]string{
		"a.txt":       "A",
		"dir/b.txt":   "B",
	})

	dest := filepath.Join(dir, "out")
	if err := ExtractZipTopDir(zipPath, dest); err != nil {
		t.Fatalf("ExtractZipTopDir: %v", err)
	}
	for _, rel := range []string{"a.txt", filepath.Join("dir", "b.txt")} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("%s 应原样解出: %v", rel, err)
		}
	}
}

func TestExtractZipMissingFile(t *testing.T) {
	if err := ExtractZipTopDir(filepath.Join(t.TempDir(), "nope.zip"), t.TempDir()); err == nil {
		t.Fatal("解压不存在的 zip 应当报错")
	}
}
