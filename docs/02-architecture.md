# 02 · 封装方式与运行机制

## 1. 组件与数据流

```
┌──────────────────────── dsh-desktop.exe（Wails 壳，Go） ────────────────────────┐
│                                                                                 │
│  main.go ── wails.Run                                                           │
│      │  窗口(无边框) / 单实例锁 / HideWindowOnClose / 生命周期回调                 │
│      ▼                                                                          │
│  app.go ── App（绑定到前端的对象）                                                │
│      ├─ bootstrap.Installer ─┐                                                  │
│      ├─ dsh.Supervisor ──────┤ 状态经 wruntime.EventsEmit 推给前端                │
│      ├─ proxy.Proxy ─────────┤ 状态经 traySetStatus 推给托盘 tooltip              │
│      └─ update.Checker ──────┘                                                  │
│                                                                                 │
│  tray_windows.go ── 原生 Win32 托盘（独立 OS 线程）                                │
│                                                                                 │
│  proxy.Proxy ── 127.0.0.1:<随机端口> ──┐                                          │
└───────────────────────────────────────┼─────────────────────────────────────────┘
                                        │ 反代（附 cookie / 改 Origin）
                                        ▼
                 node.exe dsh/lib/bin.js web --no-open --port 0
                 （DSH 本体，独立进程；挂 Job Object）
                                        │ http://127.0.0.1:<DSH端口>
                                        ▼
                       壳页面 <iframe src="http://127.0.0.1:<代理端口>/">
```

要点：**唯一的显示通路是 iframe → 本地反代 → DSH 的 HTTP 服务**。
壳不解析、不打包 DSH 的任何前端资源。

## 2. 启动时序

```
用户双击 dsh-desktop.exe
  │
  ├─ main.go: wails.Run(OnStartup=app.startup, OnBeforeClose, OnShutdown)
  │
  ├─ app.startup(ctx)
  │    ├─ dsh.ResolveDataDir("")      解析数据目录（exe 旁 → 不可写则 LOCALAPPDATA）
  │    ├─ config.Load(dataDir)        读 config.json，缺字段补默认并回写
  │    ├─ dsh.ResolvePaths(dataDir)   推导 node.exe / npm / dsh bin.js 等路径
  │    ├─ 装配 Installer / Supervisor / Proxy / Checker
  │    ├─ proxy.Start()               在 127.0.0.1 随机端口起反代（此时还没接线）
  │    ├─ go setupTray(app)           托盘线程（首行 LockOSThread）
  │    └─ go bootstrapAndStart()
  │
  └─ bootstrapAndStart()
       ├─ Installer.EnsureRuntime()   幂等自举：
       │     ├─ 已就位 → check/skipped，直接过
       │     ├─ 缺 node.exe → 按 nodeDownloadUrl 下载 zip → 剥顶层目录解压到 runtime/node
       │     └─ 装 dsh → node npm-cli.js install @deepseek-ai/dsh@<版本> --prefix runtime/dsh
       │     （每阶段经 bootstrap:progress 事件上报，前端浮层显示进度）
       ├─ Supervisor.Start(ctx)       起 DSH 子进程，阻塞等就绪（≤45s）
       │     └─ 就绪 → Events.OnURL(url含token) 与 OnStatus(ready)
       ├─ OnURL → app.wireProxy()
       │     ├─ proxy.ExchangeToken(url)  换取会话 cookie（见第 4 节）
       │     ├─ proxy.SetTarget(base, cookies)
       │     └─ EventsEmit("runtime:url", 代理地址)  → 前端设置 iframe.src
       └─ go checkUpdate(false)       异步查 registry，有新版本则弹更新窗
```

前端侧：`GetStatus()` 拉一次全量状态（处理"壳先起、页面后加载"的情况），
其余全部靠 `EventsOn(...)` 推。

