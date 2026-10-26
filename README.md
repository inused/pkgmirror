# pkgmirror

uv / pip / npm / node / go 离线镜像工具：单二进制提供服务端与客户端，为无法
联网的环境提供 PyPI 包、CPython 与 Node 安装包、npm 包、Go 安装包与
Go 模块的按需镜像能力。

## 功能特性

- **Python 安装包镜像**：`uv python install` 直接可用（含精确补丁版本）
- **PyPI 包镜像**：PEP 503 simple 协议，pip / uv 直接可用
- **Node 安装包镜像**：官方 nodejs.org/dist 布局（可移植归档 + SHASUMS256.txt），nvm / nvm-windows / n / mise 换镜像变量直接可用
- **npm 包镜像**：标准 registry 协议（packument + tarball），npm / pnpm / yarn 直接可用
- **Go 安装包镜像**：官方 dl.google.com/go flat 布局（归档 + .sha256 伴随文件），mise / asdf-golang 换镜像变量直接可用
- **Go 模块镜像**：module proxy 协议（.mod/.zip/.info 静态 + @v/list 与 @latest 合成）；按 go.sum 或模块名下载，递归读取 .mod 的 require 补齐依赖，无需本地 Go 命令
- **按需下载客户端**：自动解析依赖（uv / npm），支持按版本、操作系统、架构过滤；
  pypi 全精确清单与 npm 现成 lock 无需解析器
- **删除能力**：命令行和网页预览后删除包 / 模块 / 安装包；`--deps` 按镜像元数据级联未被其他包引用的依赖，或扩展至安装包的整个版本组
- **网页管理**：文件浏览、包上传、接入配置一键复制
- **服务端单二进制部署**：镜像数据存放在文件目录中
- 跨平台：Linux / Windows（含 Win7）/ macOS，HTTP 代理与镜像源可配

## 环境要求

