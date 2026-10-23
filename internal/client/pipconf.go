// Package client 实现 download 与 upload 两个子命令的客户端逻辑。
package client

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"pkgmirror/internal/npm"
	"pkgmirror/internal/pypi"
)

// pipConfPaths 返回当前平台 pip 配置文件的候选路径（按优先顺序）。
//
// Windows 按 pip 约定读 %APPDATA%\pip\pip.ini；其余平台读 XDG 位置与系统级配置。
//
//	@return []string 候选文件路径列表，可能不存在。
func pipConfPaths() []string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return []string{filepath.Join(appdata, "pip", "pip.ini")}
		}
		return nil
	}
	paths := []string{}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "pip", "pip.conf"))
	} else if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "pip", "pip.conf"))
	}
	return append(paths, "/etc/pip/pip.conf")
}

// readPipConfIndex 解析 pip 配置中 [global] 或 [install] 段的 index-url。
//
// 只实现 pip.conf 用到的 ini 子集（段头 + key = value），不支持多行值等高级语法。
//
//	@return string 找到的 index-url；未配置时为空串。
func readPipConfIndex() string {
	for _, path := range pipConfPaths() {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		section := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.TrimSpace(line[1 : len(line)-1])
				continue
			}
			if section != "global" && section != "install" {
				continue
			}
			if key, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(key) == "index-url" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// ResolveIndexURL 按优先级解析 PyPI 索引地址。
//
// 优先级：命令行参数 > 客户端配置文件 > 环境变量 UV_INDEX_URL > PIP_INDEX_URL
// > pip.conf > 官方源。这样外网机已有的 uv/pip 镜像配置可以直接复用。
//
//	@param explicit string 命令行 --index-url 传入的值，空串表示未指定。
//	@param fromConfig string 客户端配置文件的 index-url，空串表示未配置。
//	@return string 解析出的索引地址，恒非空。
func ResolveIndexURL(explicit, fromConfig string) string {
	switch {
	case explicit != "":
		return explicit
	case fromConfig != "":
		return fromConfig
	case os.Getenv("UV_INDEX_URL") != "":
		return os.Getenv("UV_INDEX_URL")
	case os.Getenv("PIP_INDEX_URL") != "":
		return os.Getenv("PIP_INDEX_URL")
	case readPipConfIndex() != "":
		return readPipConfIndex()
	default:
		return pypi.DefaultIndexURL
	}
}

// ResolveRegistry 按优先级解析 npm registry 地址。
//
// 优先级：命令行参数 > 客户端配置文件 > NPM_CONFIG_REGISTRY 环境变量
// > 官方源。外网机已有的 npm 镜像配置（npm config 设置的环境变量）可直接复用。
//
//	@param explicit string 命令行 --registry 传入的值，空串表示未指定。
//	@param fromConfig string 客户端配置文件的 registry，空串表示未配置。
//	@return string 解析出的 registry 地址，恒非空。
func ResolveRegistry(explicit, fromConfig string) string {
	switch {
	case explicit != "":
		return explicit
	case fromConfig != "":
		return fromConfig
	case os.Getenv("NPM_CONFIG_REGISTRY") != "":
		return os.Getenv("NPM_CONFIG_REGISTRY")
	default:
		return npm.DefaultRegistry
	}
}
