// Package bootstrap 负责把 DSH 运行时（Node + npm 包）安装到数据目录。
// 首次启动与更新共用同一套下载/安装原语。
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"dsh-desktop-wails/internal/config"
	"dsh-desktop-wails/internal/dsh"
)

// Progress 是安装各阶段向上层（app）的进度回调。phase 取值见 phase 常量。
// downloaded/total 字节；indeterminate=true 表示无总量的阶段（npm install）。
type Progress func(phase Phase, detail string, downloaded, total int64, indeterminate bool)

// Phase 是安装流程的阶段。
type Phase string

const (
	PhaseCheck   Phase = "check"   // 存在性检查
	PhaseNode    Phase = "node"    // 下载/解压 Node
	PhaseDsh     Phase = "dsh"     // npm install dsh
	PhaseDone    Phase = "done"    // 全部完成
	PhaseFailed  Phase = "failed"  // 失败（detail 为原因）
	PhaseSkipped Phase = "skipped" // 已就位，跳过
)

// Installer 持有配置与路径，安装操作的可重入入口。
type Installer struct {
	Cfg   config.Config
	Paths dsh.Paths
	OnLog func(line string)
}

// RuntimeReady 判断运行时是否已就位（node.exe 与 dsh bin.js 都存在）。
func (in *Installer) RuntimeReady() bool {
	if _, err := os.Stat(in.Paths.NodeExe); err != nil {
		return false
	}
	if _, err := os.Stat(in.Paths.DshBin); err != nil {
		return false
	}
	return true
}

// EnsureRuntime 幂等地把运行时装齐：缺 Node 装 Node，缺 dsh 装 dsh。
// 每阶段经 progress 上报。任何阶段失败返回 error 并上报 PhaseFailed。
func (in *Installer) EnsureRuntime(ctx context.Context, progress Progress) error {
	if in.RuntimeReady() {
		progress(PhaseCheck, "", 0, 0, false)
		progress(PhaseSkipped, "", 0, 0, false)
		return nil
	}

	// --- Node ---
	if _, err := os.Stat(in.Paths.NodeExe); err != nil {
		url := fmt.Sprintf(in.Cfg.NodeDownloadURL, in.Cfg.NodeVersion, in.Cfg.NodeVersion)
		zipPath := filepath.Join(in.Paths.Root, "runtime", "node-download.zip")
		if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
			return in.fail(progress, err)
		}
		in.logf("下载 Node %s ...", in.Cfg.NodeVersion)
		if err := Download(ctx, url, zipPath, func(got, total int64) {
			progress(PhaseNode, "下载 Node", got, total, total <= 0)
		}); err != nil {
			return in.fail(progress, fmt.Errorf("下载 Node 失败: %w", err))
		}
		in.logf("解压 Node ...")
		progress(PhaseNode, "解压 Node", 0, 0, true)
		// ExtractZipTopDir 会剥掉 zip 的单一顶层目录（node-vX-win-x64），
		// 内容直接落到 dest；因此 dest 就是 runtime/node。
		nodeDir := filepath.Join(in.Paths.Root, "runtime", "node")
		if err := ExtractZipTopDir(zipPath, nodeDir); err != nil {
			return in.fail(progress, fmt.Errorf("解压 Node 失败: %w", err))
		}
		_ = os.Remove(zipPath)
		if _, err := os.Stat(in.Paths.NodeExe); err != nil {
			return in.fail(progress, fmt.Errorf("解压完成但 %s 不存在", in.Paths.NodeExe))
		}
	}

	// --- dsh ---
	in.logf("安装 %s@%s ...", in.Cfg.DshPackage, in.Cfg.DshVersion)
	if err := in.npmInstall(ctx, in.Cfg.DshPackage+"@"+in.Cfg.DshVersion, func(line string) {
		progress(PhaseDsh, line, 0, 0, true)
	}); err != nil {
		return in.fail(progress, fmt.Errorf("安装 DSH 失败: %w", err))
	}
	if !in.RuntimeReady() {
		return in.fail(progress, fmt.Errorf("安装完成但 %s 不存在", in.Paths.DshBin))
	}

	progress(PhaseDone, "", 0, 0, false)
	return nil
}

