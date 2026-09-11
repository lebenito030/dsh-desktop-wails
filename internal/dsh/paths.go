// Package dsh 提供对被托管 DSH 进程的监督：路径解析、进程树回收（Job
// Object）与启动/停止/重启状态机。
package dsh

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths 是解析后的数据目录布局。
type Paths struct {
	// Root 是数据目录（runtime/ 与 config.json 所在处）。
	Root string
	// NodeExe 是捆绑的 node.exe 路径。
	NodeExe string
	// NpmCmd 是捆绑的 npm.cmd 路径。
	NpmCmd string
	// DshDir 是 dsh npm 包的 --prefix 安装目录。
	DshDir string
	// DshBin 是 dsh CLI 的入口 bin.js。
	DshBin string
	// DshPackageJSON 是本地安装的 dsh package.json 路径（读版本用）。
	DshPackageJSON string
	// ConfigFile 是 config.json 路径。
	ConfigFile string
}

// ResolvePaths 由数据目录根推导全部子路径。
func ResolvePaths(root string) Paths {
	return Paths{
		Root:           root,
		NodeExe:        filepath.Join(root, "runtime", "node", "node.exe"),
		NpmCmd:         filepath.Join(root, "runtime", "node", "npm.cmd"),
		DshDir:         filepath.Join(root, "runtime", "dsh"),
		DshBin:         filepath.Join(root, "runtime", "dsh", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"),
		DshPackageJSON: filepath.Join(root, "runtime", "dsh", "node_modules", "@deepseek-ai", "dsh", "package.json"),
		ConfigFile:     filepath.Join(root, "config.json"),
	}
}

// ResolveDataDir 决定数据目录：exe 旁 dsh-desktop-data（可写则用，便携模式）>
// %LOCALAPPDATA%\dsh-desktop-wails 回退。返回实际选中的目录与是否为便携模式。
//
// dirHint 是外部指定入口（非空则直接采用）。当前唯一调用方 app.go 传空串：
// 本项目**有意不提供「自定义数据目录」功能**（配置项 dataDir 已按
// docs/01-design.md 的「功能准入」裁掉），留这个参数只为标明扩展点。
func ResolveDataDir(dirHint string) (string, bool, error) {
	if dirHint != "" {
		if err := os.MkdirAll(dirHint, 0o755); err != nil {
			return "", false, err
		}
		return dirHint, false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	portable := filepath.Join(filepath.Dir(exe), "dsh-desktop-data")
	if isWritable(portable) {
		return portable, true, nil
	}
	fallback := filepath.Join(os.Getenv("LOCALAPPDATA"), "dsh-desktop-wails")
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return "", false, err
	}
	return fallback, false, nil
}

// isWritable 探测目录可写：能创建并删除临时文件即算可写。
func isWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// LocalVersion 读取本地已安装 dsh 的版本号；未安装时返回空串。
func (p Paths) LocalVersion() string {
	raw, err := os.ReadFile(p.DshPackageJSON)
	if err != nil {
		return ""
	}
	// 只取 version 字段，避免引入完整结构体耦合 npm 元数据。
	s := string(raw)
	const key = `"version"`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	rest = rest[j+1:]
	k := strings.Index(rest, `"`)
	if k < 0 {
		return ""
	}
	return rest[:k]
}
