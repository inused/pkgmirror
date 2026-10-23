package client

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// ClientConfig 是 download 子命令的可选配置文件（TOML），
// 与命令行参数、环境变量构成三级来源，命令行优先级最高。
type ClientConfig struct {
	// IndexURL 是 PyPI 索引地址，空表示未配置。
	IndexURL string `toml:"index-url"`
	// Registry 是 npm registry 地址（download npm 使用），空表示未配置。
	Registry string `toml:"registry"`
	// GitHubPrefix 是 GitHub 反代前缀，空表示未配置。
	GitHubPrefix string `toml:"github-prefix"`
	// Proxy 是 HTTP 代理地址（如 http://127.0.0.1:7890），空表示未配置。
	Proxy string `toml:"proxy"`
}

// LoadClientConfig 读取客户端配置文件。
//
// 文件不存在时返回零值配置，由调用方继续读取环境变量与默认值。
//
//	@param path string 配置文件路径。
//	@return ClientConfig 解析出的配置；文件不存在时为零值。
//	@return error 文件存在但读取或 TOML 解析失败时的错误。
func LoadClientConfig(path string) (ClientConfig, error) {
	var cfg ClientConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析客户端配置 %s 失败: %w", path, err)
	}
	return cfg, nil
}
