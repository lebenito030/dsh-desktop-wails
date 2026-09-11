# 04 · 构建与启动流程

## 1. 环境要求

| 组件 | 版本 | 说明 |
|---|---|---|
| Go | **1.26+** | `go.mod` 声明 `go 1.26.0` |
| Node.js | 20+ | **只用于构建前端**（vite 7）。运行期用的是壳自己下载的 Node，与系统 Node 无关 |
| Wails CLI | v2 | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |
| WebView2 Runtime | — | Windows 10/11 通常已预装；缺失时需单独安装（见第 5 节发布选项） |

自检：`wails doctor`。

## 2. 常用命令

```bash
wails build                 # 生产构建 → build/bin/dsh-desktop.exe
wails dev                   # 开发模式，前端热重载
wails build -o dsh-desktop-new   # 换产物名（当前实例还在跑、文件被锁时很有用）
wails build -windowsconsole      # 保留控制台：**调试日志时最有用**（见下）
wails build -nsis           # 额外产出 NSIS 安装包
wails build -clean          # 先清 build/bin 再构建
wails build -webview2 embed # WebView2 策略：download(默认)/embed/browser/error

go vet ./...                # 静态检查（改完 Go 代码至少跑一次）
go build ./...              # 只验编译；注意：这样出来的 exe 没有图标与清单，不是交付物
cd frontend && npm run build  # 只重建前端（正常用 wails build 即可，它内部会跑）
```

> **调试提示**：生产产物是 windowsgui 子系统，`fmt.Println` / `log.Printf` 的输出**会被丢弃**。
> 需要看输出时用 `wails build -windowsconsole` 构建一个保留控制台的版本，
> 或看 `<数据目录>/tray.log`。

**重建前必须先退出正在运行的实例**：运行中的 exe 被文件锁占用，`wails build` 会因无法
覆盖而失败。注意本应用「关窗 = 隐藏到托盘」，**点右上角 X 不会结束进程**，
要从托盘菜单「退出」或在任务管理器里结束 `dsh-desktop.exe`。

## 3. 构建做了什么

`wails build` 依次做四件事：

1. **Generating bindings** — 依据 `main.go` 的 `Bind` 生成 `frontend/wailsjs/`（Go 方法的 TS 绑定）；
2. **Installing frontend dependencies / Compiling frontend** — `npm install` + `npm run build`
   （命令取自 `wails.json` 的 `frontend:install` / `frontend:build`），产出 `frontend/dist`；
3. **Generating application assets** — 由 `build/appicon.png`、`build/windows/icon.ico`、
   `build/windows/wails.exe.manifest`、`build/windows/info.json` 生成 Windows 资源（.syso）；
4. **Compiling application** — 编译 Go，并 `go:embed all:frontend/dist` 把前端打进 exe。

产物：`build/bin/dsh-desktop.exe`，windowsgui 子系统（**没有控制台**，见
[01-design.md](01-design.md) 第 7 节的日志约束）。

> `build/windows/icon.ico` 若比 `build/appicon.png` 新，Wails 不会覆盖它——所以想自定义
> 多尺寸 ICO 时，按「先写 appicon.png、再写 icon.ico」的顺序落盘即可保住手写的 ICO。

## 4. 启动流程

### 4.1 首次启动（自举安装）

DSH 的运行时不在 exe 里，首次启动要装到数据目录：

| 阶段（`bootstrap:progress` 的 phase） | 做什么 | 前端表现 |
|---|---|---|
| `check` | 检查 `node.exe` 与 `dsh/lib/bin.js` 是否都在 | 无感 |
| `skipped` | 都在，跳过安装 | 无感 |
| `node` | 按 `nodeDownloadUrl` 下载 Node zip（带进度）→ 解压并剥掉顶层目录到 `runtime/node` | 进度条 + 「下载/解压 Node」 |
| `dsh` | `node <npm-cli.js> install @deepseek-ai/dsh@<版本> --prefix runtime/dsh --registry <镜像>` | 不确定态进度条 + npm 日志滚动 |
| `done` | 装完，开始启动 DSH | 浮层消失 |
| `failed` | 任一步失败（detail 为原因） | 浮层显示原因 + 重试按钮 |

