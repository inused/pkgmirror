@echo off
rem 交叉编译四个平台到 dist\，发布包与校验清单输出到 dist\release\。
rem 用法: scripts\build.bat（需 Go 1.20.7）
setlocal
cd /d "%~dp0.."
if errorlevel 1 exit /b 1
if not exist dist mkdir dist
if errorlevel 1 exit /b 1

set CGO_ENABLED=0

set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o dist\pkgmirror-windows-amd64.exe .
if errorlevel 1 exit /b 1
echo 已构建 dist\pkgmirror-windows-amd64.exe

set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o dist\pkgmirror-linux-amd64 .
if errorlevel 1 exit /b 1
echo 已构建 dist\pkgmirror-linux-amd64

set GOOS=linux
set GOARCH=arm64
go build -trimpath -ldflags "-s -w" -o dist\pkgmirror-linux-arm64 .
if errorlevel 1 exit /b 1
echo 已构建 dist\pkgmirror-linux-arm64

set GOOS=darwin
set GOARCH=arm64
go build -trimpath -ldflags "-s -w" -o dist\pkgmirror-darwin-arm64 .
if errorlevel 1 exit /b 1
echo 已构建 dist\pkgmirror-darwin-arm64

rem 将服务端与客户端示例配置复制到产物目录。
copy /y resources\server.example.toml dist\server.example.toml
if errorlevel 1 exit /b 1
copy /y resources\client.example.toml dist\client.example.toml
if errorlevel 1 exit /b 1

copy /y README.md dist\README.md
if errorlevel 1 exit /b 1
copy /y LICENSE dist\LICENSE
if errorlevel 1 exit /b 1
copy /y THIRD_PARTY_NOTICES dist\THIRD_PARTY_NOTICES
if errorlevel 1 exit /b 1

rem 打包工具在构建主机上运行。
set GOOS=
set GOARCH=
go run ./scripts/release
if errorlevel 1 exit /b 1

endlocal