**`runtime:url` 意味着「DSH（重）新就绪」**：代理监听端口跨重启不变（壳启动时
一次性建好），重启后 URL 同值，前端在这条路径上**同值也重设 `iframe.src` 强制
重载**——否则 iframe 停在旧实例的页面上，装插件 / 升级 DSH 后的新前端资产不
生效。初始 `GetStatus()` 路径保持同值跳过，避免页面加载时双重导航。

## 3. 就绪协议

DSH 启动后会在 stdout 打印一行就绪信息，壳按行扫描：

```
dsh web: http://127.0.0.1:<port>/?token=<登录令牌>
```

- 解析处：`internal/dsh/supervisor.go`，正则 `^dsh web: (https?://\S+)`；
- 启动参数：`web --no-open --port 0`（`--no-open` 阻止 DSH 自己拉浏览器，
  `--port 0` 让系统分配空闲端口，避免和用户已有实例撞端口）；
- 超时 `defaultReadyTimeout = 45s`，超时即杀进程树并报错；
- 就绪 URL **含 token**，壳不把它透传给前端（前端只用代理地址）。

> 这是壳与 DSH 之间最脆弱的耦合点。DSH 若改了这行输出格式，**只需改 `readyLine`**。

## 4. cookie-in-proxy（为什么要反代）

**问题**：DSH 的浏览器会话用 `SameSite=Strict` 的登录 cookie。壳页面跑在
`wails.localhost`，DSH 服务在 `127.0.0.1:<port>`，两者**必然跨站**；浏览器在跨站 iframe 里
既不存也不发这个 cookie，于是 token 兑换必然失败，页面报
`dsh web authentication required`。

**解法**：cookie 由壳（Go 侧）持有，**不经浏览器**。

```
1. ExchangeToken(就绪URL)   带 ?token= 请求一次，DSH 回 303 + Set-Cookie；
                            壳用 CheckRedirect=ErrUseLastResponse 只收 cookie 不跟跳转
2. SetTarget(上游根, cookies) 之后每个经过反代的请求都：
   - 重写 Host 为上游 authority
   - 删掉浏览器带来的 Cookie，改附壳持有的会话 cookie
   - 把 Origin / Referer 改写为上游 scheme://host
     （DSH 有 Host/Origin 信任围栏：Origin 存在时必须等于 Host）
   - Accept-Encoding 强制 identity，便于注入探针
3. iframe 指向 http://127.0.0.1:<代理端口>/
4. WebSocket 由 httputil.ReverseProxy 原样升级转发（DSH 前端有实时通道）
```

兑换失败时**降级为直连**（`a.OnURL(readyURL)`），至少浏览器场景可用，而不是白屏。

**iframe 的 `allow="clipboard-write"`**：跨站之外，浏览器对 iframe 还有一道
Permissions Policy 闸门——`clipboard-write` 的默认 allow list 是 `self`（顶层
文档），iframe 自身的 secure context 不足以放行。壳页面的 iframe 若不带
`allow="clipboard-write"`，DSH 里所有复制按钮的 `navigator.clipboard.writeText`
会被浏览器**静默拒绝**（API 存在、无报错），表现为「点了复制没反应」。2026-09
实测：同拓扑下缺属性复制必失败、加属性即恢复，故 `frontend/index.html` 的
`<iframe id="dsh-frame">` 固定携带该属性。

**探针注入**：`ModifyResponse` 对 `text/html` 响应注入一段**只读**脚本（插在 `</head>` 前），
它做两件事并通过 `postMessage` 上报给壳：

| 上报 | 用途 |
|---|---|
| `dsh-desktop-theme`（dark/light + 背景色） | 切换壳的窗口按钮配色（iframe 跨源，壳读不到页面） |
| `dsh-desktop-sidebar`（侧栏实际宽度 px） | 移动顶部拖动条，避免遮住 DSH 的侧栏折叠按钮 |

注入以 `dshDesktopThemeProbe` 标记去重；`Content-Length`/`Content-Encoding` 会在注入后清掉；
**任何一步失败都直接放弃注入，不影响页面返回**。

