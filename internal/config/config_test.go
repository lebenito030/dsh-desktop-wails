package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFillsEmptyFieldsWithDefaults(t *testing.T) {
	c := Config{}
	if err := c.Sanitize(); err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	d := Default()
	if c.NodeVersion != d.NodeVersion {
		t.Errorf("NodeVersion = %q, want %q", c.NodeVersion, d.NodeVersion)
	}
	if c.NodeDownloadURL != d.NodeDownloadURL {
		t.Errorf("NodeDownloadURL = %q, want %q", c.NodeDownloadURL, d.NodeDownloadURL)
	}
	if c.NpmRegistry != d.NpmRegistry {
		t.Errorf("NpmRegistry = %q, want %q", c.NpmRegistry, d.NpmRegistry)
	}
	if c.DshPackage != d.DshPackage {
		t.Errorf("DshPackage = %q, want %q", c.DshPackage, d.DshPackage)
	}
	if c.DshVersion != "latest" {
		t.Errorf("DshVersion = %q, want latest", c.DshVersion)
	}
}

func TestSanitizeKeepsUserValues(t *testing.T) {
	c := Config{
		NodeVersion:     "20.18.0",
		NodeDownloadURL: "https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip",
		NpmRegistry:     "https://registry.npmmirror.com",
		DshVersion:      "0.1.4",
	}
	if err := c.Sanitize(); err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if c.NodeVersion != "20.18.0" || c.DshVersion != "0.1.4" {
		t.Fatalf("用户自定义值被默认值覆盖: %+v", c)
	}
	if !strings.Contains(c.NodeDownloadURL, "npmmirror") {
		t.Errorf("NodeDownloadURL 被覆盖: %q", c.NodeDownloadURL)
	}
}

// Load 会对空缺字段补默认值并回写：这是「用户手改 config.json 不必补全字段」
// 这一承诺的基础，值得钉住。
func TestLoadFillsDefaultsAndWritesBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.NodeVersion == "" || c.DshPackage == "" {
		t.Fatalf("默认值未补齐: %+v", c)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("回写文件读取失败: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("回写内容不是合法 JSON: %v", err)
	}
	for _, key := range []string{"nodeVersion", "nodeDownloadUrl", "npmRegistry", "dshPackage", "dshVersion", "dshHome"} {
		if _, ok := back[key]; !ok {
			t.Errorf("回写缺少字段 %q（内容：%s）", key, raw)
		}
	}
	if _, ok := back["dataDir"]; ok {
		t.Errorf("dataDir 已被裁掉，不应再出现在 config.json 里")
	}
}

func TestLoadKeepsUserFileValues(t *testing.T) {
	dir := t.TempDir()
	orig := `{"npmRegistry":"https://registry.npmmirror.com","dshVersion":"0.1.4"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.NpmRegistry != "https://registry.npmmirror.com" || c.DshVersion != "0.1.4" {
		t.Fatalf("用户配置丢失: %+v", c)
	}
}
