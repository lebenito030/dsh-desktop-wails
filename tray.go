// tray.go 保存托盘在各平台之间**共用**的状态与文案。
// 平台实现见 tray_windows.go（原生 Win32）与 tray_unix.go（darwin/linux）。
package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"dsh-desktop-wails/internal/dsh"
)

// 托盘菜单三项在各平台的实现里都要用同一套语义，文案也保持一致：
// 重启服务 / 检查更新 / 退出。
const (
	trayMenuRestartText = "重启服务"
	trayMenuUpdateText  = "检查更新"
	trayMenuQuitText    = "退出"
)

var (
	// trayMu 保护 trayCtl（Windows）与 trayTip / trayIconUp（全平台）。
	trayMu sync.Mutex

	// trayTip 在托盘尚未就绪时暂存最后一次状态，就绪后补上，
	// 避免启动早期那几秒的状态丢失。
	trayTip string

	// trayIconUp 表示托盘图标是否已成功登记。
	trayIconUp bool
)

// trayAlive 报告托盘图标当前是否已登记成功。
//
// 关窗行为依赖它。本项目的约定是「关窗 = 隐藏到托盘，只有从托盘退出才真正退出」，
// 但这套约定成立的前提是托盘真的在。Linux 上不成立：GNOME 默认不显示
// StatusNotifierItem 图标，用户得装 AppIndicator 扩展或 snixembed 之类的代理才看得见；
// 登记成功不等于用户看得见。若此时仍把窗口藏起来，用户就再也找不回界面、也无处退出。
// 所以托盘不可用时必须退化成「关窗即退出」——宁可行为不一致，也不要留下唤不回的窗口。
func trayAlive() bool {
	trayMu.Lock()
	defer trayMu.Unlock()
	return trayIconUp
}

func setTrayIconAlive(v bool) {
	trayMu.Lock()
	trayIconUp = v
	trayMu.Unlock()
}

// trayStatusTip 把 DSH 状态翻成托盘 tooltip 文案（Windows 的 szTip / Unix 的图标提示）。
func trayStatusTip(s dsh.Status, detail string) string {
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
	return tip
}

// newTrayLogger 把托盘日志写到数据目录下的 tray.log，并在有 stderr 时同步打一份。
//
// 注意别用 io.MultiWriter：它在第一个 writer 报错时就整体返回，而 GUI 子系统
// 进程（wails build 的产物）根本没有控制台，写 os.Stderr 必然拿到
// "句柄无效"，于是日志文件会永远停在 0 字节——正是加这层日志要防的情况。
// teeWriter 逐个写、忽略个别失败，坏掉一个不影响另一个。
func newTrayLogger(dir string) *log.Logger {
	var writers []io.Writer

	path := filepath.Join(dir, "tray.log")
	f, err := os.Create(path)
	if err != nil {
		path = filepath.Join(os.TempDir(), "dsh-desktop-tray.log")
		f, err = os.Create(path)
	}
	if err == nil {
		writers = append(writers, f)
	}
	writers = append(writers, os.Stderr)

	lg := log.New(teeWriter(writers), "[tray] ", log.LstdFlags|log.Lmicroseconds)
	if err != nil {
		lg.Printf("警告：日志文件不可写（%v），只输出到 stderr", err)
	} else {
		lg.Printf("日志文件：%s", path)
	}
	return lg
}

// teeWriter 依次写多个 writer，忽略个别 writer 的失败。
type teeWriter []io.Writer

func (t teeWriter) Write(p []byte) (int, error) {
	var lastErr error
	n := 0
	for _, w := range t {
		wn, err := w.Write(p)
		if err != nil {
			lastErr = err
			continue
		}
		n = wn
	}
	if n == 0 && lastErr != nil {
		return 0, lastErr
	}
	return n, nil
}
