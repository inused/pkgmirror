#!/bin/sh
# 交叉编译四个平台到 dist/，发布包与校验清单输出到 dist/release/。
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
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "dist/pkgmirror-$os-$arch$ext" .
    echo "已构建 dist/pkgmirror-$os-$arch$ext"
done

# 将服务端与客户端示例配置复制到产物目录。
cp resources/server.example.toml resources/client.example.toml dist/
cp README.md LICENSE THIRD_PARTY_NOTICES dist/
GOOS= GOARCH= go run ./scripts/release
