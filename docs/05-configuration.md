# 05 · 配置项

## 1. 位置与写回规则

配置文件是数据目录下的 `config.json`（数据目录怎么定位见 [03-layout.md](03-layout.md) 第 2 节）。

- **首次运行自动生成**，含全部可配置项与默认值；
- **每次启动都会读取并回写**（`config.Load` 补齐全缺省字段后 `MarshalIndent` 写回），
  所以你手改后不必担心漏项，错误也容易被肉眼发现；
- 解析失败**不致命**：壳会记一条错误日志并使用内置默认值继续启动（不会因为配置写坏而打不开）；
- 所有配置**只在启动时读一次**，改完需要重启壳（托盘「退出」后重新打开）。

## 2. 字段一览

| JSON 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `nodeVersion` | string | `22.20.0` | 自举下载的 Node 版本，填进 `nodeDownloadUrl` 模板 |
| `nodeDownloadUrl` | string | `https://nodejs.org/dist/v%s/node-v%s-win-x64.zip` | Node zip 地址模板，**两处 `%s` 都填版本号** |
| `npmRegistry` | string | `https://registry.npmjs.org` | `npm install` 用的 `--registry`；同时决定更新检查查询的 registry |
| `dshPackage` | string | `@deepseek-ai/dsh` | 安装的 npm 包名 |
| `dshVersion` | string | `latest` | **首次安装**使用的版本或 dist-tag；空串按 `latest` 处理。见第 4 节的重要区别 |
| `dshHome` | string | `""`（空） | 非空时作为 `DSH_HOME` 环境变量传给 DSH 子进程，**搬的是 DSH 的全部用户数据**（插件及其依赖、会话、凭据、设置、皮肤）；空表示不覆盖、沿用系统默认 `~/.dsh`。详见第 5 节 |

> **没有 `dataDir`。** 壳自己的数据目录（`runtime/` 那几百 MB）位置**不可配置**，
> 由 exe 位置与可写性决定，见 [03](03-layout.md) 第 2 节与本文第 6 节。

## 3. 国内 / 内网镜像示例

```json
{
  "nodeVersion": "22.20.0",
  "nodeDownloadUrl": "https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip",
  "npmRegistry": "https://registry.npmmirror.com",
  "dshPackage": "@deepseek-ai/dsh",
  "dshVersion": "latest",
  "dshHome": ""
}
```

改掉 `nodeDownloadUrl` 与 `npmRegistry` 这两个字段即可全程不访问公网（Node zip 与 npm 包都走镜像）。

## 4. `dshVersion` 与更新机制的边界（容易误解）

| 动作 | 实际使用的版本 |
|---|---|
| 首次安装 / 删掉 `runtime/dsh` 后重装 | `dshVersion` 指定的版本或 dist-tag |
| 托盘「检查更新」、启动时自动检查 | 一律对比 registry 的 **`latest`** |
| 用户确认更新（`ApplyUpdate` → `UpdateDsh`） | **硬编码 `@latest`**，不接受 `dshVersion` 干预 |

推论（重要）：

- 把 `dshVersion` 钉在旧版本**不能阻止**壳提示有新版——检查逻辑比的是
  「本地版本 vs registry latest」，钉住旧版只会让提示一直出现；
- 想真正停在某个版本，就不要点更新；万一被更新了，改回 `dshVersion` 并**删掉
  `runtime/dsh/`** 重启，会按指定版本重装（见 [04](04-build-and-run.md) 第 6 节的回退办法）。

如果你需要"锁定版本且不提示更新"的行为，那是**功能改动**，要先读
[01-design.md](01-design.md) 确认边界后再动 `internal/update`。

## 5. `dshHome`：DSH 用户数据的落点（唯一与「位置」有关的配置项）

非空时，壳把它作为 `DSH_HOME` 环境变量传给 DSH 子进程（见 `app.go`）。

**搬的是整棵树。** DSH 侧由 `@deepseek-ai/dsh-home-paths` 解析主目录，它的设计原则是
「harness 的所有用户数据都位于同一个根目录下」，所以**没有**「插件放这、会话放那」的
单目录粒度：

| 一起搬走的 | 说明 |
|---|---|
| `profiles/` | 插件及其依赖（pnpm 装），体积大头，实测 GB 级 |
| `sessions/` | 会话记录 |
| `.credentials.yaml` | 登录凭据 |
| `settings.yaml` | 设置 |
| `skins/`、`attachments/`、`storages/` 等 | 皮肤、附件与其它本地存储 |

**解析优先级**（DSH 侧）：显式配置 > `$DSH_HOME` > `~/.dsh`；空或纯空白的 `$DSH_HOME`
视为未设置。所以本字段填 `""` 就是「不覆盖」，与 DSH 的语义天然对齐。

注意事项：

- **不会自动迁移。** 设了新路径后旧 `~/.dsh` 原样留在原地，DSH 会在新位置从零初始化：
  插件要重装、会话看不到、可能要重新登录。想保住存量就先把 `~/.dsh` 整份拷过去再改配置。
- **会与其它 DSH 客户端分裂。** 未设 `DSH_HOME` 的客户端（例如 Electron 版桌面端）仍用
  `~/.dsh`，于是两边各持一套会话与插件。**想共享就别设。**
- **填绝对路径。** 相对路径按子进程的工作目录解析，容易出意外。
- **怎么确认生效**：DSH 面向用户展示主目录时会把配置过的 home 渲染成 `$DSH_HOME`
  而不是真实绝对路径（它有意不泄露机器路径）——界面上显示成符号即说明吃上了。

## 6. 壳的数据目录位置不可配置（有意为之）

壳自己的数据目录（`config.json`、`tray.log`、`runtime/node`、`runtime/dsh`）**没有**配置项，
位置完全由「exe 在哪 + 那个目录能不能写」决定，见 [03](03-layout.md) 第 2 节。

曾有一个 `dataDir` 字段声称可以强制指定，但它从未接进 `ResolveDataDir`（而且 `config.json`
本身住在数据目录里，自我指涉是循环依赖）。现按「只做套壳，不做多余功能」把该字段
**从代码与文档里一并删除**，理由与已裁功能清单见 [01-design.md](01-design.md) 的「功能准入」。

两者别混淆：

| | 壳的数据目录 | DSH_HOME（由 `dshHome` 决定）|
|---|---|---|
| 装什么 | 自举下载的 Node 与 DSH 本体 | 插件、会话、凭据、设置、皮肤 |
| 删了会怎样 | 下次启动重新下载，**可再生** | **数据丢失** |
| 位置 | 由 exe 位置决定，不可配 | 由 `dshHome` 决定 |

想省 C 盘空间，该动的是 `dshHome`（用户数据通常才是大头）。
