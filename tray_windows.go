//go:build windows

// tray_windows.go 用原生 Win32 API 实现系统托盘，不依赖第三方 systray 库。
//
// 为什么自己写：systray 这类库隐含要求「注册窗口类 / 创建窗口」与随后的
// GetMessage 消息循环跑在同一个 OS 线程上——Windows 的消息队列是线程私有的，
// GetMessage(NULL,...) 只取调用线程的消息。而 Go 调度器并不保证一个 goroutine
// 一直待在原线程（除非显式 runtime.LockOSThread），一旦时机不巧被挪走，
// 消息循环就永远收不到 WM_LBUTTONUP / WM_COMMAND，表现为「图标在、点什么都没
// 反应」；更麻烦的是库内部只用 log.Printf 报错，而 GUI 子系统进程没有 stderr，
// 失败是完全不可见的。
//
// 本实现把上面每个前提都握在自己手里：
//   - 托盘 goroutine 第一行就 LockOSThread，窗口创建与消息循环同线程；
//   - 用 TPM_RETURNCMD 直接拿菜单选中项，避开阻塞回调期间的 WM_COMMAND 丢失；
//   - 每一步结果写进 <数据目录>/tray.log，出问题可直接查。
package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf16"
	"unsafe"

	"dsh-desktop-wails/internal/dsh"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

//go:embed build/tray.ico
var trayIcon []byte

// ---- Win32 常量 ----

const (
	// 自定义回调消息：Shell_NotifyIcon 把鼠标事件投递到窗口的这个消息上。
	msgTrayCallback = 0x8000 + 1 // WM_APP + 1

	wmNull          = 0x0000
	wmClose         = 0x0010
	wmDestroy       = 0x0002
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	csHRedraw = 0x0002
	csVRedraw = 0x0001

	wsPopup     = 0x80000000
	wsExToolWnd = 0x00000080 // 不进 Alt+Tab / 任务栏

	// NOTIFYICONDATA 标志
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	// Shell_NotifyIcon 动作
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	// 菜单项标志
	mfString    = 0x00000000
	mfSeparator = 0x00000800

	// TrackPopupMenu 标志
	tpmLeftAlign   = 0x0000
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100 // 直接返回选中项 ID，不投递 WM_COMMAND

	idxSmCXSmIcon = 49 // GetSystemMetrics 索引：小图标宽度

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040

	iconResVersion = 0x00030000

	trayIconTipMax = 127 // szTip 128 个 UTF-16 单元，留一个给结尾 NUL
)

// 托盘菜单项 ID。菜单只有三项：重启服务 / 检查更新 / 退出。
// 「显示主窗口」不放进菜单——左键单击托盘图标已经是它（见 trayWndProc）；
// 「关闭服务」（停 DSH 但留着壳）已按 docs/01 的功能准入裁掉。
const (
	trayIDRestart = 1001
	trayIDUpdate  = 1002
	trayIDQuit    = 1003
)

// ---- Win32 调用表 ----

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")

	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procRegisterWindowMsgW = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu    = user32.NewProc("CreatePopupMenu")
	procAppendMenuW        = user32.NewProc("AppendMenuW")
	procDestroyMenu        = user32.NewProc("DestroyMenu")
	procTrackPopupMenu     = user32.NewProc("TrackPopupMenu")
	procSetForegroundWnd   = user32.NewProc("SetForegroundWindow")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procCreateIconFromRes  = user32.NewProc("CreateIconFromResourceEx")
	procLoadImageW         = user32.NewProc("LoadImageW")
	procDestroyIcon        = user32.NewProc("DestroyIcon")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procGetProcessDpiAwareness = shcore.NewProc("GetProcessDpiAwareness")
)

// ---- Win32 结构体 ----

type w32Point struct {
	x, y int32
}

type w32Guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// wndClassExW 对应 WNDCLASSEXW。
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

// notifyIconDataW 对应 NOTIFYICONDATAW（x64 下 cbSize=976，字段顺序与对齐
// 必须与 Windows 头文件严格一致）。
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         w32Guid
	hBalloonIcon     windows.Handle
}

// w32Msg 对应 MSG。
type w32Msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      w32Point
}

// ---- 全局状态 ----

