# pkgmirror Go 镜像设计

`download golang` 下载 Go 官方发行归档，`download gomod` 按 go.sum 或模块引用下载 `.mod`、`.zip`、`.info` 文件。服务端按官方文件布局分发归档，通过 module proxy 协议提供模块文件、版本列表和最新版本信息。

## 1. 使用方式

1. 内网服务端提供 `dl.google.com/go` 同布局的 Go 安装包镜像（flat 目录），
   mise / asdf-golang 换镜像变量直接可用，浏览页可手工下载；
2. 内网服务端提供 module proxy 协议子集（`.mod`/`.zip`/`.info` 静态文件 +
   `@v/list`/`@latest` 目录扫描合成），内网 `GOPROXY` 指向镜像后
   `go build` / `go mod download` / `go install pkg@latest` 直接可用；
3. 外网机按需下载指定 Go 版本的发行归档与项目 go.sum 对应的模块闭包，
   U 盘拷入内网一键上传；
4. 上传通过 Basic auth 认证，相同哈希的文件重复上传时跳过。

内网使用 `GOTOOLCHAIN=local`，需要其他 Go 版本时通过 `download golang` 下载后安装。模块校验与输入要求见下文。

## 2. 文件布局与模块协议

- Go 发行归档平铺存储，发布清单 `go.dev/dl/?mode=json&include=all` 的
  files 条目包含文件名、平台、架构和 sha256，默认按主流平台筛选。
- 服务端上传成功后写入 `{归档}.sha256`，内容为小写十六进制哈希，
  供 mise 等版本管理器校验。
- Go 模块的 `.mod`、`.zip`、`.info` 按原始内容存储和返回；
  `@v/list`、`@latest` 根据目录中的版本动态生成。
- go.sum 逐行记录模块版本及哈希，客户端下载其中的模块版本，
  无需本地 Go 命令。
- **路径转义**：模块路径大写字母按代理协议转义为 `!` + 小写
  （`github.com/Azure` → `github.com/!azure`），服务端 URL 层解码映射，
  存储用原始路径（同 npm 存原始包名的做法，浏览页友好）；
- **404 语义**：module proxy 协议要求缺失资源返回 404/410（go 命令据此
  回退或报错），服务端返回 404 和 HTML 补下载提示。

## 3. 命令行

### 3.1 download golang

```
pkgmirror download golang <版本>... [--platform LIST] [--arch LIST]
                          [--src] [--all-platforms]
                          [--dist-url URL] [--proxy URL] [-o 目录]
```

- 版本形式：`1.23`（该 minor 最新 patch）、`1.23.4`（精确）、`1`（主版本
  最新），逗号分隔可多个；排除 rc 等预发布条目；
- 元数据固定拉取 `https://go.dev/dl/?mode=json&include=all`，含历史版本；网络不畅时用 `--proxy`；
- **平台过滤（默认白名单，两维度独立）**：
  - 未给出的维度套白名单：os ∈ {linux, darwin, windows}，arch ∈
    {amd64, arm64}，具体数量取决于目标版本的可用产物；
  - 显式给出的维度精确匹配 JSON 的 `os`/`arch` 字段：`--platform freebsd
    --arch 386` 只下 freebsd-386；只给 `--platform linux` 仍套 arch 白名单
    （linux-amd64/arm64），控制归档数量；
  - 组合在 files 里缺失（如 darwin-386）安静跳过；整体过滤结果为空时报错；
  - `--all-platforms` 跳过默认白名单，按显式过滤条件选择归档；
- `--platform` 按 JSON 的 os 字段匹配（`linux,darwin,windows`，
  macos 为 darwin 别名）；`--arch` 归一到 `amd64/arm64`（x86_64/aarch64
  为别名）；
- kind 固定只取 `archive`（unix tar.gz / windows zip）；`--src` 追加
  `kind=source` 条目（每目标版本一个）；
- `--dist-url` 归档根三级来源：命令行 > client.toml `golang-dist-url` >
  `GOLANG_DIST_URL` > `https://dl.google.com/go`（国内可配
  `https://mirrors.aliyun.com/golang`，flat 同布局）；
- 下载按 JSON 内嵌 sha256 校验后落盘 `{out}/golang/{filename}`，
  `--proxy` 指定代理，`-o` 指定产出目录，manifest 与已有清单增量合并。

