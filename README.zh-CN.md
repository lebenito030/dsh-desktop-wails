# DSH Desktop

[English](README.md) | [简体中文](README.zh-CN.md)

**DSH（DeepSeek Harness）的轻量桌面套壳**：单个可执行文件，把 DSH 装起来、跑起来、
托管住，并把它的 Web UI 显示在原生窗口里。Go + [Wails v2](https://wails.io)。

**壳绝不修改 DSH。** DSH 在运行时从 npm 安装（`@deepseek-ai/dsh`），被当成黑盒对待：
壳只通过 stdout（就绪行）和 HTTP 与它通信。

## 它做什么

- **自举安装**：首次启动自动下载 Node.js 并把 DSH `npm install` 进数据目录，
  带进度条与日志浮层。不需要你装 Node、不需要命令行。
- **托管 Web UI**：无边框窗口经本地反向代理加载 DSH 自己的 Web 界面，
  登录 token 在 Go 侧兑换成会话 cookie——你不会看到浏览器，也不会在地址栏里见到 token。
- **重启服务**：托盘一键重启，端口与 token 自动重新适配。
- **更新检查**：每次启动对比 npm registry；更新失败不影响已装好的旧版本。
- **托盘**：关窗隐藏到托盘，退出是托盘里的一个明确动作。
- **进程安全**：DSH 进程树被严格托管，壳退场后不会留下还在跑的进程。

## 平台支持

| | Windows | macOS | Linux |
|---|---|---|---|
| 产物 | `.exe`（zip）| `.app`（zip，universal）| 可执行文件（tar.gz）|
| 托盘 | 原生 Win32 | `energye/systray`（Cocoa）| `energye/systray`（DBus / StatusNotifierItem）|
| 关窗 | 隐藏到托盘 | 隐藏到托盘 | 隐藏到托盘 |
| 退出 | 托盘 → 退出 | 托盘 → Quit | 托盘 → Quit |
| 进程托管 | Job Object（内核保证）| 进程组 | 进程组 |

几个必须知道的点：

- **Linux / GNOME**：GNOME **默认不显示** StatusNotifierItem 图标，需要装
  *AppIndicator* 扩展，或使用 [snixembed](https://git.sr.ht/~steef/snixembed) 之类的代理；
  KDE 与多数其它桌面开箱即用。为避免「托盘看不见导致应用失联」，壳做了**安全退化**：
  托盘不可用时，关窗直接退出，而不是把窗口藏进一个看不见的托盘。
- **逃生口**：`dsh-desktop --quit` 通知正在运行的实例退出（就是给上面那种场景用的）。
  没有实例在跑时它是无害的空操作。
- **macOS**：构建未做签名/公证。首次打开请右键 → *打开*，或执行
  `xattr -cr /Applications/DSH\ Desktop.app`。
- 刻意**没有**「关闭服务（停 DSH 但留着壳）」这个动作——一个不托管 DSH 的壳没有存在意义，
  取舍理由见 `docs/01-design.md` 的「功能准入」。

## 从源码构建

依赖：Go 1.25+、Node 20+、Wails CLI v2；Linux 还需要 webview 开发包。

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0

# 仅 Linux 需要
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.0-dev

wails build            # 产物：build/bin/dsh-desktop(.exe)
wails doctor           # 环境自检
```

Wails v2 **不支持交叉编译**，请在目标平台上构建。CI 就是这么做的——见
`.github/workflows/`：`test.yml` 在三个系统上跑 `go vet` + `go test ./internal/...`，
`release.yml` 在推送 `v*` tag 时构建并发布产物。

### 图标

所有图标都是生成出来的，不是手画的：

```bash
python build/gen-icons.py    # 需要 numpy + Pillow
```

`build/gen-icons.py` 记录了设计背后的全部实测数值（托盘图标尺寸是量出来的），
并产出 `appicon.png`、`tray.ico`、`windows/icon.ico`、`tray.png`（Linux）与
`tray-template.png`（macOS 单色 template——系统会按菜单栏明暗自动反色）。

## 运行时布局

数据目录优先放在 **exe 旁边**（该位置可写时，即便携模式）；不可写则回退到
`%LOCALAPPDATA%\dsh-desktop-wails`。**它不可配置**——这是有意的
（见 `docs/01-design.md`）：`config.json` 就住在这个目录里，用它来决定自己的位置是循环依赖。

```
<dsh-desktop-data>/
├── config.json          # 壳的配置
├── tray.log             # 唯一可靠的诊断输出（GUI 进程没有 stderr）
└── runtime/
    ├── node/            # 首次启动下载的 Node.js
    └── dsh/             # @deepseek-ai/dsh 的 npm --prefix 安装目录
```

由此带来的实用结论：

- **卸载 = 删掉数据目录**。exe 本身无状态。
- **重置 = 删掉 `runtime/`**——下次启动会重新下载 Node、重装 DSH。
- 你的 DSH 用户数据（插件、会话、凭据）**不在这里**，它在 `DSH_HOME`
  （默认 `~/.dsh`），那个才可以用下面的 `dshHome` 改位置。
  删这个目录绝不会动到你的会话。

## 配置（config.json）

首次运行按默认值生成，之后每次启动都会补齐全部字段并回写。
只在启动时读一次——改完需要重启壳。

| 字段 | 默认值 | 说明 |
|---|---|---|
| `nodeVersion` | `22.20.0` | 首次启动下载的 Node 版本 |
| `nodeDownloadUrl` | `https://nodejs.org/dist/v%s/node-v%s-win-x64.zip` | 地址模板，两处 `%s` 都填版本号 |
| `npmRegistry` | `https://registry.npmjs.org` | `npm install` 与更新检查用的 registry |
| `dshPackage` | `@deepseek-ai/dsh` | 安装的包名 |
| `dshVersion` | `latest` | **仅首次安装**使用的版本 / dist-tag（更新一律用 `latest`）|
| `dshHome` | （空）| 覆盖 `DSH_HOME`——把 DSH 的**整棵**用户数据树（插件、会话、凭据、设置）搬走。用之前先读 `docs/05-configuration.md` 第 5 节：它不会迁移已有数据，且会与未设置 `DSH_HOME` 的其它客户端分裂 |

离线 / 国内镜像：把 `nodeDownloadUrl` 换成
`https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip`，`npmRegistry`
换成 `https://registry.npmmirror.com`。

## 架构要点

- **就绪协议**：DSH 在 stdout 打印 `dsh web: <URL>`（含登录 token），
  监督器只解析这一行（`internal/dsh/supervisor.go`）。
- **cookie-in-proxy**：DSH 的会话 cookie 是 `SameSite=Strict`，跨源 iframe 会被浏览器丢弃。
  壳在 Go 侧用 token 兑换 cookie，经本地反代对每个请求代附（`internal/proxy`）。
- **进程托管**：Windows 用带 kill-on-close 的 Job Object，壳崩溃也不会留孤儿 `node.exe`；
  macOS / Linux 用独立进程组 + `SIGKILL`（`internal/dsh/job_*.go`）。
- **两套托盘实现**：Windows 原生 Win32（消息队列线程私有，必须自己握住整条链路），
  其它平台 `energye/systray`。取舍见 `docs/02-architecture.md` 第 6 节。

## 测试

```bash
go test ./internal/...
```

覆盖的是那些**不允许悄悄烂掉**的部分：就绪行契约、数据目录布局、配置回写行为、
zip 顶层目录剥除、npm 语义版本比较、registry 客户端。

## 文档

| 文档 | 回答的问题 |
|---|---|
| [docs/01-design.md](docs/01-design.md) | 为什么是「轻量套壳」；范围规则；UI / 图标 / 日志规范 |
| [docs/02-architecture.md](docs/02-architecture.md) | 启动时序、就绪协议、cookie 反代、进程托管、托盘 |
| [docs/03-layout.md](docs/03-layout.md) | 源码树 + 运行时数据布局 |
| [docs/04-build-and-run.md](docs/04-build-and-run.md) | 构建与排查 |
| [docs/05-configuration.md](docs/05-configuration.md) | `config.json` 全部字段 |
| [docs/06-dependencies-and-versioning.md](docs/06-dependencies-and-versioning.md) | 依赖分层、DSH 版本跟进机制 |

`AGENTS.md` 是给 AI 编码代理的工作规则。
