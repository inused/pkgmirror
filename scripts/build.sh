#!/bin/sh
# 交叉编译 pymirror 全平台产物到 dist/，并附带示例配置文件。
# 用法: scripts/build.sh（需 Go 1.20.7）
set -e
cd "$(dirname "$0")/.."
mkdir -p dist

# 目标平台：Windows 64 位、Linux amd64/arm64、macOS arm64
for target in windows/amd64 linux/amd64 linux/arm64 darwin/arm64; do
    os=${target%/*}
    arch=${target#*/}
    ext=""
    if [ "$os" = "windows" ]; then
        ext=".exe"
    fi
    # 纯 Go 项目，禁用 cgo 保证产物无动态链接依赖
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -o "dist/pymirror-$os-$arch$ext" .
    echo "已构建 dist/pymirror-$os-$arch$ext"
done

# 附带示例配置（resources/ 存放项目附属资源，扩展新镜像时新增对应示例）
cp resources/server.example.toml resources/client.example.toml dist/
echo "已附带示例配置到 dist/"
