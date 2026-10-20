# 06 · 依赖管理与 DSH 版本跟进

这份文档回答两个问题：**壳依赖什么**，以及**DSH 发了新版本，用户怎么跟上**。

## 1. 依赖分三层，生命周期完全不同

| 层 | 内容 | 何时确定 | 产物 |
|---|---|---|---|
| **构建期 Go** | `github.com/wailsapp/wails/v2 v2.15.0`、`golang.org/x/sys` | 编译时静态链接 | 进 exe |
| **构建期前端** | `vite`、`typescript`（仅 `devDependencies`） | 编译时打包 | 编译进 `frontend/dist` → 进 exe |
| **运行期下载** | Node zip + `@deepseek-ai/dsh` | **用户机器上首次启动时** | 落在数据目录 `runtime/` |

关键结论：**前两层是壳自己的事；第三层不是壳的一部分**。DSH 与 Node 不进 exe，
也不随壳发版，这是版本跟进机制能成立的前提。

### 1.1 依赖升级怎么走

| 升级对象 | 做法 | 注意 |
|---|---|---|
| Wails / x/sys | `go get -u` → `wails build` → 手工过一遍窗口/托盘/DPI | Wails 大版本会动 manifest 与 WebView2 加载器，**必须实机验证托盘与窗口**，不要只看编译通过 |
| 前端 vite / typescript | 改 `frontend/package.json` → `npm install` | 与 Wails 模板版本对齐，避免两套构建链行为不一致 |
| Node | 改 `config.json` 的 `nodeVersion` | ⚠️ **只在首装或删掉 `runtime/node/` 后生效**，已有安装不会被替换。要强制升级就删目录重启 |
| DSH | **不需要动壳**，见第 2 节 | — |

## 2. DSH 版本跟进机制（本项目的核心）

### 2.1 为什么能做到"不改壳就升级 DSH"

因为壳与 DSH 的耦合只有两处（就绪行 + HTTP，见
[01-design.md](01-design.md) 第 4 节），且 DSH 是运行时从 npm 装到数据目录的独立进程。
所以：

> **DSH 出新版本 → 用户点一下「更新」 → 壳就地重装 npm 包并重启 DSH。
> 壳本身不需要重新编译、重新分发。**

### 2.2 三条跟进路径

| # | 时机 | 触发 | 用户看到什么 |
|---|---|---|---|
| 1 | 首次安装 | 自举 `EnsureRuntime` 装 `dshVersion`（默认 `latest`） | 直接就是最新版 |
| 2 | **每次启动** | 启动完成后异步 `checkUpdate(false)`（不阻塞界面） | 有新版本弹确认窗：`当前版本 x.y.z，最新版本 a.b.c`，可「立即更新」或「稍后」 |
| 3 | 随时手动 | 托盘菜单「检查更新」→ `CheckUpdate()` | 有新版弹同一个窗；已是最新（或检查失败）由左下角轻提示条显示 3 秒（`update:none`） |

检查逻辑（`internal/update/check.go`）：

```
本地版本  = 读 runtime/dsh/node_modules/@deepseek-ai/dsh/package.json 的 version
上游版本  = GET <npmRegistry>/@deepseek-ai/dsh/latest  （Accept: npm install-v1+json）
是否有更新 = 本地非空 && newer(上游, 本地)
```

- 网络路径按 **直连 → Windows 系统代理 → 直连重试** 依次尝试，每跳独立超时 8s、
  间隔 1s，全挂才报失败（取最后一跳的原因）；registry 已应答（如 404）就不换路，
  直接上报。第二跳的存在理由：Go 的 `net/http` 只认 `HTTPS_PROXY` 环境变量，
  不读 WinINET 系统代理——Clash/V2Ray 用户的代理对壳不可见，而国内直连
  registry 间歇被重置是实测常态（2026-10 实测：直连失败的同时系统代理可达）；
- 失败仍**静默**（只在手动检查时提示「检查更新失败: <原因>」）；
- 本地未安装时不提示（首装归 bootstrap 管，不走更新流程）。

### 2.3 版本比较规则

`newer()` 自己实现，不引依赖：

1. 首尾空白与前缀 `v` 会被剥掉；
2. 形如 `主.次.补丁[-预发布]`，三段按**数值**比较（避免字符串比较下 `0.10.0 < 0.9.0` 这类错误）；
3. 三段相等时：**正式版 > 预发布版**；预发布之间按字符串比较（够用，不追求 semver 完整实现）；
4. 任一侧解析失败则退化为**字符串不等比较**（宁可提示一次更新，也不静默错过）。

### 2.4 一键更新的时序与自愈语义