var (
	// 托盘窗口类名必须常驻内存：RegisterClassExW 不复制 lpszClassName，
	// 类注销前该指针都得有效，所以放在包级变量里。
	trayClassName = mustUTF16("DSHDesktopTrayWnd")

	trayWndProcPtr = windows.NewCallback(trayWndProc)

	// explorer 重启后会广播 TaskbarCreated，需要在启动时查出它的消息号。
	taskbarCreatedMsg uint32

	// trayCtl 由 tray.go 里的 trayMu 保护（跨平台共用状态）。
	trayCtl *trayController
)

// trayController 持有托盘窗口/图标/菜单句柄。
type trayController struct {
	app *App
	log *log.Logger

	hwnd  windows.Handle
	hicon windows.Handle
	menu  windows.Handle
	nid   *notifyIconDataW

	mu    sync.Mutex
	ready bool
	tip   string
}

// setupTray 启动托盘。由 app.startup 以独立 goroutine 调用。
func setupTray(app *App) {
	// 关键：必须在 goroutine 的第一行锁线程。窗口在此线程创建，GetMessage
	// 也只取此线程的消息队列，两者必须同线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	tc := &trayController{app: app, log: newTrayLogger(app.paths.Root)}
	trayMu.Lock()
	trayCtl = tc
	pending := trayTip
	trayMu.Unlock()

	tc.log.Printf("初始化：图标 %d 字节，数据目录 %q", len(trayIcon), app.paths.Root)

	if err := tc.createWindow(); err != nil {
		tc.log.Printf("失败：创建托盘窗口：%v", err)
		return
	}
	if err := tc.addIcon(); err != nil {
		tc.log.Printf("失败：注册托盘图标：%v", err)
		return
	}
	setTrayIconAlive(true)
	if err := tc.buildMenu(); err != nil {
		tc.log.Printf("失败：构建托盘菜单：%v", err)
		return
	}

	tc.mu.Lock()
	tc.ready = true
	tip := tc.tip
	tc.mu.Unlock()
	tc.log.Printf("就绪：hwnd=%#x hicon=%#x menu=%#x", tc.hwnd, tc.hicon, tc.menu)

	if tip == "" {
		tip = pending
	}
	if tip == "" {
		tip = "DSH Desktop"
	}
	tc.setTooltip(tip)

	tc.messageLoop()
	// 图标已摘除：此后关窗要走「退出」而不是「隐藏」，否则窗口无处唤回。
	setTrayIconAlive(false)
	tc.log.Printf("消息循环退出，托盘结束")
}

// traySetStatus 让托盘 tooltip 跟随 DSH 状态（由 app 的 OnStatus 调用）。
func traySetStatus(s dsh.Status, detail string) {
	tip := trayStatusTip(s, detail)
	trayMu.Lock()
	tc := trayCtl
	if tc == nil {
		trayTip = tip
	}
	trayMu.Unlock()
	if tc != nil {
		tc.setTooltip(tip)
	}
}

// trayShutdown 请求托盘线程收尾（摘掉图标，避免留下悬空的幽灵图标）。
// 由 App.QuitApp / App.shutdown 调用；进程即将退出，投递即可，不必等待。
func trayShutdown() {
	trayMu.Lock()
	tc := trayCtl
	trayMu.Unlock()
	if tc == nil {
		return
	}
	tc.mu.Lock()
	hwnd := tc.hwnd
	tc.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
	}
}

// ---- 窗口 ----

func (tc *trayController) createWindow() error {
	// 查一次 TaskbarCreated 的消息号（同一字符串全局唯一，重复注册返回同值）。
	name, _ := windows.UTF16FromString("TaskbarCreated")
	if r, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(&name[0]))); r != 0 {
		taskbarCreatedMsg = uint32(r)
		tc.log.Printf("TaskbarCreated 消息号 = %#x", taskbarCreatedMsg)
	}

	hInst, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassExW{
		style:         csHRedraw | csVRedraw,
		lpfnWndProc:   trayWndProcPtr,
		hInstance:     windows.Handle(hInst),
		lpszClassName: &trayClassName[0],
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW: %v", err)
	}

	// 普通顶层窗口 + WS_POPUP + 从不 Show：不会出现在任务栏，也能收到
	// TaskbarCreated 这类广播（HWND_MESSAGE 消息窗口收不到广播）。
	hwnd, _, err := procCreateWindowExW.Call(
		wsExToolWnd,
		uintptr(unsafe.Pointer(&trayClassName[0])),
		0,
		wsPopup,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", err)
	}
	tc.hwnd = windows.Handle(hwnd)
	tc.log.Printf("隐藏窗口已创建 hwnd=%#x", tc.hwnd)
	return nil
}

