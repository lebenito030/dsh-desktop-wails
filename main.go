package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "DSH Desktop",
		Width:     1180,
		Height:    800,
		MinWidth:  720,
		MinHeight: 480,
		// 无边框：窗口装饰由前端自绘（右上角最小化/最大化/关闭），
		// 拖动经顶部工具栏的 --wails-draggable 区域，边缘 resize 仍可用。
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 18, G: 24, B: 39, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		// 关窗 = 隐藏到托盘（Wails 在 WM_CLOSE 时直接 Hide，不走 Quit）；
		// 真正退出只经托盘「退出」→ QuitApp → runtime.Quit。
		HideWindowOnClose: true,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "dsh-desktop-wails-7f3c2a91",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.activateFromSecondInstance()
			},
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
