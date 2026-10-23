// Package paths 查找当前工作目录或二进制目录下的默认配置文件。
package paths

import (
	"os"
	"path/filepath"
)

// ExeDir 返回二进制所在目录。
//
// 获取可执行文件路径失败时返回 "."，由调用方按当前工作目录解析。
//
//	@return string 二进制目录的绝对或相对路径。
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	// macOS 上可执行文件可能经符号链接安置，取真实路径所在的目录
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// FindConfig 按探测链查找默认配置文件：当前目录优先，其次二进制目录。
//
// 两处都不存在时返回 name，由调用方按配置文件缺失处理。
//
//	@param name string 配置文件名（如 server.toml）。
//	@return string 实际路径：命中的探测结果，或当前目录下的名字。
func FindConfig(name string) string {
	if _, err := os.Stat(name); err == nil {
		return name
	}
	p := filepath.Join(ExeDir(), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return name
}