| 场景 | 要求 |
| --- | --- |
| 构建 | Go 1.20.7 |
| 下载客户端 | 已安装 [uv](https://docs.astral.sh/uv/)（PyPI 裸包名解析）、npm ≥ 7（npm 裸包名/package.json 解析）；精确清单与现成 lock 无需 |
| 服务端、上传和删除客户端 | 单二进制，直接运行 |

## 安装

从 GitHub 仓库的 Releases 页面下载对应平台的 `pkgmirror-<系统>-<架构>.zip`，
可用平台为 Windows amd64、Linux amd64/arm64、macOS arm64。解压后将程序
重命名为 `pkgmirror`（Windows 为 `pkgmirror.exe`）。

每个发布包包含程序、README、`docs/` 文档、两份示例配置、`LICENSE` 和
`THIRD_PARTY_NOTICES`。同一 Release 的 `SHA256SUMS` 记录各 ZIP 的校验和。

从源码构建，在仓库根目录执行：

```bash
go build -o pkgmirror .
```

## 快速开始

```bash
# 1. 启动服务端
cp server.example.toml server.toml            # 源码目录中从 resources/ 复制
# 编辑 server.toml，设置数据目录，并将 CHANGE_ME_BEFORE_STARTING 替换为随机长口令
./pkgmirror server --config server.toml

# 2. 下载（在可联网的机器上）
./pkgmirror download python 3.12                     # Python 安装包
./pkgmirror download pypi --python 3.12 six          # PyPI 包（含依赖）
./pkgmirror download npm lodash                      # npm 包（含依赖）
./pkgmirror download npm -r package-lock.json        # npm 现成 lock（无需 npm）
./pkgmirror download node 22                         # Node 安装包（--lts 为最新 LTS）
./pkgmirror download golang 1.23                      # Go 安装包（默认主流平台，--all-platforms 全量）
./pkgmirror download gomod -r go.sum                  # Go 模块闭包（无需外部工具）
./pkgmirror download gomod github.com/spf13/cobra     # 裸名自动取最新稳定版并递归展开依赖

# 3. 上传（把 downloads/ 目录传给服务端）
./pkgmirror upload --server http://user:pass@host:port downloads
```

示例 URL 中的 `user:pass` 需替换为 `server.toml` 中的账号和口令。全部认证
账号均可上传和删除；用户表为空时允许匿名浏览、下载、上传和删除。服务端
适用于受信任的局域网，跨不可信网络接入时应在前置反向代理配置 HTTPS。

使用方接入（服务端首页会按实际访问地址生成可直接复制的配置）：

```bash
export UV_INDEX_URL=http://user:pass@host:port/mirror/simple
export UV_PYTHON_INSTALL_MIRROR=http://user:pass@host:port/mirror/python
```

```ini
# npm / pnpm（~/.npmrc 或项目 .npmrc；npm 不支持 URL 内嵌凭据）
registry=http://host:port/mirror/npm/
//host:port/:_auth=BASE64(user:pass)
```

```bash
# Node 版本管理器（可选其一）
export NVM_NODEJS_ORG_MIRROR=http://user:pass@host:19999/mirror/node   # nvm
export N_NODE_MIRROR=http://user:pass@host:19999/mirror/node           # n
export MISE_NODE_MIRROR_URL=http://user:pass@host:19999/mirror/node    # mise
nvm node_mirror http://host:19999/mirror/node/                         # nvm-windows
```

```bash
# Go 模块代理（离线工具链配置见 docs/golang设计.md）
export GOPROXY=http://user:pass@host:19999/mirror/goproxy GOSUMDB=off GOTOOLCHAIN=local

# Go 安装包（版本管理器二选一）
export MISE_GO_DOWNLOAD_MIRROR=http://user:pass@host:19999/mirror/golang   # mise
export GO_DOWNLOAD_MIRROR=http://user:pass@host:19999/mirror/golang        # asdf-golang
```

之后 `uv pip install`、`uv python install`、`pip install --index-url ...`、
`npm install`（及 pnpm / yarn）、`nvm install`（及 n / mise / nvm-windows）、
`go build`（及 go mod download / go install）全部走镜像。

## 文档

- [docs/使用指南.md](docs/使用指南.md) —— 构建部署、下载/上传详解、网络配置、常见问题
- [docs/设计.md](docs/设计.md) —— 架构、存储布局、协议细节、风险与约束
- [docs/npm设计.md](docs/npm设计.md) —— npm 镜像的协议差异、模式与约束
- [docs/node设计.md](docs/node设计.md) —— Node 镜像的 dist 布局协议与版本管理器接入
- [docs/golang设计.md](docs/golang设计.md) —— Go 镜像的 flat 布局、module proxy 协议、GOTOOLCHAIN 约束与接入
- [docs/路由设计.md](docs/路由设计.md) —— 浏览页面和镜像协议路径
- [docs/删除设计.md](docs/删除设计.md) —— 删除单位、预览和依赖级联
- [docs/页面与日志设计.md](docs/页面与日志设计.md) —— 导航、搜索和日志

## 项目结构

```
main.go            入口与子命令分发
internal/pypi      PyPI 协议（PEP 503、simple 解析、wheel 过滤）
internal/pymeta    uv 下载元数据解析与筛选
internal/npm       npm 协议（包名校验、lock 解析、packument 重写、SRI 校验）
internal/node      node dist 协议（index.json 解析、版本选择、平台映射、SHASUMS 校验）
internal/golang    Go dl 协议（发布清单解析、版本选择、平台白名单过滤）
internal/gomod     Go module proxy 协议（路径转义、go.sum 解析、版本比较）
internal/manifest  下载产出目录的清单契约
internal/server    服务端（路由、页面、上传、删除）
internal/client    客户端（下载、上传、删除、配置）
resources/         服务端与客户端示例配置
scripts/           交叉编译脚本与发布包工具
docs/              文档
```

## 开发与贡献

问题反馈请附程序版本、操作系统、复现命令和已脱敏的错误输出。提交修改前
运行测试，涉及行为变化时同步相关用例和文档。

```bash
go test ./...        # 单元测试
go vet ./...         # Go 静态检查
scripts/build.sh     # macOS/Linux 构建四个平台；Windows 使用 scripts\build.bat
```

构建脚本将程序和示例文件输出到 `dist/`，按固定文件清单生成
`dist/release/` 中的四个 ZIP 和 `SHA256SUMS`。发布时上传这五个文件。

## 许可证

项目代码和文档采用 [MIT 许可证](LICENSE)。第三方组件的版权和许可原文见
[THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。通过本工具下载或镜像的内容按其
各自的许可证分发。
