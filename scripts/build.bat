@echo off
rem 交叉编译 pymirror 全平台产物到 dist\，并附带示例配置文件。
rem 用法: scripts\build.bat（需 Go 1.20.7）
setlocal
cd /d "%~dp0.."
if not exist dist mkdir dist

set CGO_ENABLED=0

set GOOS=windows
set GOARCH=amd64
go build -trimpath -o dist\pymirror-windows-amd64.exe .
echo 已构建 dist\pymirror-windows-amd64.exe

set GOOS=linux
set GOARCH=amd64
go build -trimpath -o dist\pymirror-linux-amd64 .
echo 已构建 dist\pymirror-linux-amd64

set GOOS=linux
set GOARCH=arm64
go build -trimpath -o dist\pymirror-linux-arm64 .
echo 已构建 dist\pymirror-linux-arm64

set GOOS=darwin
set GOARCH=arm64
go build -trimpath -o dist\pymirror-darwin-arm64 .
echo 已构建 dist\pymirror-darwin-arm64

rem 附带示例配置（resources 目录，扩展新镜像时新增对应示例）
copy /y resources\server.example.toml dist\server.example.toml >nul
copy /y resources\client.example.toml dist\client.example.toml >nul
echo 已附带 server.example.toml、client.example.toml 到 dist\

endlocal
