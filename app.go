package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"dsh-desktop-wails/internal/bootstrap"
	"dsh-desktop-wails/internal/config"
	"dsh-desktop-wails/internal/dsh"
	"dsh-desktop-wails/internal/proxy"
	"dsh-desktop-wails/internal/update"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是绑定到前端的壳应用：串起 bootstrap / supervisor / update，
// 并把它们的状态经 Wails 事件推给前端。
type App struct {
	ctx      context.Context
	cfg      config.Config
	paths    dsh.Paths
	inst     *bootstrap.Installer
	sup      *dsh.Supervisor
	upd      update.Checker
	prx      *proxy.Proxy
	updating bool
	quitting bool
}

func NewApp() *App {
	return &App{}
}

// startup 在 Wails 起来后调用：解析配置、装配各模块、发起启动编排。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 带 --quit 启动、但单实例锁没有触发（说明本来就没有别的实例在跑）：
	// 这里没有任何需要清理的东西（DSH 还没起来），直接退出即可。
	if wantQuit(os.Args[1:]) {
		log.Printf("--quit：没有正在运行的实例可通知，直接退出")
		os.Exit(0)
	}

	dataDir, _, err := dsh.ResolveDataDir("")
	if err != nil {
		wruntime.LogFatal(ctx, "解析数据目录失败: "+err.Error())
		return
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		wruntime.LogError(ctx, "加载配置失败（使用默认值）: "+err.Error())
		cfg = config.Default()
	}
	a.cfg = cfg
	a.paths = dsh.ResolvePaths(dataDir)
	a.inst = &bootstrap.Installer{Cfg: cfg, Paths: a.paths, OnLog: func(line string) {
		wruntime.EventsEmit(a.ctx, "runtime:log", line)
	}}
	a.upd = update.Checker{Registry: cfg.NpmRegistry, Package: cfg.DshPackage}

	env := map[string]string{}
	if cfg.DshHome != "" {
		env["DSH_HOME"] = cfg.DshHome
	}
	a.sup = dsh.NewSupervisor(dsh.SupervisorConfig{
		NodeExe: a.paths.NodeExe,
		DshBin:  a.paths.DshBin,
		Args:    []string{"--no-open", "--port", "0"},
		Env:     env,
	}, a)

	a.prx = proxy.New()
	if err := a.prx.Start(); err != nil {
		wruntime.LogError(ctx, "代理启动失败: "+err.Error())
	}
	go setupTray(a)
	go a.bootstrapAndStart()
}

// bootstrapAndStart：确保运行时就位（首启动下载）→ 启动 DSH。就绪后
// supervisor 触发 OnURL（含 token），由其完成 cookie 兑换与代理接线；
// 最后异步查更新。为在查更新前等到代理接线完成，这里轮询代理地址。
func (a *App) bootstrapAndStart() {
	ctx := a.ctx
	if err := a.inst.EnsureRuntime(ctx, a.reportBootstrap); err != nil {
		return // PhaseFailed 已上报，等用户点重试（RetryBootstrap）
	}
	if _, err := a.sup.Start(ctx); err != nil {
		return // 状态事件已上报
	}
	// 启动完成后异步检查更新；结果经 update:available 事件到前端弹窗。
	go a.checkUpdate(false)
}

// wireProxy 用就绪 URL 兑换会话 cookie 并把代理指向上游；随后通知前端
// 改用代理地址加载 iframe。
func (a *App) wireProxy(readyURL string) {
	cookies, err := proxy.ExchangeToken(readyURL)
	if err != nil {
		log.Printf("token 兑换失败: %v", err)
		// 兑换失败时退回直连（至少浏览器场景可用），前端仍收到 runtime:url。
		a.OnURL(readyURL)
		return
	}
	// 去掉 query 里的 token，得到上游根（http://127.0.0.1:port）。
	base := readyURL
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if err := a.prx.SetTarget(base, cookies); err != nil {
		log.Printf("代理目标设置失败: %v", err)
		a.OnURL(readyURL)
		return
	}
	a.OnURL(a.prx.Addr + "/")
}

// reportBootstrap 把 bootstrap 进度翻译成前端事件。
func (a *App) reportBootstrap(phase bootstrap.Phase, detail string, got, total int64, indeterminate bool) {
	wruntime.EventsEmit(a.ctx, "bootstrap:progress", map[string]any{
		"phase": string(phase), "detail": detail,
		"downloaded": got, "total": total, "indeterminate": indeterminate,
	})
}

