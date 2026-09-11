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
| `dshHome` | string | `""`（空） | 非空时作为 `DSH_HOME` 环境变量传给 DSH 子进程；空表示不覆盖、沿用系统值 |
| `dataDir` | string | `""`（空） | 强制指定数据目录；空表示自动探测（exe 旁 → 不可写则 `%LOCALAPPDATA%`） |

## 3. 国内 / 内网镜像示例

```json
{
  "nodeVersion": "22.20.0",
  "nodeDownloadUrl": "https://npmmirror.com/mirrors/node/v%s/node-v%s-win-x64.zip",
  "npmRegistry": "https://registry.npmmirror.com",
  "dshPackage": "@deepseek-ai/dsh",
  "dshVersion": "latest",
  "dshHome": "",
  "dataDir": ""
}
```

改完这两个字段即可全程不访问公网（Node zip 与 npm 包都走镜像）。

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
