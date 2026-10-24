// Package manifest 定义下载产出目录的清单文件结构：
// download 产出、upload 消费，是两端之间唯一的契约。
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// File 描述下载产出目录中的一个文件。
// type 为 "pypi" 时 Name 必填（PEP 503 规范包名）；
// type 为 "python" 时 Tag 必填（python-build-standalone 构建标签）；
// type 为 "npm" 时 Name 必填（完整包名，scoped 时含 @scope/ 前缀）；
// type 为 "node" 时 Tag 必填（Node 版本目录名，如 v22.14.0）。
// Path 仅用于上传方在本下载产出目录内定位文件，服务端按 type 与 name/tag/filename
// 自行构造落盘路径，不信任 Path。
type File struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Manifest 是下载产出目录根下的 manifest.json。
type Manifest struct {
	Version   int       `json:"version"`
	Generator string    `json:"generator"`
	Created   time.Time `json:"created"`
	Files     []File    `json:"files"`
}

// Load 从下载产出目录读取并校验 manifest.json。
//
//	@param dir string 下载产出目录路径。
//	@return Manifest 解析出的清单。
//	@return error 文件不存在、JSON 解析失败或条目字段不完整时的错误。
func Load(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(dir + "/manifest.json")
	if err != nil {
		return m, fmt.Errorf("读取清单失败: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("解析 manifest.json 失败: %w", err)
	}
	if m.Version != 1 {
		return m, fmt.Errorf("不支持的清单版本 %d", m.Version)
	}
	if len(m.Files) == 0 {
		return m, fmt.Errorf("清单为空")
	}
	for i, f := range m.Files {
		if f.Type != "pypi" && f.Type != "python" && f.Type != "npm" && f.Type != "node" {
			return m, fmt.Errorf("第 %d 个文件 type 非法: %q", i+1, f.Type)
		}
		if f.Filename == "" || f.Path == "" || f.SHA256 == "" {
			return m, fmt.Errorf("第 %d 个文件缺少 filename/path/sha256", i+1)
		}
		if (f.Type == "pypi" || f.Type == "npm") && f.Name == "" {
			return m, fmt.Errorf("第 %d 个文件（%s）缺少 name", i+1, f.Type)
		}
		if (f.Type == "python" || f.Type == "node") && f.Tag == "" {
			return m, fmt.Errorf("第 %d 个文件（%s）缺少 tag", i+1, f.Type)
		}
	}
	return m, nil
}

// Save 将清单写入 dir/manifest.json（缩进两空格）。
//
//	@param dir string 下载产出目录路径，需已存在。
//	@param m Manifest 待写入的清单。
//	@return error 序列化或写盘失败时的错误。
func Save(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dir+"/manifest.json", data, 0o644)
}
