# 03 · 目录结构

## 1. 源码树

```
dsh-desktop-wails/
├── AGENTS.md                 给 AI agent 的操作说明
├── README.md                 面向用户/开发者的简要说明
├── wails.json                Wails 构建配置（产物名、前端安装/构建命令）
├── go.mod / go.sum           Go 依赖
│
├── main.go                   Wails 装配：窗口参数、单实例锁、生命周期回调
├── app.go                    绑定层：串起 internal 各包，向前端/托盘转发状态
├── tray_windows.go           原生 Win32 托盘（仅 windows 构建）
│
├── internal/
│   ├── config/config.go      config.json 读写、默认值、Sanitize
│   ├── bootstrap/
│   │   ├── bootstrap.go      自举编排：EnsureRuntime / UpdateDsh / npmInstall
│   │   ├── download.go       HTTP 下载（带进度、断点语义）
│   │   ├── zip.go            解压并剥掉 zip 单一顶层目录
│   │   └── hidewindow_*.go   子进程不弹控制台窗口（windows / 其它平台）
│   ├── dsh/
│   │   ├── paths.go          数据目录解析 + 各子路径推导
│   │   ├── supervisor.go     DSH 进程监督器：启动/就绪探测/停止/重启合并
│   │   ├── job_windows.go    Job Object(kill-on-close) 进程树回收
│   │   └── hidewindow_*.go   同上
│   ├── proxy/
│   │   ├── proxy.go          持有会话 cookie 的反代 + Director/ModifyResponse
│   │   ├── theme.go          注入只读探针（主题 + 侧栏几何上报）
│   │   └── listen.go         127.0.0.1 随机端口监听
│   └── update/check.go       查 npm registry dist-tag latest 并对比本地版本
│
├── frontend/                 壳页面（Vite + 原生 TypeScript）
│   ├── index.html            骨架：iframe + 拖动条 + 自绘窗口按钮 + 浮层
│   ├── package.json          仅 devDependencies（vite / typescript），无运行时依赖
│   ├── package-lock.json
│   ├── tsconfig.json
│   ├── src/main.ts           事件接线、浮层、窗口按钮、postMessage 处理
│   ├── src/style.css
│   ├── wailsjs/              Wails 生成的 Go 绑定（**勿手改**，构建时重生成）
│   ├── node_modules/         依赖安装目录（勿提交）
│   └── dist/                 前端构建产物（被 Go 侧 go:embed 进 exe，勿提交）
│
├── build/                    图标、清单、安装器模板（→ build/README.md）
│   ├── appicon.png           1024 应用图标源
│   ├── tray.ico              托盘图标（16/20/24/32/48）
│   ├── dsh-logo.svg          鲸鱼矢量源文件
│   ├── windows/
│   │   ├── icon.ico          exe / 窗口图标（16…256）
│   │   ├── info.json         版本信息（exe 属性面板）
│   │   ├── wails.exe.manifest  per-monitor-v2 DPI 感知 + Common Controls 6
│   │   └── installer/*.nsi   NSIS 安装器模板
│   ├── darwin/               macOS 打包 plist（当前不构建 mac，保留模板）
│   └── bin/                  **构建产物目录，勿提交**
│       ├── dsh-desktop.exe
│       └── dsh-desktop-data/ ← 便携模式下的运行时数据目录（见第 2 节）
│
└── docs/                     本文档目录
```

### 关于 `frontend/wailsjs/`

Wails 构建时根据 `Bind: []interface{}{app}` 自动生成 Go 方法的 TS 绑定。
它是**生成物**，改 Go 侧绑定后由 `wails build` / `wails dev` 重新生成，不要手改。

## 2. 数据目录（运行时）

DSH 的运行时**不在仓库里**，也不在 exe 内部——它在数据目录里，首次启动时下载安装。

**解析顺序**（`internal/dsh/paths.go` 的 `ResolveDataDir`）：

1. `config.json` 的 `dataDir`（非空则直接用）；
2. exe 同级的 `dsh-desktop-data/`（**便携模式**，可写时采用，exe 改名不影响这个名字）；
3. `%LOCALAPPDATA%\dsh-desktop-wails\`（上面不可写时兜底，例如装在 Program Files）。

```
<数据目录>/
├── config.json             用户配置（每次启动补齐缺省字段后回写，见 docs/05）
├── tray.log                壳的托盘/窗口日志（唯一可靠的诊断入口）
└── runtime/
    ├── node/               自举下载的 Node，解压后剥掉顶层目录
    │   ├── node.exe
    │   ├── node_modules/npm/bin/npm-cli.js   ← 壳就是直接跑这个装 DSH
    │   └── ...
    └── dsh/                npm --prefix 安装目标
        └── node_modules/@deepseek-ai/dsh/
            ├── package.json          读本地版本用（版本跟进）
            └── lib/bin.js            ← 壳启动它：node bin.js web --no-open --port 0
```

要点：

- **卸载 = 删掉数据目录**（连同 config 一起）。exe 本身无状态。
- `runtime/` 可以整个删掉重建，下次启动会重新自举——这是恢复"环境搞坏了"的兜底手段。
- Node 的下载临时文件 `runtime/node-download.zip` 在解压后即删除。
- 数据目录**不要提交**（`.gitignore` 已覆盖 `dsh-desktop-data/`）。

## 3. 构建产物布局

```
build/bin/
├── dsh-desktop.exe        交付物：单个 exe（前端已 go:embed 进去）
└── dsh-desktop-data/      仅当从 build/bin 直接运行时才会生成（便携模式）
```

> 因此**把 exe 拷给别人时只需要那一个文件**；数据目录会在首次运行时自动创建在 exe 旁边。
> 如果 exe 放在不可写位置（如 `C:\Program Files`），运行时会自动落到 `%LOCALAPPDATA%`。