### 3.2 download gomod

```
pkgmirror download gomod [-r go.sum] [模块@版本]...
                         [--goproxy URL] [--proxy URL] [-o 目录]
```

- `-r` 接受 go.sum（逐行解析 `模块 版本[/go.mod] h1:哈希`，版本并集去重，
  空行与 `#` 注释忽略）；与命令行模块可并用（并集）；
- 命令行支持四种形式：裸模块名（自动查询上游 `@latest` 端点取最新稳定
  版）、`模块@latest`（显式等价）、`模块@v1.8`（minor 前缀，拉 `@v/list`
  取该线最新稳定 patch，预发布排除）、`模块@v1.8.1`（精确）。版本发现
只用上游 proxy 的官方端点，不依赖本地 go 命令；
- 命令行目标递归展开依赖闭包：下载各模块 `.mod` 时解析其中的 `require`
  声明（单行与块两种语法、注释与 indirect 标记忽略、replace 是主模块概
  念不参与），全量边集合入队下载。下载端保留 require 中各版本，
  由内网 Go 命令选择构建版本；go.sum 中的模块版本也按同样规则递归展开；
- 每个模块@版本下载 `.mod`、`.zip`、`.info`，供模块解析和版本查询；`.zip` 不做 h1 校验（内网 go 命令
  按项目 go.sum 校验），下载时算 sha256 记入 manifest；
- go.sum 可包含未进入 build list 的历史模块版本，客户端下载时一并保留；
- `--goproxy` 三级来源：命令行 > client.toml `goproxy` > `GOPROXY` 环境变量
  （取逗号分隔首段，忽略 `direct`）> `https://proxy.golang.org`（国内可配
  `https://goproxy.cn`）；
- 上游请求路径 `{goproxy}/{转义后模块}/@v/{版本}.{mod|zip|info}`，大写
  字母转义为 `!` + 小写（协议规定）；
- 落盘 `{out}/gomod/{模块}/@v/{版本}.{ext}`（原始模块路径嵌套目录）。

## 4. 服务端

### 4.1 存储布局（root 目录）

```
data/
├── golang/go1.23.4.linux-amd64.tar.gz         # flat，与 dl.google.com/go 同布局
└── gomod/{模块路径}/@v/
    ├── v1.9.1.mod
    ├── v1.9.1.zip
    └── v1.9.1.info
```

归档和模块文件上传后生成同名 `.sha256` 缓存。