装完即 `Supervisor.Start()` 起 DSH 进程，等 stdout 就绪行（≤45s），
就绪后经反代把 iframe 指过去。完整时序见 [02-architecture.md](02-architecture.md) 第 2 节。

**这一整套只在首次启动发生**（数据目录已有 runtime 时走 `skipped`，毫秒级）。

### 4.2 后续启动

```
config.Load → RuntimeReady()==true → Supervisor.Start() → 就绪 → 接线反代 → iframe 加载
                                                                    └→ 异步查更新
```

### 4.3 环境变量

壳只额外设置一个环境变量：`DSH_HOME`（当 `config.json` 的 `dshHome` 非空时）。
其余继承壳进程的环境。子进程以 `CREATE_NO_WINDOW` 启动，不会闪黑窗。

## 5. 发布

- **便携**：直接分发 `build/bin/dsh-desktop.exe` 单个文件。首次运行在 exe 旁创建
  `dsh-desktop-data/`；若 exe 所在目录不可写，自动回退 `%LOCALAPPDATA%\dsh-desktop-wails`。
- **安装包**：`wails build -nsis`，模板在 `build/windows/installer/`。
- **内网 / 离线**：把 `nodeDownloadUrl` 与 `npmRegistry` 指向内网镜像（见
  [05-configuration.md](05-configuration.md)），即可全程不访问公网。

## 6. 排查

先看 `<数据目录>/tray.log`（壳侧）与界面浮层里的 DSH 日志（DSH 侧）。

| 症状 | 先查什么 |
|---|---|
| 托盘图标找不到 | Win11 默认把新图标折叠进 `^` 溢出区——这不是 bug。拖出来一次即可，或「设置 → 个性化 → 任务栏 → 其他系统托盘图标」里打开 |
| 托盘图标在但点了没反应 | `tray.log`。正常情况下应有「就绪」与「托盘：左键点击 → 显示窗口」；若只有「初始化」没有「就绪」，看紧随其后的失败原因 |
| 界面白屏 / `authentication required` | 反代没接上。查 `tray.log` 之外的启动日志：`ExchangeToken` 是否拿到 cookie（兑换失败会降级直连，此时 iframe 可能报鉴权失败） |
| 一启动就闪退、无任何日志 | 多半是 pre-`OnStartup` 失败（WebView2 / 窗口创建）。**注意：在沙箱或非交互桌面里启动 Wails GUI 应用会让 WebView2 报 `0x8000FFFF Catastrophic failure`**，这不是代码问题，换真实桌面环境再试 |
| 一直卡在「正在准备 DSH 运行时」 | 网络/镜像。看浮层里 npm 的输出；确认 `npmRegistry` 可达、`nodeDownloadUrl` 模板两处 `%s` 都在 |
| 装到一半失败，反复失败 | 直接删掉 `<数据目录>/runtime/` 再重启，会完整重装（config.json 不动） |
| DSH 起不来（就绪超时 45s） | 用系统 Node 手动跑一次复现：`node <数据目录>/runtime/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js web --no-open --port 0`，看它输出什么 |
| 更新后想回退 | 改 `config.json` 的 `dshVersion` 为指定版本号（如 `0.3.1`），删掉 `runtime/dsh/` 重启即可定向安装 |

## 7. 开发模式

`wails dev` 会在 Vite 开发服务器 + Wails 窗口之间联调，前端改动热重载、Go 改动自动重编。
两点注意：

- 开发模式下的数据目录与生产**共用**（同样在 exe 旁推导），调试时可以直接删 `runtime/`
  演练首次启动流程；
- 托盘是独立 OS 线程 + 独立消息循环，**改 `tray_windows.go` 后 `wails dev` 不会热重载
  已运行的托盘**，需要完整重启进程才生效。