// UpdateDsh 只重装 dsh 包（Node 不动）：Stop DSH 后调用，装完再 Start。
// 终态（done/failed）不由这里上报——「完成」要看重启是否成功，由调用方定。
func (in *Installer) UpdateDsh(ctx context.Context, progress Progress) error {
	in.logf("更新 %s@latest ...", in.Cfg.DshPackage)
	if err := in.npmInstall(ctx, in.Cfg.DshPackage+"@latest", func(line string) {
		progress(PhaseDsh, line, 0, 0, true)
	}); err != nil {
		return in.fail(progress, fmt.Errorf("更新 DSH 失败: %w", err))
	}
	// 装完即校验运行时就位：上游若改了 bin 路径，这里给明确报错，
	// 而不是等 Start 后 45s 就绪超时。
	if !in.RuntimeReady() {
		return in.fail(progress, fmt.Errorf("更新完成但 %s 不存在", in.Paths.DshBin))
	}
	return nil
}

// prependPath 把 dir 前置到环境变量 PATH 的值里（不改变其余部分）。
// npm install 的依赖可能带 install 脚本（如 koffi 的 `node ./cnoke.cjs`），
// 这些脚本经 cmd 起子进程后按 PATH 解析 `node`；捆绑 node 不前置进去，
// 在没有全局 node 的机器上（本项目目标用户的标准处境）install 必失败。
func prependPath(env []string, dir string) []string {
	sep := ";"
	if runtime.GOOS != "windows" {
		sep = ":"
	}
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			out = append(out, "PATH="+dir+sep+kv[5:])
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+dir)
	}
	return out
}

// lineWriter 按 Write 到达顺序拆行并回调 onLine，流式透传子进程输出。
// 之前用 CombinedOutput 攒到进程退出才统一回调，npm 安装的一两分钟里
// 前端收不到任何进度事件，用户看到的就是「点了更新没反应」。
type lineWriter struct {
	onLine func(string)
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.onLine(line)
		}
	}
	return len(p), nil
}

// npmInstall 用捆绑的 node 运行 npm-cli.js，以 --prefix 方式安装一个包说明符。
// （npm.cmd 是批处理，node 无法直接执行；经 cmd /c 调起又会引入 cmd 中间层，
// 直接用 npm-cli.js 最干净。）
func (in *Installer) npmInstall(ctx context.Context, spec string, onLine func(string)) error {
	if err := os.MkdirAll(in.Paths.DshDir, 0o755); err != nil {
		return err
	}
	npmCli := filepath.Join(filepath.Dir(in.Paths.NodeExe), "node_modules", "npm", "bin", "npm-cli.js")
	if _, err := os.Stat(npmCli); err != nil {
		return fmt.Errorf("找不到 npm-cli.js（%s）: %w", npmCli, err)
	}
	cmd := exec.CommandContext(ctx, in.Paths.NodeExe, npmCli, "install", spec, "--prefix", in.Paths.DshDir, "--registry", in.Cfg.NpmRegistry, "--no-audit", "--no-fund", "--loglevel=info")
	cmd.Dir = in.Paths.DshDir
	// npm 的依赖安装脚本可能再调 node / npm（koffi 的 install 脚本就调 node），
	// 它们按 PATH 找可执行文件；捆绑 node 必须进子进程 PATH。
	cmd.Env = prependPath(os.Environ(), filepath.Dir(in.Paths.NodeExe))
	hideChildWindow(cmd)
	lw := &lineWriter{onLine: onLine}
	cmd.Stdout = lw
	cmd.Stderr = lw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("npm install %s: %w", spec, err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("npm install %s: %w", spec, err)
	}
	return nil
}

func (in *Installer) fail(progress Progress, err error) error {
	progress(PhaseFailed, err.Error(), 0, 0, false)
	in.logf("安装失败: %v", err)
	return err
}

func (in *Installer) logf(format string, args ...any) {
	if in.OnLog != nil {
		in.OnLog(fmt.Sprintf(format, args...))
	}
}