除上报外，脚本还按「侧栏右侧、贴顶通高的最外层列」做**列让位**，为 32px 拖动条腾空间：
文档流列（中列）补 `padding-top: 32px`；绝对定位的右栏面板（`top:0` 钉在列的
padding-box 上，祖先的 padding 推不动它）改它自己的内联 `top`（它自带 `bottom:0`，
top 下移即整体收缩）。定位为 `fixed` 的一律不动（弹层与右栏全屏态）。

## 5. 进程树回收

- 起 DSH 前创建 Job Object，设 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，把 DSH 进程挂进去
  （`internal/dsh/job_windows.go`）；
- 停止 = `TerminateJobObject` 杀整棵树，再等 `cmd.Wait`（超时 `defaultStopTimeout = 8s`）；
- 壳进程被杀 / 崩溃时，Job 句柄随进程关闭 → **操作系统自动回收所有子进程**，
  不会留孤儿 `node.exe`；
- 用 `CREATE_NO_WINDOW` 起子进程，避免 GUI 父进程派生控制台子进程时闪黑窗。

**macOS / Linux 用进程组代替**（`internal/dsh/job_unix.go`）：子进程以 `Setpgid: true`
自成一个进程组，`terminate` 就是 `kill(-pgid, SIGKILL)`。能力差异要记住：

| | Windows（Job Object）| macOS / Linux（进程组）|
|---|---|---|
| 正常停止 / 重启 | 杀整棵树 | 杀整棵树（等价）|
| 壳被 SIGKILL / 崩溃 | 内核自动回收 | **子树成为孤儿**（进程组只在主动 terminate 时生效）|

Linux 的 `Pdeathsig` 看似能补齐，但它按「父**线程**」判定，而 Go 运行时会销毁线程，
可能把活着的 DSH 误杀——所以**不用**（见 `job_unix.go` 顶部注释）。

## 6. 托盘：Windows 原生 Win32，macOS/Linux 用 energye/systray

原实现（全平台）用第三方 `energye/systray`，Windows 上托盘时好时坏。根因：

> Windows 的消息队列是**线程私有**的，`GetMessage(NULL, ...)` 只取调用线程的消息。
> systray 这类库在同一个 goroutine 里先创建窗口、再跑消息循环，但**从不
> `runtime.LockOSThread`**；Go 调度器一旦把这个 goroutine 挪到别的 OS 线程，
> 消息循环就停在一条永远收不到消息的队列上——表现是「图标在、点什么都没反应」。
> 更糟的是库只用 `log.Printf` 报错，而 GUI 进程没有 stderr，失败完全不可见。

所以三端现在是**两套实现**，这是个有意的不对称：

| 平台 | 实现 | 为什么 |
|---|---|---|
| Windows | `tray_windows.go`，原生 Win32 | 上述竞态必须自己握住整条链路才能根除 |
| macOS / Linux | `tray_unix.go`，`energye/systray` | 没有线程私有消息队列的问题；库的 darwin(Cocoa) / linux(StatusNotifierItem+DBus) 实现成熟，自己手写 cgo/DBus 在无法真机验证的前提下风险更大；Linux 侧还是**纯 Go**（该 fork 移除了 GTK 依赖），唯一依赖 `godbus/dbus/v5` 本来就在 go.mod 里 |

Windows 侧自己持有整条链路：