```
用户点「立即更新」→ ApplyUpdate(ctx)
  1. updating 标志置位（互斥锁保护，重复点击无效）
  2. Supervisor.Stop(StatusUpdating)   停 DSH，回收进程树
  3. bootstrap.UpdateDsh()             node npm-cli.js install @deepseek-ai/dsh@latest --prefix runtime/dsh
     │                                 （子进程 PATH 前置了捆绑 node 目录——koffi 等依赖的
     │                                  install 脚本要再调 node，没有全局 node 的机器全靠它）
     ├─ 成功 → 校验 RuntimeReady（bin.js 在位）
     └─ 失败 → npm reify **可能已把旧包从 node_modules 删掉**（实测会）：
              若 RuntimeReady() 为假 → EnsureRuntime 重装（node 在位，只装 dsh）恢复旧版
              ├─ 重装也失败 → 发 update:progress{failed}，落失败浮层
              └─ 重装成功 → 继续往下走（旧版本复活并重启）
  4. Supervisor.Start()                重启 DSH（新端口、新 token）
     └─ 失败 → 发 update:progress{failed}
  5. 成功后才发 update:progress{done}  → 前端提示「更新完成，DSH 已重启」
  6. wireProxy 重新兑换 cookie → runtime:url → 前端 iframe 刷新到新实例
     （代理端口跨重启不变，URL 同值；`runtime:url` 路径同值也重设 src 强制重载，
     见 [02](02-architecture.md) 第 2 节——不重载则新前端资产不生效）
  7. 再跑一次 checkUpdate(false)
```

设计要点：**更新失败不把用户卡死**——npm 失败后旧包若被 reify 误删，壳会自动重装
把它救回来再重启；`done` 事件只在 DSH 真正重启成功后发出，前端不会看到
「已重启」之后紧跟「运行出错」的自相矛盾提示。

> 为什么是"重装整个包"而不是"热替换"：热替换需要动 DSH 的运行态，越界（见铁律 1）。
> npm 装到独立 `--prefix` 目录；但**不要假设"npm 失败 = 旧目录完好"**——reify 中途
> 失败会删除待替换的旧包，壳靠上面的重装兜底，这正是 3 的失败分支存在的理由。

### 2.5 内网 / 受限网络

改 `config.json` 的 `npmRegistry`（查询与安装共用同一个 registry）即可走镜像，
配置见 [05-configuration.md](05-configuration.md)。禁用自动检查的做法目前**没有开关**，
属于待办功能，不要用"钉住 dshVersion"去变相实现（见 05 第 4 节的说明）。

版本检查另有一层兜底：直连失败会自动换 Windows 系统代理（WinINET 注册表里的
静态代理）再试一轮，见 2.2。两个已知边界：PAC 脚本代理不认（求值需要脚本引擎，
直接跳过）；**npm install 阶段不走这层兜底**——npm 只认环境变量，需要让安装也
走代理时，设 `HTTPS_PROXY` 后再触发更新即可。

## 3. 契约漂移：DSH 变了怎么办

壳与 DSH 的耦合点只有三处，按风险从高到低：

| 耦合点 | 位置 | 失效表现 | 处理 |
|---|---|---|---|
| **就绪行格式** | `internal/dsh/supervisor.go` 的 `readyLine` 正则 | DSH 起得来但壳一直「启动中」，45s 后报超时 | 先手动跑一次 bin.js 看它现在打印什么，**只改正则** |
| **CLI 子命令** | `SupervisorConfig.Args`（`web --no-open --port 0`） | 进程立刻退出，错误信息在浮层日志里 | 对齐新的命令行参数；确认 `--no-open` 与端口参数是否还叫这个名字 |
| **前端 DOM 结构** | `internal/proxy/theme.go` 的注入探针 | 仅影响窗口按钮配色与拖动条位置，**页面照常可用** | 更新探针的读取方式；已有兜底逻辑，失效不阻断 |

**升级 DSH 前的最小回归清单**（三条都过就说明契约没漂）：

1. 启动后 45s 内界面能出内容（就绪行可解析）；
2. 托盘 tooltip 从「启动中…」变成「运行中」（状态链路通）；
3. 右侧窗口按钮配色跟随 DSH 主题、拖动条不遮侧栏折叠按钮（探针还认得 DOM）。

## 4. 什么时候需要重建壳

| 场景 | 要不要重编壳 | 说明 |
|---|---|---|
| DSH 发新版本（前端/后端/功能） | ❌ 不用 | 用户点「更新」即可，这正是本设计的核心目标 |
| DSH 改了就绪行 / CLI 参数 / DOM | ✅ 要 | 契约漂移，改壳的一处常量后发版 |
| 换图标、改窗口行为、改托盘菜单 | ✅ 要 | 壳自身的事 |
| 换 Node 版本 | ❌ 不用（改配置） | 但需删 `runtime/node/` 才会重装 |
| 换镜像源 | ❌ 不用（改配置） | 下次安装/更新生效 |

## 5. 用户视角：怎么确认自己是最新版

- 界面**启动时自动检查**，有新版会弹窗（无需用户主动做任何事）；
- 想主动确认：托盘右键 → 「检查更新」；
- 想知道当前装的是哪个版本：`GetStatus()` 返回的 `localVersion`（读本地 `package.json`），
  或直接看 `<数据目录>/runtime/dsh/node_modules/@deepseek-ai/dsh/package.json`。