// dsh.Events 实现：supervisor 状态 → 前端事件 + 托盘 tooltip。
// 注意：supervisor 的 OnURL 是直连 URL（含 token），不直接透传给前端——
// 前端一律使用 wireProxy 发出的代理地址（OnURL 的调用方只有 supervisor
// 与 wireProxy，这里拦截 supervisor 路径，改为触发 cookie 兑换）。
func (a *App) OnStatus(s dsh.Status, detail string) {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "runtime:status", map[string]any{"status": string(s), "detail": detail})
	}
	traySetStatus(s, detail)
}

// OnURL 由 supervisor 在就绪时调用（readyURL 含 token）。这里完成
// cookie 兑换并向前端发代理地址；兑换失败退回直连地址。
func (a *App) OnURL(readyURL string) {
	if strings.Contains(readyURL, "token=") {
		a.wireProxy(readyURL)
		return
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "runtime:url", readyURL)
	}
}

func (a *App) OnLog(line string) {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "runtime:log", line)
	}
}

// ---- 前端绑定方法 ----

// StatusPayload 是 GetStatus 的返回。
type StatusPayload struct {
	Status       string `json:"status"`
	Detail       string `json:"detail"`
	URL          string `json:"url"`
	LocalVersion string `json:"localVersion"`
	DataDir      string `json:"dataDir"`
}

// GetStatus 供前端初始化时拉取当前全量状态。
func (a *App) GetStatus() StatusPayload {
	st, detail := dsh.StatusStopped, ""
	if a.sup != nil {
		st, detail = a.sup.StatusDetail()
	}
	// 前端用代理地址；代理未接线（DSH 未就绪）时为空。
	url := ""
	if a.prx != nil && a.prx.Addr != "" && st == dsh.StatusReady {
		url = a.prx.Addr + "/"
	}
	local := ""
	if a.paths.DshPackageJSON != "" {
		local = a.paths.LocalVersion()
	}
	dir := ""
	if a.paths.Root != "" {
		dir = a.paths.Root
	}
	return StatusPayload{
		Status: string(st), Detail: detail, URL: url,
		LocalVersion: local, DataDir: dir,
	}
}

// RestartDSH 由工具栏/托盘触发；并发调用在 supervisor 内合并。
func (a *App) RestartDSH() error {
	if a.sup == nil {
		return fmt.Errorf("应用尚未完成初始化")
	}
	_, err := a.sup.Restart(a.ctx, dsh.StatusStopped)
	return err
}

// StopDSH 停止 DSH 运行时（托盘「关闭服务」）；壳保持运行。
func (a *App) StopDSH() error {
	if a.sup == nil {
		return fmt.Errorf("应用尚未完成初始化")
	}
	return a.sup.Stop(a.ctx, dsh.StatusStopped)
}

// ---- 窗口控制（无边框窗口的自绘按钮） ----

// WindowMin 最小化窗口。
func (a *App) WindowMin() {
	if a.ctx != nil {
		wruntime.WindowMinimise(a.ctx)
	}
}

// WindowToggleMax 最大化/还原切换，并同步前端按钮状态。
func (a *App) WindowToggleMax() {
	if a.ctx == nil {
		return
	}
	if wruntime.WindowIsMaximised(a.ctx) {
		wruntime.WindowUnmaximise(a.ctx)
	} else {
		wruntime.WindowMaximise(a.ctx)
	}
	wruntime.EventsEmit(a.ctx, "window:maximised", wruntime.WindowIsMaximised(a.ctx))
}

// WindowHideToTray 自绘关闭按钮的动作：隐藏窗口到托盘。
//
// 托盘不可用时退化为退出。原因见 trayAlive 的注释：把窗口藏进一个用户看不见的
// 托盘，等于让应用变得不可达——既没有界面，也没有地方退出。
func (a *App) WindowHideToTray() {
	if a.ctx == nil {
		return
	}
	if !trayAlive() {
		log.Printf("无托盘可用，关窗改为退出")
		a.QuitApp()
		return
	}
	wruntime.WindowHide(a.ctx)
}

// RetryBootstrap 首装失败后由前端重试按钮触发。
func (a *App) RetryBootstrap() {
	if a.inst == nil {
		return
	}
	go a.bootstrapAndStart()
}

// CheckUpdate 手动（托盘）触发更新检查；无更新时走 update:none 事件。
func (a *App) CheckUpdate() {
	go a.checkUpdate(true)
}

