// Package config 持有 dsh-desktop 壳的配置文件读写。
// 配置随数据目录存放（config.json），首次运行以默认值生成。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Node 下载源与 npm registry 的默认值；国内用户可在 config.json 里换成镜像。
const (
	DefaultNodeVersion     = "22.20.0"
	DefaultNodeDownloadURL = "https://nodejs.org/dist/v%s/node-v%s-win-x64.zip"
	DefaultNpmRegistry     = "https://registry.npmjs.org"
	DefaultDshPackage      = "@deepseek-ai/dsh"
)

// Config 是 config.json 的结构。零值不可用，一律经 Load 产生。
type Config struct {
	// NodeVersion 用于自举下载的 Node LTS 版本（仅主版本语义，补丁随默认值走）。
	NodeVersion string `json:"nodeVersion"`
	// NodeDownloadURL 是 Node zip 下载地址模板，%s 依次填版本号两处。
	NodeDownloadURL string `json:"nodeDownloadUrl"`
	// NpmRegistry 是 npm install 使用的 --registry。
	NpmRegistry string `json:"npmRegistry"`
	// DshPackage 是安装的 npm 包名。
	DshPackage string `json:"dshPackage"`
	// DshVersion 是安装的版本，空串或 "latest" 表示 dist-tag latest。
	DshVersion string `json:"dshVersion"`
	// DshHome 覆盖 DSH_HOME 环境变量；空串表示不覆盖。
	DshHome string `json:"dshHome"`
	// DataDir 强制指定数据目录；空串表示自动探测（exe 旁，回退 LOCALAPPDATA）。
	DataDir string `json:"dataDir"`
}

// Default 返回内置默认配置。
func Default() Config {
	return Config{
		NodeVersion:     DefaultNodeVersion,
		NodeDownloadURL: DefaultNodeDownloadURL,
		NpmRegistry:     DefaultNpmRegistry,
		DshPackage:      DefaultDshPackage,
		DshVersion:      "latest",
	}
}

// Sanitize 补齐空字段并校验模板占位符。
func (c *Config) Sanitize() error {
	d := Default()
	if c.NodeVersion == "" {
		c.NodeVersion = d.NodeVersion
	}
	if c.NodeDownloadURL == "" {
		c.NodeDownloadURL = d.NodeDownloadURL
	}
	if c.NpmRegistry == "" {
		c.NpmRegistry = d.NpmRegistry
	}
	if c.DshPackage == "" {
		c.DshPackage = d.DshPackage
	}
	if c.DshVersion == "" {
		c.DshVersion = "latest"
	}
	if fmt.Sprintf(c.NodeDownloadURL, "x", "x") == "" {
		// Sprintf 不校验多余占位符，这里只拦格式串本身非法的情况。
		return fmt.Errorf("nodeDownloadUrl 不是合法的格式模板")
	}
	return nil
}

// Load 读取 dir/config.json；文件不存在时写入默认值再返回。dir 必须已存在。
func Load(dir string) (Config, error) {
	c := Default()
	path := filepath.Join(dir, "config.json")
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("解析 %s 失败: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return c, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if err := c.Sanitize(); err != nil {
		return c, err
	}
	// 回写以便用户看到全部可配置项（含默认值）；失败不致命。
	if raw, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(path, raw, 0o644)
	}
	return c, nil
}
