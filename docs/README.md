# DSH Desktop（dsh-desktop-wails）文档

DSH（DeepSeek Harness）的**轻量桌面套壳**。Golang + Wails v2，单个 exe。

这个仓库**不含 DSH 的源码**，也不对 DSH 做任何修改。它做的事情只有四件：
把 DSH 装起来、跑起来、托管住、把它的 Web UI 显示出来。

## 阅读顺序

| 文档 | 回答的问题 |
|---|---|
| [01-design.md](01-design.md) | 为什么是「轻量套壳」？边界在哪？UI/图标/日志有哪些必须遵守的规范？ |
| [02-architecture.md](02-architecture.md) | 壳和 DSH 到底怎么拼起来的？启动时序、就绪协议、cookie 反代、进程回收 |
| [03-layout.md](03-layout.md) | 源码怎么放？运行时数据目录长什么样？ |
| [04-build-and-run.md](04-build-and-run.md) | 怎么构建、怎么启动、出问题先看哪里？ |
| [05-configuration.md](05-configuration.md) | `config.json` 每个字段什么含义、怎么换镜像？ |
| [06-dependencies-and-versioning.md](06-dependencies-and-versioning.md) | 依赖分几类？**DSH 发新版本了，用户怎么跟上？** |

给 AI agent 的操作说明在仓库根目录的 [`../AGENTS.md`](../AGENTS.md)。

## 一句话架构

```
dsh-desktop.exe (Wails 壳，Go)
├── bootstrap   首次启动：下载 Node + npm install @deepseek-ai/dsh 到数据目录
├── supervisor  以独立进程跑 `node dsh/lib/bin.js web`，解析 stdout 就绪行
│               └── Job Object(kill-on-close)：壳一退出，DSH 进程树自动回收
├── proxy       127.0.0.1 随机端口反代：持有会话 cookie、注入只读探针
│               └── 壳页面的 iframe 一律指向它，而不是 DSH 直连地址
└── tray        原生 Win32 托盘：关窗隐藏、菜单重启/停止/检查更新/退出
```

关键点：**DSH 是一个独立的外部进程，不是被编译进壳里的模块**。所以桌面框架层的任何问题，
都不会影响 DSH 本身能不能跑（详见 [01-design.md](01-design.md)）。