| 措施 | 解决什么 |
|---|---|
| 托盘 goroutine **第一行** `runtime.LockOSThread()` | 窗口创建与消息循环同线程，从结构上消除上述竞态 |
| 隐藏顶层窗口（`WS_POPUP` + `WS_EX_TOOLWINDOW`，永不 Show） | 不进任务栏 / Alt+Tab，但仍能收到 `TaskbarCreated` 广播 |
| `TrackPopupMenu` 用 `TPM_RETURNCMD` | 阻塞回调期间不依赖 `WM_COMMAND`，选中项直接由返回值拿到 |
| 菜单动作全部丢独立 goroutine | 重启要等子进程退出，同步执行会饿死消息循环 |
| `CreateIconFromResourceEx` 直接吃内存 ICO | 不落临时文件；失败回退「临时文件 + LoadImage」 |
| 全流程写 `<数据目录>/tray.log` | 失败可查（`teeWriter`，不用 `io.MultiWriter`） |
| 按真实 DPI 选图标尺寸 | 非 DPI-aware 进程的 `SM_CXSMICON` 会被虚拟化成 16，此时取 32px 交给系统缩小 |

### 跨平台托盘的几个平台事实

- **图标尺寸**：库在 darwin 侧把 `NSImage` 强制设成 16×16 **点**，所以 macOS 的
  `tray-template.png` 给 32×32 像素（16pt @2x）才不会糊；Linux 走彩色 `tray.png`。
  `SetTemplateIcon(template, color)` 一次调用两个平台都对——darwin 只取第 1 个参数
  并打 template 标记，linux 只取第 2 个参数。
- **事件循环**：Wails 占着主线程，所以必须用 `RunWithExternalLoop`（返回 start/end），
  不能用会阻塞的 `Run()`。
- **Linux 上「登记成功 ≠ 用户可见」**：GNOME 默认不显示 StatusNotifierItem，需要
  AppIndicator 扩展或 snixembed 之类的代理。为此壳做了一个**安全退化**：`trayAlive()`
  报告托盘是否已登记，托盘不可用时关窗从「隐藏到托盘」退化成「退出」——
  宁可行为不一致，也不留一个唤不回的窗口。另提供 `dsh-desktop --quit` 作为
  命令行逃生口（经单实例锁通知运行中的实例退出）。

## 7. 状态机与事件

DSH 状态（`internal/dsh` 的 `Status`）：`stopped` / `starting` / `ready` / `restarting` /
`updating` / `error`。

`Supervisor` 的并发语义：`opMu` 把 `Start` / `Stop` / `Restart` 串行化，
**并发重启请求会被合并成一轮完整重启**（stop → start → 重新兑换 cookie → 前端刷新 iframe）。

壳 → 前端的事件（`frontend/src/main.ts` 全部有接线）：

| 事件 | 载荷 | 前端行为 |
|---|---|---|
| `runtime:status` | `{status, detail}` | `status=error` 时自动弹出浮层、回放日志缓冲并显示重试按钮（DSH 的失败诊断在 stdout 里，壳必须把现场呈现出来，不能让用户面对无声的白屏） |
| `runtime:url` | URL 字符串 | 设置 `iframe.src`（同值也重设=强制重载，见第 2 节），隐藏浮层并清空日志缓冲 |
| `runtime:log` | 一行日志 | 追加进 400 行环形缓冲；浮层可见时同时追加到日志区（浮层未开时只缓冲，弹开时整体回放） |
| `bootstrap:progress` | `{phase, detail, downloaded, total, indeterminate}` | 进度条 / 失败显示重试 |
| `update:available` | `{local, latest}` | 弹更新确认窗 |
| `update:progress` | 同上 | 更新进度（浮层 + toast）；失败提示"已回退旧版本" |
| `update:none` | 文案 | 左下角轻提示条（toast）显示 3 秒：「已是最新版本 x」或「检查更新失败: …」 |
| `update:progress`（下载中） | 同上 | 除浮层外，左下角 toast 同步显示下载进度（百分比 + MB，sticky 常驻直到 done/failed） |
| `window:maximised` | bool | 切换最大化/还原图标 |

前端可调用的绑定方法：`GetStatus` / `RetryBootstrap` / `ApplyUpdate` / `WindowMin` /
`WindowToggleMax` / `WindowHideToTray`；`RestartDSH` / `StopDSH` / `CheckUpdate` / `QuitApp`
由托盘调用（同样绑定了，前端未用）。