func (tc *trayController) messageLoop() {
	var msg w32Msg
	for {
		r, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(r) {
		case -1:
			tc.log.Printf("GetMessage 出错：%v", err)
			return
		case 0:
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}
}

// trayWndProc 必须是包级普通函数：windows.NewCallback 只保证对普通函数取到
// 正确的入口地址，闭包/方法值在跨 C 边界调用时拿不到 Go 的闭包上下文。
func trayWndProc(hwnd windows.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	tc := currentTray()
	if tc != nil {
		switch {
		case msg == msgTrayCallback:
			switch uint32(lparam) {
			case wmLButtonUp, wmLButtonDblClk:
				tc.log.Printf("托盘：左键点击 → 显示窗口")
				tc.showWindow()
			case wmRButtonUp:
				tc.popupMenu()
			}
			return 0

		case taskbarCreatedMsg != 0 && msg == taskbarCreatedMsg:
			tc.log.Printf("托盘：explorer 重启，重新挂载图标")
			tc.reAddIcon()
			return 0

		case msg == wmClose:
			procDestroyWindow.Call(uintptr(hwnd))
			return 0

		case msg == wmDestroy:
			tc.removeIcon()
			procPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}

// ---- 图标 ----

func (tc *trayController) addIcon() error {
	hicon, desc, err := loadTrayIcon(trayIcon)
	if err != nil {
		return err
	}
	tc.hicon = hicon
	tc.log.Printf("图标句柄已创建 hicon=%#x（%s）", hicon, desc)

	nid := &notifyIconDataW{
		hWnd:             tc.hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: msgTrayCallback,
		hIcon:            hicon,
	}
	nid.cbSize = uint32(unsafe.Sizeof(*nid))
	fillUTF16(nid.szTip[:], "DSH Desktop", trayIconTipMax)

	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(nid))); r == 0 {
		procDestroyIcon.Call(uintptr(hicon))
		return fmt.Errorf("Shell_NotifyIcon(NIM_ADD): %v", err)
	}
	tc.nid = nid
	tc.log.Printf("Shell_NotifyIcon(NIM_ADD) 成功，图标应已出现在通知区域")
	return nil
}

func (tc *trayController) reAddIcon() {
	tc.mu.Lock()
	nid := tc.nid
	tc.mu.Unlock()
	if nid == nil {
		return
	}
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(nid))); r == 0 {
		tc.log.Printf("重新挂载图标失败：%v", err)
	}
}

func (tc *trayController) removeIcon() {
	tc.mu.Lock()
	nid, hicon := tc.nid, tc.hicon
	tc.nid = nil
	tc.mu.Unlock()
	if nid != nil {
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(nid)))
	}
	if hicon != 0 {
		procDestroyIcon.Call(uintptr(hicon))
	}
}

// setTooltip 可从任意 goroutine 调用；托盘尚未就绪时先记下，就绪后补发。
func (tc *trayController) setTooltip(tip string) {
	tc.mu.Lock()
	tc.tip = tip
	if !tc.ready || tc.nid == nil {
		tc.mu.Unlock()
		return
	}
	nid := tc.nid
	fillUTF16(nid.szTip[:], tip, trayIconTipMax)
	nid.uFlags |= nifTip
	tc.mu.Unlock()

	if r, _, err := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(nid))); r == 0 {
		tc.log.Printf("更新 tooltip 失败：%v", err)
		return
	}
	tc.log.Printf("tooltip → %s", tip)
}

