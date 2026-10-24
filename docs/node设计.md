# pkgmirror node 安装包镜像设计

`download node` 下载 Node.js 官方发行版的可移植归档与校验清单。服务端按 nodejs.org/dist 目录布局分发，版本管理器通过镜像地址接入。

## 1. 使用方式

1. 外网机按需下载指定 Node 版本的可移植归档（Linux/macOS tar.xz、
   Windows zip）与该版本 `SHASUMS256.txt`，U 盘拷入内网一键上传；
2. 内网服务端按官方 dist 布局（`/mirror/node/vX.Y.Z/`）提供文件，版本管理器与
   浏览页均可直接使用；
3. 内网机器可解压归档配 PATH 直接运行（便携式，无需安装器）；
4. 上传通过 Basic auth 认证，重复上传相同文件按哈希跳过。

## 2. 发行索引与文件布局

- `{dist}/index.json` 包含版本、files 平台矩阵、lts 代号和内置 npm 版本；
- 服务端按 dist 布局提供静态文件，版本管理器按
  `{mirror}/{version}/node-vX-...` 与 `{mirror}/{version}/SHASUMS256.txt`
  请求；
- **文件名与标识的平台命名不一致**：index.json 的 files 标识用
  `osx-*`/`win-*`，实际文件名 os 段是 `darwin`/`win`，linux 一致；
  macOS 标识带 `-tar` 后缀（区分 pkg），Windows 带 `-zip`（区分 exe/msi/7z）。

## 3. 命令行

```
pkgmirror download node <版本>... [--lts] [--platform linux] [--arch x86_64]
                      [--dist-url URL] [--proxy URL] [-o 目录]
```

- 版本形式：`22`（主版本最新 patch）、`22.11`（该 minor 最新 patch）、
  `22.11.0`（精确）；`--lts` 下载当前最新 LTS（`lts` 字段非 false 的最大
  版本），与版本参数互斥；
  版本与 `--lts` 至少给一个；
- `--platform` 取值 `linux,darwin,windows`（macos 为 darwin 别名），`--arch` 取值 `x86_64,aarch64`（amd64/arm64 为别名）；默认下载三种系统的两种架构；
- 平台映射（files 标识 → 文件名）：

  | --platform | --arch x86_64 | --arch aarch64 |
  | --- | --- | --- |
  | linux | `linux-x64` → `node-vX-linux-x64.tar.xz` | `linux-arm64` → `...linux-arm64.tar.xz` |
  | macos | `osx-x64-tar` → `node-vX-darwin-x64.tar.xz` | `osx-arm64-tar` → `...darwin-arm64.tar.xz` |
  | windows | `win-x64-zip` → `node-vX-win-x64.zip` | `win-arm64-zip` → `...win-arm64.zip` |

  按整段标识精确匹配可移植归档，筛选结果为空时报错；
- `--dist-url` 三级来源：命令行 > client.toml `node-dist-url` >
  `NODE_DIST_URL` > `https://nodejs.org/dist`（国内可配
  `https://cdn.npmmirror.com/binaries/node`）；
- 每个目标版本额外下载 `{dist-url}/vX.Y.Z/SHASUMS256.txt`，归档按其中
  对应行校验 sha256 后落盘；
- `--proxy` 指定代理，`-o` 指定产出目录，manifest 与目录内已有清单增量合并。

## 4. 服务端

### 4.1 存储布局（root 目录）

```
data/node/{vX.Y.Z}/
    ├── node-vX.Y.Z-{os}-{arch}.tar.xz|.zip
    └── SHASUMS256.txt
```

上传文件后生成同名 `.sha256` 缓存。

### 4.2 路由

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/browse/node/` | Node 安装包浏览页：版本列表（版本、文件、大小） |
| GET | `/mirror/node/{vX.Y.Z}/{file}` | 文件下载；404 返回提示页"外网执行 download node <版本> 补同步" |

- 版本目录名校验 `^v\d+\.\d+\.\d+$`，文件名按 `fileRe` 白名单校验；
- 导航 Node.js 组包含“安装包”入口；首页展示 Node 版本数与版本管理器接入配置。

### 4.3 上传接口

`/api/upload` 接收以下 multipart 表单字段：

| 字段 | 说明 |
| --- | --- |
| `type` | `node` |
| `tag` | 版本号 `vX.Y.Z`（与 python 的 8 位构建标签同字段、不同格式，各自校验） |
| `sha256` | 期望哈希，可省略；提供时落盘前校验 |
| `file` | 归档或 SHASUMS256.txt 文件内容 |

落盘 `node/{tag}/{filename}`，幂等与 409 复用 `storeUpload`。同一版本只
同步部分平台时，SHASUMS256.txt 含其他平台的行，不影响工具按行查找校验。

## 5. manifest 字段

`File.Type` 为 `"node"`，`Tag` 为版本号 `vX.Y.Z`。上传客户端将类型、版本号、文件名与哈希写入表单，服务端据此确定落盘路径。

## 6. 内网接入（版本管理器）

管理器镜像根指向 `/mirror/node`，首页按请求 Host 生成接入配置：

```bash
export NVM_NODEJS_ORG_MIRROR=http://user:pass@host:19999/mirror/node   # nvm
export N_NODE_MIRROR=http://user:pass@host:19999/mirror/node           # n
export MISE_NODE_MIRROR_URL=http://user:pass@host:19999/mirror/node    # mise（内嵌凭据未确认）
```

```powershell
nvm node_mirror http://host:19999/mirror/node/                          # nvm-windows（写入 settings.txt）
```

各管理器随后 `nvm install 22` / `n 22` / `nvm use 22` / `mise use -g node@22`
即从镜像下载并按 SHASUMS256.txt 校验。fnm 同布局可用（`FNM_NODE_DIST_MIRROR`），
Volta 不支持自定义镜像，其用户走浏览页手工下载解压。

## 7. 验证

- 单元测试：index.json 解析与目标选择（主版本最新 patch / minor / 精确 /
  --lts / 无目标报错）、平台→标识→文件名映射（darwin 命名差异、musl 与
  7z/exe 排除、缺失标识报错）、SHASUMS256.txt 解析与校验、上传 type=node
  的版本格式与路径校验、路由与浏览页数据；
- 集成冒烟（真实网络）：`download node 22 --platform linux` → upload →
  curl 校验 `/mirror/node/vX/` 归档与 SHASUMS256.txt → 有 nvm 环境时设
  `NVM_NODEJS_ORG_MIRROR` 实测 `nvm install` 成功；重复 upload 幂等；
- 构建：运行 `go build ./...`，跨平台构建使用项目脚本。

## 8. 接入限制

1. **管理器凭据差异**：nvm（curl）与 n 明确支持 URL 内嵌凭据；nvm-windows
   与 mise 对内嵌凭据未确认，启用 Basic auth 后若返回 401，检查管理器的
   认证配置，或登录浏览页手工下载。
2. **归档体积**：单版本全平台约 150MB（6 归档），按需 `--platform` 控制。
3. **--lts 语义**：取当前最大 LTS 版本，不区分代号线；需要特定 LTS 线
   （如 Jod）时直接给主版本号。
