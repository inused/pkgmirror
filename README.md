# pymirror

uv / pip 离线镜像工具：单二进制提供服务端与客户端，为无法联网的环境提供
PyPI 包与 CPython 安装包的按需镜像能力。

## 功能特性

- **PyPI 包镜像**：PEP 503 simple 协议，pip / uv 直接可用
- **Python 安装包镜像**：`uv python install` 直接可用（含精确补丁版本）
- **按需下载客户端**：自动解析依赖（uv），支持按版本、操作系统、架构过滤
- **网页管理**：文件浏览、包上传、接入配置一键复制
- **零依赖部署**：单二进制 + 文件目录存储，无数据库等中间件
- **可扩展**：数据目录与页面按镜像类型平铺组织（`pypi/`、`python/`，可继续扩展）
- 跨平台：Linux / Windows（含 Win7）/ macOS，HTTP 代理与镜像源可配

## 环境要求

| 场景 | 要求 |
| --- | --- |
| 构建 | Go 1.20.7 |
| 下载客户端 | 已安装 [uv](https://docs.astral.sh/uv/)（用于依赖解析） |
| 运行 | 无依赖，单二进制 |

## 快速开始

```bash
# 1. 构建（scripts/build.sh 产出全平台产物到 dist/，Windows 用 scripts\build.bat）
go build -o pymirror .

# 2. 启动服务端（示例配置见 resources/）
cp resources/server.example.toml server.toml   # 修改监听地址、数据目录、账号
./pymirror server --config server.toml

# 3. 下载（在可联网的机器上）
./pymirror download python 3.12                     # Python 安装包
./pymirror download pypi --python 3.12 six          # PyPI 包（含依赖）

# 4. 上传（把 downloads/ 目录传给服务端）
./pymirror upload --server http://user:pass@host:port downloads
```

使用方接入（服务端首页会按实际访问地址生成可直接复制的配置）：

```bash
export UV_INDEX_URL=http://user:pass@host:port/simple
export UV_PYTHON_INSTALL_MIRROR=http://user:pass@host:port/python
```

之后 `uv pip install`、`uv python install` 与 `pip install --index-url ...` 全部走镜像。

## 文档

- [docs/使用指南.md](docs/使用指南.md) —— 构建部署、下载/上传详解、网络配置、常见问题
- [docs/设计.md](docs/设计.md) —— 架构、存储布局、协议细节、风险与约束

## 项目结构

```
main.go            入口与子命令分发
internal/pypi      PyPI 协议（PEP 503、simple 解析、wheel 过滤）
internal/pymeta    uv 下载元数据解析与筛选
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
