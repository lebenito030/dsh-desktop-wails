//go:build windows

package main

import (
	_ "embed"

	"dsh-desktop-wails/internal/dsh"

	"github.com/energye/systray"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/tray.ico
var trayIcon []byte

// trayController 持托盘菜单项引用，供状态联动刷新。
type trayController struct {
	app *App
}

func setupTray(app *App) {
	tc := &trayController{app: app}
	// 双击托盘 = 聚焦显示主窗口；左键单击也唤起。
	systray.SetOnClick(func(menu systray.IMenu) { tc.showWindow() })
	systray.SetOnDClick(func(menu systray.IMenu) { tc.showWindow() })
	systray.Run(tc.onReady, nil)
}

func (tc *trayController) onReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle("")
	systray.SetTooltip("DSH Desktop")

	mShow := systray.AddMenuItem("显示主窗口", "显示主窗口")
	mRestart := systray.AddMenuItem("重启服务", "重启 DSH 运行时")
	mStop := systray.AddMenuItem("关闭服务", "停止 DSH 运行时")
	mUpdate := systray.AddMenuItem("检查更新", "对比 npm registry 最新版")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "停止 DSH 并退出")

	mShow.Click(func() { tc.showWindow() })
	mRestart.Click(func() {
		if err := tc.app.RestartDSH(); err != nil {
			wruntime.LogErrorf(tc.app.ctx, "托盘重启失败: %v", err)
		}
	})
	mStop.Click(func() { tc.app.StopDSH() })
	mUpdate.Click(func() { tc.app.CheckUpdate() })
	mQuit.Click(func() { tc.app.QuitApp() })
}

func (tc *trayController) showWindow() {
	if tc.app.ctx != nil {
		wruntime.WindowUnminimise(tc.app.ctx)
		wruntime.WindowShow(tc.app.ctx)
	}
}

// traySetStatus 让托盘 tooltip 跟随 DSH 状态（由 app 的 OnStatus 调用）。
func traySetStatus(s dsh.Status, detail string) {
	tip := "DSH Desktop — "
	switch s {
	case dsh.StatusReady:
		tip += "运行中"
	case dsh.StatusStarting:
		tip += "启动中…"
	case dsh.StatusRestarting:
		tip += "重启中…"
	case dsh.StatusUpdating:
		tip += "更新中…"
	case dsh.StatusError:
		tip += "出错"
		if detail != "" {
			tip += ": " + detail
		}
	default:
		tip += "已停止"
	}
	systray.SetTooltip(tip)
}