// loadTrayIcon 从 ICO 字节里挑一张最贴合当前小图标尺寸的图，
// 用 CreateIconFromResourceEx 直接建成 HICON（不落临时文件）。
// 万一失败则退回「写临时文件 + LoadImage」，两条路都记录日志。
func loadTrayIcon(data []byte) (windows.Handle, string, error) {
	want := trayIconSize()
	img, size, err := pickICOImage(data, want)
	if err != nil {
		return 0, "", err
	}
	h, _, callErr := procCreateIconFromRes.Call(
		uintptr(unsafe.Pointer(&img[0])),
		uintptr(len(img)),
		1, // fIcon = TRUE
		iconResVersion,
		0, 0, // cx/cy = 0 → 用图片自身尺寸
		0, // LR_DEFAULTCOLOR
	)
	if h != 0 {
		return windows.Handle(h), fmt.Sprintf("取 %dx%d 图，CreateIconFromResourceEx", size, size), nil
	}

	path := filepath.Join(os.TempDir(), "dsh-desktop-tray.ico")
	if werr := os.WriteFile(path, data, 0o644); werr != nil {
		return 0, "", fmt.Errorf("CreateIconFromResourceEx: %v；写临时图标文件也失败: %w", callErr, werr)
	}
	p, _ := windows.UTF16FromString(path)
	h, _, lerr := procLoadImageW.Call(0, uintptr(unsafe.Pointer(&p[0])), imageIcon, 0, 0,
		lrLoadFromFile|lrDefaultSize)
	if h == 0 {
		return 0, "", fmt.Errorf("CreateIconFromResourceEx: %v；LoadImage 回退也失败: %v", callErr, lerr)
	}
	return windows.Handle(h), "LoadImage 回退（临时文件）", nil
}

// pickICOImage 解析 ICO 目录，选出边长最接近 want 的那张图的原始数据。
func pickICOImage(data []byte, want int) ([]byte, int, error) {
	if len(data) < 6 {
		return nil, 0, fmt.Errorf("图标数据只有 %d 字节，过短", len(data))
	}
	if binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		return nil, 0, fmt.Errorf("不是合法的 ICO（魔数不匹配）")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	best, bestSize, bestDelta := -1, 0, 0
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(data) {
			break
		}
		size := int(data[off])
		if size == 0 {
			size = 256
		}
		blobLen := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
		blobOff := int(binary.LittleEndian.Uint32(data[off+12 : off+16]))
		if blobLen <= 0 || blobOff < 0 || blobOff+blobLen > len(data) {
			continue
		}
		delta := want - size
		if delta < 0 {
			delta = -delta
		}
		// 距离相同的两张取大的：缩小比放大清晰。
		if best < 0 || delta < bestDelta || (delta == bestDelta && size > bestSize) {
			best, bestSize, bestDelta = i, size, delta
		}
	}
	if best < 0 {
		return nil, 0, fmt.Errorf("ICO 里没有可用的图像条目")
	}
	off := 6 + best*16
	blobLen := int(binary.LittleEndian.Uint32(data[off+8 : off+12]))
	blobOff := int(binary.LittleEndian.Uint32(data[off+12 : off+16]))
	return data[blobOff : blobOff+blobLen], bestSize, nil
}

// trayIconSize 返回托盘图标该用的像素边长。
//
// GetSystemMetrics(SM_CXSMICON) 只在进程 DPI-aware 时才给出真实尺寸：
// 经 wails build 打进去的清单声明了 per-monitor-v2（150% 缩放下是 24px），
// 但直接 go build 出来的 exe 没有清单，系统会把结果虚拟化成 16，此时再让
// Windows 把 16px 放大到 24px 就发虚。所以非 DPI-aware 时取 32px 交给
// shell 缩小——缩小永远比放大清晰。
func trayIconSize() int {
	size := int(smCXSmIcon())
	if size > 16 {
		return size // DPI-aware 且缩放大于 100%，值可信
	}
	if processDPIAware() {
		return size // 确实是 100% 缩放，16px 就是最佳尺寸
	}
	return 32
}

func smCXSmIcon() int32 {
	r, _, _ := procGetSystemMetrics.Call(idxSmCXSmIcon)
	if r == 0 {
		return 16
	}
	return int32(r)
}

// processDPIAware 判断当前进程是否声明了 DPI 感知。
func processDPIAware() bool {
	if procGetProcessDpiAwareness.Find() != nil {
		return true // 老系统查不到，按原值处理
	}
	var awareness uint32
	r, _, _ := procGetProcessDpiAwareness.Call(0, uintptr(unsafe.Pointer(&awareness)))
	if int32(r) != 0 { // 非 S_OK
		return true
	}
	return awareness != 0 // PROCESS_DPI_UNAWARE == 0
}

