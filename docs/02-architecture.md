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

**探针注入**：`ModifyResponse` 对 `text/html` 响应注入一段**只读**脚本（插在 `</head>` 前），
它做两件事并通过 `postMessage` 上报给壳：

| 上报 | 用途 |
|---|---|
| `dsh-desktop-theme`（dark/light + 背景色） | 切换壳的窗口按钮配色（iframe 跨源，壳读不到页面） |
| `dsh-desktop-sidebar`（侧栏实际宽度 px） | 移动顶部拖动条，避免遮住 DSH 的侧栏折叠按钮 |

注入以 `dshDesktopThemeProbe` 标记去重；`Content-Length`/`Content-Encoding` 会在注入后清掉；
**任何一步失败都直接放弃注入，不影响页面返回**。

## 5. 进程树回收（Job Object）

- 起 DSH 前创建 Job Object，设 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，把 DSH 进程挂进去
  （`internal/dsh/job_windows.go`）；
- 停止 = `TerminateJobObject` 杀整棵树，再等 `cmd.Wait`（超时 `defaultStopTimeout = 8s`）；
- 壳进程被杀 / 崩溃时，Job 句柄随进程关闭 → **操作系统自动回收所有子进程**，
  不会留孤儿 `node.exe`；
- 用 `CREATE_NO_WINDOW` 起子进程，避免 GUI 父进程派生控制台子进程时闪黑窗。

## 6. 托盘为什么是原生 Win32 实现

原实现用第三方 `energye/systray`，托盘时好时坏。根因：

> Windows 的消息队列是**线程私有**的，`GetMessage(NULL, ...)` 只取调用线程的消息。
> systray 这类库在同一个 goroutine 里先创建窗口、再跑消息循环，但**从不
> `runtime.LockOSThread`**；Go 调度器一旦把这个 goroutine 挪到别的 OS 线程，
> 消息循环就停在一条永远收不到消息的队列上——表现是「图标在、点什么都没反应」。
> 更糟的是库只用 `log.Printf` 报错，而 GUI 进程没有 stderr，失败完全不可见。

现在自己持有整条链路（`tray_windows.go`）：

| 措施 | 解决什么 |
|---|---|
| 托盘 goroutine **第一行** `runtime.LockOSThread()` | 窗口创建与消息循环同线程，从结构上消除上述竞态 |
| 隐藏顶层窗口（`WS_POPUP` + `WS_EX_TOOLWINDOW`，永不 Show） | 不进任务栏 / Alt+Tab，但仍能收到 `TaskbarCreated` 广播 |
| `TrackPopupMenu` 用 `TPM_RETURNCMD` | 阻塞回调期间不依赖 `WM_COMMAND`，选中项直接由返回值拿到 |
| 菜单动作全部丢独立 goroutine | 重启/停止要等子进程退出，同步执行会饿死消息循环 |
| `CreateIconFromResourceEx` 直接吃内存 ICO | 不落临时文件；失败回退「临时文件 + LoadImage」 |
| 全流程写 `<数据目录>/tray.log` | 失败可查（`teeWriter`，不用 `io.MultiWriter`） |
| 按真实 DPI 选图标尺寸 | 非 DPI-aware 进程的 `SM_CXSMICON` 会被虚拟化成 16，此时取 32px 交给系统缩小 |

## 7. 状态机与事件

DSH 状态（`internal/dsh` 的 `Status`）：`stopped` / `starting` / `ready` / `restarting` /
`updating` / `error`。

`Supervisor` 的并发语义：`opMu` 把 `Start` / `Stop` / `Restart` 串行化，
**并发重启请求会被合并成一轮完整重启**（stop → start → 重新兑换 cookie → 前端刷新 iframe）。

壳 → 前端的事件（`frontend/src/main.ts` 全部有接线）：

| 事件 | 载荷 | 前端行为 |
|---|---|---|
| `runtime:status` | `{status, detail}` | 目前只保留接线（状态点已从工具栏移除） |
| `runtime:url` | URL 字符串 | 设置 `iframe.src`，隐藏浮层 |
| `runtime:log` | 一行日志 | 浮层日志区追加（仅浮层可见时） |
| `bootstrap:progress` | `{phase, detail, downloaded, total, indeterminate}` | 进度条 / 失败显示重试 |
| `update:available` | `{local, latest}` | 弹更新确认窗 |
| `update:progress` | 同上 | 更新进度；失败提示"已回退旧版本" |
| `update:none` | 文案 | 目前前端未接线（托盘手动检查时由托盘侧兜底） |
| `window:maximised` | bool | 切换最大化/还原图标 |

前端可调用的绑定方法：`GetStatus` / `RetryBootstrap` / `ApplyUpdate` / `WindowMin` /
`WindowToggleMax` / `WindowHideToTray`；`RestartDSH` / `StopDSH` / `CheckUpdate` / `QuitApp`
由托盘调用（同样绑定了，前端未用）。