### 4.2 路由

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/golang/` | Go 安装包浏览页（版本、文件、大小） |
| GET | `/golang/{file}` | 归档下载；404 返回提示页"外网执行 download golang 补同步" |
| GET | `/goproxy/{模块}/@v/{版本}.{mod\|zip\|info}` | 模块文件，`http.ServeFile` |
| GET | `/goproxy/{模块}/@v/list` | 目录扫描合成：`@v/` 下文件名取版本并集，每行一个 |
| GET | `/goproxy/{模块}/@latest` | 最大版本的 `.info` 内容；缺失时合成 `{"Version":"..."}` |
| GET | `/browse/gomod/` | 模块浏览列表（递归扫描含 `@v` 的目录） |
| GET | `/browse/gomod/{模块}/` | 模块详情页：文件列表（链接指向 `/goproxy/` 协议端点） |

- 路径解析用 `r.URL.EscapedPath()` + `url.PathUnescape`（npm 的 `%2F`
  手法）：模块路径段还原后再做 `!x` 大写解码；`!` 后非字母视为非法；
- 模块路径白名单：整体 `字母数字开头`，段字符限 `[A-Za-z0-9._~-]`，段
  非空且不为 `.`/`..`，防路径穿越；
- 模块文件名白名单：`^v[A-Za-z0-9.+-]+\.(mod|zip|info)$`；golang 归档
  文件名沿用 `fileRe`；
- 缺失资源一律 404 + HTML 提示页（module proxy 协议只看状态码，body 供
  人工排障）；
- 首页统计 Go 版本数与模块数，提供两类接入配置；导航 Go 组含安装包与模块入口，上传表单支持 `golang`、`gomod`。

### 4.3 上传接口

`/api/upload` 接收以下 multipart 表单字段：

| 字段 | 说明 |
| --- | --- |
| `type` | `golang` 或 `gomod` |
| `name` | type=gomod 时的模块路径（必填，原始大小写；不从文件名反推：版本文件名不含模块路径） |
| `sha256` | 期望哈希，可省略；提供时落盘前校验 |
| `file` | 归档或模块文件内容 |

- type=golang 落盘 `golang/{filename}`，不使用 name/tag；
- type=gomod 落盘 `gomod/{name}/@v/{filename}`；
- 幂等跳过与 409 冲突复用 `storeUpload`。

## 5. manifest 字段

- Go 安装包：`File.Type` 为 `"golang"`，文件名包含版本号。
- Go 模块：`File.Type` 为 `"gomod"`，`Name` 必填，保存原始大小写的模块路径。

上传客户端将清单字段写入表单，服务端按类型、模块名和文件名构造落盘路径。

## 6. 内网接入

```bash
# Go 模块代理（go build / go mod download / go install 直接可用）
export GOPROXY=http://user:pass@host:19999/goproxy
export GOSUMDB=off            # go.sum 完整时普通模块免 sumdb 校验
export GOTOOLCHAIN=local      # 工具链不走下载，新版本人工 download golang 补装
```

```bash
# Go 安装包（版本管理器二选一）
export MISE_GO_DOWNLOAD_MIRROR=http://user:pass@host:19999/golang   # mise
export GO_DOWNLOAD_MIRROR=http://user:pass@host:19999/golang        # asdf-golang
```

- `GOSUMDB=off` 后 go 命令信任镜像并把哈希写入 go.sum；仅 GOTOOLCHAIN=auto
  的工具链下载在 off 下必然失败（官方机制强制 sumdb），故定 local；
- gvm 与 golang.org/dl 辅助命令不支持自定义镜像，走浏览页手工下载解压。

## 7. 验证

- 单元测试：dl JSON 解析、版本 spec 解析、Select 白名单默认 6 归档 /
  显式平台精确匹配 / 组合缺失跳过 / 空结果报错 / rc 排除 / --src /
  --all-platforms；模块路径转义与解码（大写往返、非法 `!` 拒绝）、
  ValidModulePath（穿越拒绝）、go.sum 解析（mod/zip 行、extra 行去重、
  注释空行）、版本比较（semver / pseudo / +incompatible）、LatestStable
  与 IsStablePrefix（预发布排除、+incompatible 稳定）、ParseRequires
  （单行/块语法、注释、伪版本、非法行跳过）、命令行引用四形式分类与
  @latest/@v/list 解析（httptest 模拟上游，含 404 与无匹配前缀）、
  `@v/list` 与 `@latest` 合成（含 info 缺失兜底）、上传 type=golang/gomod
  的路径与名称校验、路由含 `%2F` 与 `!x` 的解码命中；
- 集成冒烟（真实网络）：`download golang 1.23 --platform darwin` →
  upload → curl 校验归档与 `.sha256`；`download gomod -r 本仓库 go.sum`
  → upload → 起 server → 隔离目录 `GOPROXY` 指镜像 `go mod download`
  和 `GOSUMDB=off GOTOOLCHAIN=local go build` 检查依赖完整性；有 mise 时设
  `MISE_GO_DOWNLOAD_MIRROR` 检查安装；重复 upload 检查幂等；
- 构建：运行 `go build ./...`，跨平台构建使用项目脚本。

## 8. 风险与约束

1. **认证**：GOPROXY 地址可内嵌 Basic auth 凭据，无凭据访问启用认证的镜像返回 401。
2. **GOTOOLCHAIN=auto 内网不可用**：工具链作为 `golang.org/toolchain` 模块
   经 GOPROXY 下载，校验强制走 GOSUMDB，off 即失败、GOPRIVATE 无法豁免。离线环境使用 `GOTOOLCHAIN=local`，通过安装包补充工具链。
3. **依赖同步**：内网修改 go.mod 引入未镜像的依赖时，需在外网补下载。
4. **大小写不敏感文件系统**：模块路径存原始大小写，两个仅大小写不同的
   模块同镜像时会互撞。
5. **归档体积**：数量和体积随版本及平台而变，可用 `--platform`、`--arch` 控制。
