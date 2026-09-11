# AGENTS.md

给在本仓库工作的 AI agent 的落地说明。人读的文档在 [`docs/`](docs/README.md)。

## 这个项目是什么

**DSH（DeepSeek Harness）的轻量桌面套壳**：Golang + Wails v2，单 exe。
壳只做四件事——把 DSH 装起来、跑起来、托管住、把它的 Web UI 显示出来。
**DSH 本体一行都不改。**

## 铁律

1. **不要修改 / patch / fork / 注入 DSH 本体**，包括数据目录里 `runtime/dsh/node_modules/@deepseek-ai/dsh`
   下的任何文件。DSH 是运行时从 npm 装进来的外部依赖，不是本仓库的源码。
2. **保持壳薄。** 加依赖前先问：没有它 DSH 还能不能跑？桌面框架层的任何问题都不应该
   波及 DSH 进程和它的 HTTP 服务——这是本项目存在的理由，展开在
   [`docs/01-design.md`](docs/01-design.md)。
3. **壳与 DSH 只通过进程间契约耦合**：stdout 就绪行 + HTTP。不要引入 JS 层耦合。
   唯一例外是代理层注入的**只读**探针（主题/侧栏几何），且注入失败必须不影响页面加载。
4. **不要提交** `build/bin/`、`frontend/dist/`、数据目录（都已在 `.gitignore`）。
   `.workbuddy/` 是本地工作记录，保持 untracked。
5. **改托盘 / 窗口 / 图标前先读规范**（[`docs/01-design.md`](docs/01-design.md) 的
   「UI 规范」一节），别凭感觉调数值——这些数字是量出来的，不是拍的。

## 常用命令

```bash
wails build            # 产出 build/bin/dsh-desktop.exe（重建前必须先退出正在运行的实例）
wails dev              # 热重载开发
go vet ./...           # 静态检查
go build ./...         # 只验编译，产出的 exe 没有图标与清单，不是交付物
wails doctor           # 环境自检
cd frontend && npm run build   # 只重建前端
```

判定「壳能不能跑」只看一条：`wails build` 之后 `build/bin/dsh-desktop.exe` 能起来。

## 代码地图

| 路径 | 职责 |
|---|---|
| `main.go` | Wails 装配：窗口尺寸、无边框、单实例锁、`HideWindowOnClose`、生命周期回调 |
| `app.go` | 绑定层：串起下面四个 internal 包，向托盘与前端转发状态 |
| `tray_windows.go` | **原生 Win32 托盘**（不依赖第三方 systray 库）|
| `internal/config` | `config.json` 读写与默认值 |
| `internal/bootstrap` | 下载 Node、`npm install` DSH 到数据目录 |
| `internal/dsh` | DSH 进程监督器 + 路径解析 + Job Object 进程树回收 |
| `internal/proxy` | 持有会话 cookie 的反代 + HTML 探针注入 |
| `internal/update` | 查 npm registry 对比版本 |
| `frontend/` | Vite + 原生 TS 的壳页面（iframe + 自绘窗口按钮 + 浮层）|
| `build/` | 图标、manifest、NSIS 安装器模板（`build/bin/` 是产物目录，勿提交）|

## 改代码时的硬约定

- **托盘线程**：托盘 goroutine 的**第一行**必须是 `runtime.LockOSThread()`。Windows 消息队列
  是线程私有的，窗口创建与 `GetMessage` 循环必须同线程，否则托盘「图标在但点了没反应」。
  同理，别再引入第三方 systray 库——它们做不到这一点。
- **GUI 进程日志**：不要用 `io.MultiWriter` 往 `os.Stderr` + 文件双写。GUI 子系统进程没有
  控制台，写 stderr 必失败，而 `MultiWriter` 遇到第一个失败就整体返回，日志文件会停在 0 字节。
  用 `tray_windows.go` 里的 `teeWriter`。日志落在 `<数据目录>/tray.log`。
- **不要用 stdout/stderr 做诊断**：`wails build` 产出的是 windowsgui 子系统，输出会被丢弃。
- **契约改动要点**：DSH 就绪行由 `internal/dsh/supervisor.go` 的 `readyLine` 正则解析。
  这是壳与 DSH 之间最脆弱的一处耦合，DSH 若改了就绪输出，**只改这里**。
- **提交信息**：英文 conventional commits（`feat(scope):` / `fix(scope):`），正文用英文散文
  说明「为什么」，必要时附项目符号。文档类改动单独成一个 `docs:` commit。

## 环境陷阱（Windows，已实测）

- **别在 agent 的沙箱 shell 里启动 `dsh-desktop.exe`**：WebView2 会以
  `0x8000FFFF Catastrophic failure` 创建 controller 失败。那是沙箱限制，不是代码问题。
  要验证就让用户自己双击，或读它写的日志。
  （判据：Wails 的 `setupChromium()` 在 `OnStartup` 之前，所以「`tray.log` 被创建」就说明
  启动与 WebView2 都成功了。）
- **重建前确认实例已退出**：运行中的 exe 被文件锁占住，`wails build` 覆盖会失败。
  注意本应用「关窗 = 隐藏到托盘」，点 X 不会结束进程。
- **托盘图标可能被 Win11 折叠**：新图标默认进 `^` 溢出区，这不是 bug。要么让用户拖出来一次，
  要么去「设置 → 个性化 → 任务栏 → 其他系统托盘图标」里打开。
- **托管 shell 的 PATH 可能缺 Git 的 `usr/bin`**（表现为 `ls`/`mkdir` command not found）：
  把 `<PortableGit>/versions/*/usr/bin` 与 `.../bin` **追加到 PATH 末尾**即可；
  不要前置，前置会盖掉 `safe-bin` 的删除保护 shim。
- PowerShell 工具在此环境 stdout 不返回，且 `Add-Type`、`Reflection.Assembly` 加载都被
  安全策略禁止（即不能 P/Invoke 截图）。需要抓屏就自己写 Go（GDI `BitBlt`）。

## 文档索引

| 文档 | 内容 |
|---|---|
| [`docs/01-design.md`](docs/01-design.md) | 设计规范：套壳原则、职责边界、失败隔离、UI/图标/日志规范 |
| [`docs/02-architecture.md`](docs/02-architecture.md) | 封装方式与运行机制：启动时序、就绪协议、cookie 反代、进程回收 |
| [`docs/03-layout.md`](docs/03-layout.md) | 目录结构：源码树与数据目录树 |
| [`docs/04-build-and-run.md`](docs/04-build-and-run.md) | 构建与启动流程、常见问题 |
| [`docs/05-configuration.md`](docs/05-configuration.md) | `config.json` 全部配置项 |
| [`docs/06-dependencies-and-versioning.md`](docs/06-dependencies-and-versioning.md) | 依赖管理与 **DSH 版本跟进机制** |
