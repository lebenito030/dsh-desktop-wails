//go:build darwin || linux

// tray_unix.go —— macOS / Linux 的托盘实现。
//
// 用 energye/systray，而 Windows 仍用 tray_windows.go 里的原生 Win32 实现。
// 这个不对称是**有意的**：
//
//   - Windows 上第三方 systray 不可用，原因是消息队列线程私有——创建窗口与
//     GetMessage 必须同线程，库内部隐含假设做不到，实测表现为「图标在但点了没反应」。
//     详见 docs/02-architecture.md 第 6 节。
//   - macOS/Linux 没有这个约束，而这个库的 darwin(Cocoa) 与 linux(StatusNotifierItem
//     /DBus) 实现是现成且被大量项目验证过的。自己手写 cgo/DBus 在「无法真机验证」
//     的前提下风险反而更大。
//   - 依赖极小：Linux 侧是纯 Go（该 fork 移除了 GTK 依赖），整个库只依赖
//     godbus/dbus/v5 —— 而它本来就在 go.mod 里。
//
// 事件循环：Wails 占着主线程（macOS 的 NSApp runloop / Linux 的 gtk_main），
// 所以不能用会阻塞的 systray.Run()，必须用 RunWithExternalLoop 返回的 start/end。
package main

import (
	_ "embed"
	"log"
	"runtime"
	"sync"

	systray "github.com/energye/systray"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"dsh-desktop-wails/internal/dsh"
)

// 图标资源。macOS 走单色 template（纯黑 + alpha，系统按菜单栏明暗自动反色），
// Linux 走彩色徽标。一次 SetTemplateIcon(template, color) 两个平台都对：
//   - darwin 的实现**只取第 1 个参数**并打上 template 标记；
//   - linux 的实现**只取第 2 个参数**（SetTemplateIcon 直接转发成 SetIcon）。
//
//go:embed build/tray-template.png
var trayTemplatePNG []byte

//go:embed build/tray.png
var trayColorPNG []byte

var (
	trayUniMu   sync.Mutex
	trayUniApp  *App
	trayUniStop func() // RunWithExternalLoop 返回的 end，退出时调用
)

// setupTray 启动托盘。由 app.startup 以独立 goroutine 调用。
func setupTray(app *App) {
	lg := newTrayLogger(app.paths.Root)
	trayUniMu.Lock()
	trayUniApp = app
	trayUniMu.Unlock()

	lg.Printf("初始化（%s）：template %d 字节 / color %d 字节，数据目录 %q",
		runtime.GOOS, len(trayTemplatePNG), len(trayColorPNG), app.paths.Root)

	start, end := systray.RunWithExternalLoop(
		func() { trayOnReady(lg) },
		func() { lg.Printf("托盘已退出") },
	)
	trayUniMu.Lock()
	trayUniStop = end
	trayUniMu.Unlock()

	start()
	lg.Printf("已启动。注意「图标登记成功」不等于「用户看得见」：GNOME 默认不显示 " +
		"StatusNotifierItem，需要 AppIndicator 扩展或 snixembed 之类的代理")
}

// trayOnReady 在托盘侧就绪回调里建图标与菜单。
// Linux 上它先于 DBus 登记被调用，所以此时只能设置「待生效」的内容，
// 由库内部在登记完成后套用。
func trayOnReady(lg *log.Logger) {
	systray.SetTemplateIcon(trayTemplatePNG, trayColorPNG)
	systray.SetTooltip(trayPendingTip())

	mShow := systray.AddMenuItem("显示主窗口", "显示 DSH Desktop 主窗口")
	mRestart := systray.AddMenuItem(trayMenuRestartText, "重启 DSH 服务")
	mUpdate := systray.AddMenuItem(trayMenuUpdateText, "检查 DSH 是否有新版本")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem(trayMenuQuitText, "退出 DSH Desktop（同时停止 DSH）")

	mShow.Click(func() { trayShowWindow() })
	mRestart.Click(func() { trayRestart(lg) })
	mUpdate.Click(func() { trayCheckUpdate() })
	mQuit.Click(func() { trayQuit(lg) })

	// Linux 上左键单击可以经 SNI 的 Activate 直接唤窗；macOS 上不装这个回调
	// —— 那里「点图标弹菜单」是平台约定，装上会把菜单行为顶掉，
	// 所以 macOS 依赖菜单里的「显示主窗口」回到窗口。
	if runtime.GOOS == "linux" {
		systray.SetOnClick(func(systray.IMenu) { trayShowWindow() })
	}

	setTrayIconAlive(true)
	lg.Printf("就绪：菜单已建（显示主窗口 / 重启服务 / 检查更新 / 退出）")
}

// traySetStatus 让托盘提示跟随 DSH 状态（由 app 的 OnStatus 调用）。
func traySetStatus(s dsh.Status, detail string, versions string) {
	tip := trayStatusTip(s, detail, versions)

	trayMu.Lock()
	trayTip = tip
	alive := trayIconUp
	trayMu.Unlock()

	if alive {
		systray.SetTooltip(tip)
	}
}

// trayShutdown 摘掉托盘图标。由 App.QuitApp / App.shutdown 调用。
func trayShutdown() {
	trayUniMu.Lock()
	stop := trayUniStop
	trayUniStop = nil
	trayUniMu.Unlock()

	if stop != nil {
		stop() // nativeEnd + systray.Quit
	}
	setTrayIconAlive(false)
}

// ---- 菜单动作 ----

func trayShowWindow() {
	app := trayAppRef()
	if app == nil || app.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(app.ctx)
	wruntime.WindowShow(app.ctx)
}

func trayRestart(lg *log.Logger) {
	app := trayAppRef()
	if app == nil {
		return
	}
	lg.Printf("执行：重启服务")
	if err := app.RestartDSH(); err != nil {
		lg.Printf("重启失败：%v", err)
	}
}

func trayCheckUpdate() {
	if app := trayAppRef(); app != nil {
		app.CheckUpdate()
	}
}

func trayQuit(lg *log.Logger) {
	lg.Printf("执行：退出")
	if app := trayAppRef(); app != nil {
		app.QuitApp()
	}
}

func trayAppRef() *App {
	trayUniMu.Lock()
	defer trayUniMu.Unlock()
	return trayUniApp
}

// trayPendingTip 取启动早期暂存的最后一条状态，没有就用应用名兜底。
func trayPendingTip() string {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayTip == "" {
		return "DSH Desktop"
	}
	return trayTip
}
