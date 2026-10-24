# pkgmirror

uv / pip / npm / node 离线镜像工具：单二进制提供服务端与客户端，为无法
联网的环境提供 PyPI 包、CPython 与 Node 安装包、npm 包的按需镜像能力。

## 功能特性

- **Python 安装包镜像**：`uv python install` 直接可用（含精确补丁版本）
- **npm 包镜像**：标准 registry 协议（packument + tarball），npm / pnpm / yarn 直接可用
- **Node 安装包镜像**：官方 nodejs.org/dist 布局（可移植归档 + SHASUMS256.txt），nvm / nvm-windows / n / mise 换镜像变量直接可用
- **按需下载客户端**：自动解析依赖（uv / npm），支持按版本、操作系统、架构过滤；
  pypi 全精确清单与 npm 现成 lock 无需解析器
- **网页管理**：文件浏览、包上传、接入配置一键复制
- **零依赖部署**：单二进制 + 文件目录存储，无数据库等中间件
- **可扩展**：数据目录与页面按镜像类型平铺组织（`pypi/`、`python/`、`npm/`、`node/`，可继续扩展）
- 跨平台：Linux / Windows（含 Win7）/ macOS，HTTP 代理与镜像源可配

## 环境要求

| 场景 | 要求 |
| --- | --- |
| 构建 | Go 1.20.7 |
| 下载客户端 | 已安装 [uv](https://docs.astral.sh/uv/)（PyPI 裸包名解析）、npm ≥ 7（npm 裸包名/package.json 解析）；精确清单与现成 lock 无需 |
| 运行 | 无依赖，单二进制 |

## 快速开始

```bash
# 1. 构建（scripts/build.sh 产出全平台产物到 dist/，Windows 用 scripts\build.bat）
go build -o pkgmirror .

# 2. 启动服务端（示例配置见 resources/）
cp resources/server.example.toml server.toml   # 修改监听地址、数据目录、账号
./pkgmirror server --config server.toml

# 3. 下载（在可联网的机器上）
./pkgmirror download python 3.12                     # Python 安装包
./pkgmirror download pypi --python 3.12 six          # PyPI 包（含依赖）
./pkgmirror download npm lodash                      # npm 包（含依赖）
./pkgmirror download npm -r package-lock.json        # npm 现成 lock（无需 npm）
./pkgmirror download node 22                         # Node 安装包（--lts 为最新 LTS）

# 4. 上传（把 downloads/ 目录传给服务端）
./pkgmirror upload --server http://user:pass@host:port downloads
```

使用方接入（服务端首页会按实际访问地址生成可直接复制的配置）：

```bash
export UV_INDEX_URL=http://user:pass@host:port/simple
export UV_PYTHON_INSTALL_MIRROR=http://user:pass@host:port/python
```

```ini
# npm / pnpm（~/.npmrc 或项目 .npmrc；npm 不支持 URL 内嵌凭据）
registry=http://host:port/npm/
//host:port/:_auth=BASE64(user:pass)
```

```bash
# Node 版本管理器（可选其一）
export NVM_NODEJS_ORG_MIRROR=http://user:pass@host:19999/node   # nvm
export N_NODE_MIRROR=http://user:pass@host:19999/node           # n
export MISE_NODE_MIRROR_URL=http://user:pass@host:19999/node    # mise
nvm node_mirror http://host:19999/node/                         # nvm-windows
```

之后 `uv pip install`、`uv python install`、`pip install --index-url ...`、
`npm install`（及 pnpm / yarn）、`nvm install`（及 n / mise / nvm-windows）
全部走镜像。

## 文档

- [docs/使用指南.md](docs/使用指南.md) —— 构建部署、下载/上传详解、网络配置、常见问题
- [docs/设计.md](docs/设计.md) —— 架构、存储布局、协议细节、风险与约束
- [docs/npm设计.md](docs/npm设计.md) —— npm 镜像的协议差异、模式与约束
- [docs/node设计.md](docs/node设计.md) —— Node 镜像的 dist 布局协议、版本管理器接入与 Python 页面标注

## 项目结构

```
main.go            入口与子命令分发
internal/pypi      PyPI 协议（PEP 503、simple 解析、wheel 过滤）
internal/pymeta    uv 下载元数据解析与筛选
internal/npm       npm 协议（包名校验、lock 解析、packument 重写、SRI 校验）
internal/node      node dist 协议（index.json 解析、版本选择、平台映射、SHASUMS 校验）
internal/manifest  下载产出目录的清单契约
internal/server    服务端（路由、页面、上传）
internal/client    客户端（下载、上传、配置）
resources/         项目附属资源（示例配置等，扩展新镜像时在此新增）
scripts/           交叉编译脚本（sh / bat）
docs/              文档
```

## 开发

```bash
go test ./...        # 单元测试
scripts/build.sh     # 交叉编译四平台产物到 dist/
```