// ---- 菜单 ----

func (tc *trayController) buildMenu() error {
	menu, _, err := procCreatePopupMenu.Call()
	if menu == 0 {
		return fmt.Errorf("CreatePopupMenu: %v", err)
	}
	m := windows.Handle(menu)

	appendItem := func(id int, text string) error {
		p, _ := windows.UTF16FromString(text)
		r, _, e := procAppendMenuW.Call(uintptr(m), mfString, uintptr(id), uintptr(unsafe.Pointer(&p[0])))
		if r == 0 {
			return fmt.Errorf("AppendMenuW(%s): %v", text, e)
		}
		return nil
	}
	appendSep := func() error {
		if r, _, e := procAppendMenuW.Call(uintptr(m), mfSeparator, 0, 0); r == 0 {
			return fmt.Errorf("AppendMenuW(分隔线): %v", e)
		}
		return nil
	}

	steps := []func() error{
		func() error { return appendItem(trayIDRestart, "重启服务") },
		func() error { return appendItem(trayIDUpdate, "检查更新") },
		appendSep,
		func() error { return appendItem(trayIDQuit, "退出") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			procDestroyMenu.Call(uintptr(m))
			return err
		}
	}
	tc.menu = m
	tc.log.Printf("菜单已构建 menu=%#x（重启服务/检查更新/退出）", m)
	return nil
}

func (tc *trayController) popupMenu() {
	tc.mu.Lock()
	menu, hwnd := tc.menu, tc.hwnd
	tc.mu.Unlock()
	if menu == 0 {
		return
	}

	var pt w32Point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 弹菜单前把托盘窗口设成前台，否则菜单不会随点击别处而收起。
	procSetForegroundWnd.Call(uintptr(hwnd))

	cmd, _, err := procTrackPopupMenu.Call(
		uintptr(menu),
		tpmLeftAlign|tpmBottomAlign|tpmRightButton|tpmReturnCmd,
		uintptr(int64(pt.x)), uintptr(int64(pt.y)),
		0, uintptr(hwnd), 0,
	)
	procPostMessageW.Call(uintptr(hwnd), wmNull, 0, 0)

	id := int32(cmd)
	if id == 0 {
		return // 用户点了别处，菜单取消
	}
	tc.log.Printf("菜单选中 id=%d", id)
	tc.dispatchMenu(id, err)
}

// dispatchMenu 把菜单动作放到独立 goroutine：重启要等子进程退出，
// 卡在托盘线程里会让后续消息全部滞留。
func (tc *trayController) dispatchMenu(id int32, callErr error) {
	go func() {
		switch id {
		case trayIDRestart:
			tc.log.Printf("执行：重启服务")
			if err := tc.app.RestartDSH(); err != nil {
				tc.log.Printf("重启失败：%v", err)
				wruntime.LogErrorf(tc.app.ctx, "托盘重启失败: %v", err)
			}
		case trayIDUpdate:
			tc.log.Printf("执行：检查更新")
			tc.app.CheckUpdate()
		case trayIDQuit:
			tc.log.Printf("执行：退出")
			tc.app.QuitApp()
		default:
			tc.log.Printf("未知菜单 id=%d（TrackPopupMenu err=%v）", id, callErr)
		}
	}()
}

func (tc *trayController) showWindow() {
	if tc.app.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(tc.app.ctx)
	wruntime.WindowShow(tc.app.ctx)
}

// ---- 工具 ----

func currentTray() *trayController {
	trayMu.Lock()
	defer trayMu.Unlock()
	return trayCtl
}

func mustUTF16(s string) []uint16 {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		panic("tray: 非法窗口类名: " + s)
	}
	return u
}

// fillUTF16 把 s 写进定长 UTF-16 缓冲，超长按字符（不是字节）截断并置 NUL。
func fillUTF16(dst []uint16, s string, maxUnits int) {
	for i := range dst {
		dst[i] = 0
	}
	r := []rune(s)
	if len(r) > maxUnits {
		r = r[:maxUnits]
	}
	u := utf16.Encode(r)
	if len(u) > len(dst)-1 {
		u = u[:len(dst)-1]
	}
	copy(dst, u)
}

