# dsh-desktop-wails

最小的 DSH（DeepSeek Harness）桌面套壳：Golang + Wails v2。单 exe，不嵌入任何 DSH 功能，只在壳层提供：

- **自举安装**：首次启动自动下载 Node.js 并安装 `@deepseek-ai/dsh` 到数据目录（进度条 + 日志）；
- **托管 Web UI**：内置窗口加载 DSH Web 界面（经本地代理自动完成登录 token 兑换，无需浏览器）；
- **重启 DSH**：顶部工具栏一键重启（自动适配新端口与新 token）；
- **更新检查**：每次启动对比 npm registry，发现新版弹窗确认后自动更新并重启；
- **托盘**：关窗隐藏到托盘，托盘菜单提供显示窗口 / 重启 / 检查更新 / 退出。Win32 原生实现（`tray_windows.go`），不依赖第三方 systray 库；排查日志写在数据目录的 `tray.log`；
- **图标**：DSH logo（`build/dsh-logo.svg` 为源文件），托盘用 `build/tray.ico`（16/20/24/32/48），exe 与窗口用 `build/appicon.png` + `build/windows/icon.ico`；
- **进程安全**：DSH 进程树挂 Windows Job Object（kill-on-close），套壳退出/被杀时自动回收，无孤儿进程。

## 构建

依赖：Go 1.25+、Node 20+、Wails CLI v2。

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
wails build
# 产物：build/bin/dsh-desktop.exe
```

## 运行时布局

数据目录优先 exe 旁的 `dsh-desktop-data\`（便携模式），不可写时回退 `%LOCALAPPDATA%\dsh-desktop-wails\`：

```
dsh-desktop-data/
├── config.json          # 配置（首次运行生成）
└── runtime/
    ├── node/            # 官方 Node zip 解压（node.exe / node_modules/npm）
    └── dsh/             # npm --prefix 安装的 @deepseek-ai/dsh
```

## 配置（config.json）

| 字段 | 默认 | 说明 |
|---|---|---|
| `nodeVersion` | `22.20.0` | 自举下载的 Node 版本 |
| `nodeDownloadUrl` | `https://nodejs.org/dist/v%s/node-v%s-win-x64.zip` | Node zip 模板，`%s` 两处填版本 |
| `npmRegistry` | `https://registry.npmjs.org` | npm 镜像 |
| `dshPackage` | `@deepseek-ai/dsh` | 安装的包名 |
| `dshVersion` | `latest` | 安装的版本或 dist-tag |
| `dshHome` | 空 | 覆盖 `DSH_HOME` 环境变量 |
| `dataDir` | 空 | 强制指定数据目录 |

### 国内镜像示例

```json
{
  "nodeDownloadUrl": "https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip",
  "npmRegistry": "https://registry.npmmirror.com"
}
```

## 架构要点

- **就绪协议**：DSH 启动后在 stdout 打印 `dsh web: <URL>`（含登录 token），监督器按行解析；
- **cookie-in-proxy**：DSH 的会话 cookie 是 `SameSite=Strict`，跨站 iframe（wails.localhost → 127.0.0.1）中会被浏览器丢弃。壳在 Go 侧用就绪 URL 兑换 cookie，由本地反向代理（127.0.0.1 随机端口）对每个请求代附，并对 WebSocket 透明升级转发；iframe 一律指向代理地址；
- **重启合并**：并发重启请求在 supervisor 内串行合并（stop → start → 重新兑换 cookie → 前端刷新 iframe）；
- **托盘为什么自己实现**：Windows 的消息队列是线程私有的，`GetMessage` 只取调用线程的消息。第三方 systray 库隐含假设「创建窗口」与「消息循环」始终在同一 OS 线程，而 Go 调度器不保证 goroutine 留在原线程；一旦被挪走，托盘图标在但点击/菜单全无反应。这里由托盘 goroutine 首行 `runtime.LockOSThread` 把这层不确定性消掉，并用 `TPM_RETURNCMD` 直接取菜单选中项，规避阻塞回调期间的 `WM_COMMAND` 丢失。

## 开发

```bash
wails doctor   # 环境检查
wails dev      # 热重载开发
```

前端为 Vite + 原生 TypeScript（`frontend/`），Go 侧模块在 `internal/`：`config`（配置）、`bootstrap`（下载/解压/npm 安装）、`dsh`（supervisor + Job Object）、`proxy`（cookie 反代）、`update`（registry 版本对比）。