func (a *App) checkUpdate(manual bool) {
	if a.ctx == nil || a.paths.DshPackageJSON == "" {
		return
	}
	res, err := a.upd.Check(a.ctx, a.paths)
	if err != nil {
		if manual {
			wruntime.EventsEmit(a.ctx, "update:none", "检查更新失败: "+err.Error())
		}
		return
	}
	switch {
	case res.Available:
		wruntime.EventsEmit(a.ctx, "update:available", map[string]any{
			"local": res.LocalVersion, "latest": res.LatestVersion,
		})
	case manual:
		wruntime.EventsEmit(a.ctx, "update:none", fmt.Sprintf("已是最新版本 %s", res.LocalVersion))
	}
}

// ApplyUpdate 用户确认更新后调用：停 DSH → npm 装新版 → 重启。
// 全程经 update:progress 事件汇报；失败时旧版本未被破坏，重启旧版继续用。
func (a *App) ApplyUpdate() {
	if a.updating {
		return
	}
	a.updating = true
	defer func() { a.updating = false }()

	ctx := a.ctx
	_ = a.sup.Stop(ctx, dsh.StatusUpdating)
	report := func(phase bootstrap.Phase, detail string, got, total int64, indeterminate bool) {
		wruntime.EventsEmit(ctx, "update:progress", map[string]any{
			"phase": string(phase), "detail": detail,
			"downloaded": got, "total": total, "indeterminate": indeterminate,
		})
	}
	if err := a.inst.UpdateDsh(ctx, report); err != nil {
		// npm install 失败不落盘新版本，旧包完好：重启旧版继续用。
		wruntime.EventsEmit(ctx, "update:progress", map[string]any{
			"phase": string(bootstrap.PhaseFailed), "detail": err.Error(),
		})
		_, _ = a.sup.Start(ctx)
		return
	}
	wruntime.EventsEmit(ctx, "update:progress", map[string]any{
		"phase": string(bootstrap.PhaseDone),
	})
	if _, err := a.sup.Start(ctx); err != nil {
		return
	}
	go a.checkUpdate(false)
}

// beforeClose 在窗口被关闭、或 runtime.Quit 触发退出时被调用。
// 返回 true = 取消关闭。
//
// 约定是「关窗 = 隐藏到托盘，只有从托盘退出才真正退出」。但这套约定成立的前提
// 是托盘真的在：托盘不可用时（Linux 上 GNOME 默认不显示 SNI 图标等），隐藏窗口
// 会让用户再也找不回界面、也无处退出，所以此时退化成「关窗即退出」。
// 因此 main.go 里 HideWindowOnClose 必须关掉——关窗路径必须经过这里才能做判断。
func (a *App) beforeClose(ctx context.Context) bool {
	if a.quitting {
		return false
	}
	if trayAlive() {
		wruntime.WindowHide(ctx)
		return true
	}
	log.Printf("无托盘可用，关窗改为退出")
	a.quitting = true
	return false
}

// onSecondInstance 处理「重复启动」：默认唤起已有窗口；带 --quit 则退出。
// --quit 是给托盘不可见的场景留的逃生口——例如 GNOME 没装 AppIndicator 扩展时，
// 用户既看不见托盘图标，托盘又是唯一的退出途径。见 docs/05 第 6 节。
func (a *App) onSecondInstance(args []string) {
	if wantQuit(args) {
		log.Printf("收到 --quit，退出运行中的实例")
		a.QuitApp()
		return
	}
	a.activateFromSecondInstance()
}

// wantQuit 判断命令行参数是否在请求退出正在运行的实例。
func wantQuit(args []string) bool {
	for _, a := range args {
		if a == "--quit" || a == "-q" {
			return true
		}
	}
	return false
}

// activateFromSecondInstance 二次启动时唤起已有窗口。
func (a *App) activateFromSecondInstance() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

// shutdown 壳退出前的清理：摘掉托盘图标、停 DSH（Job 全树回收）。
func (a *App) shutdown(ctx context.Context) {
	trayShutdown()
	if a.sup != nil {
		_ = a.sup.Stop(ctx, dsh.StatusStopped)
	}
}

// QuitApp 托盘「退出」调用：置退出标志并关窗，走 OnShutdown 清理路径。
func (a *App) QuitApp() {
	a.quitting = true
	trayShutdown()
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}
