package main

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateReleases 验证归档内容、执行权限、校验和及重复运行，并排除镜像数据。
func TestCreateReleases(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"README.md": "使用说明", "LICENSE": "项目许可", "THIRD_PARTY_NOTICES": "第三方许可",
		"resources/server.example.toml": "[users]\n", "resources/client.example.toml": "# 示例\n",
		"docs/使用指南.md": "使用指南", "docs/设计.md": "总体设计",
		"docs/npm设计.md": "npm 设计", "docs/node设计.md": "Node 设计",
		"docs/golang设计.md": "Go 设计", "docs/路由设计.md": "路由设计",
		"docs/删除设计.md": "删除设计", "docs/页面与日志设计.md": "页面与日志",
		"dist/data/private-package/secret.txt": "不得分发",
		"dist/server.toml":                     "不得分发",
	}
	for _, artifact := range artifacts {
		files["dist/"+artifact] = "程序 " + artifact
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := createReleases(root); err != nil {
			t.Fatal(err)
		}
	}
	outDir := filepath.Join(root, "dist", "release")
	sums, err := os.ReadFile(filepath.Join(outDir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(sums)), "\n")) != len(artifacts) {
		t.Fatal("校验清单应包含全部四个平台")
	}
	for _, artifact := range artifacts {
		name := strings.TrimSuffix(artifact, ".exe") + ".zip"
		path := filepath.Join(outDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		wantSum := fmt.Sprintf("%x  %s\n", sha256.Sum256(body), name)
		if !strings.Contains(string(sums), wantSum) {
			t.Errorf("%s 校验和不匹配", name)
		}
		archive, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			artifact: files["dist/"+artifact], "README.md": files["README.md"],
			"LICENSE": files["LICENSE"], "THIRD_PARTY_NOTICES": files["THIRD_PARTY_NOTICES"],
			"server.example.toml": files["resources/server.example.toml"],
			"client.example.toml": files["resources/client.example.toml"],
		}
		for name, content := range files {
			if strings.HasPrefix(name, "docs/") {
				want[name] = content
			}
		}
		if len(archive.File) != len(want) {
			t.Errorf("%s 应只有清单中的 %d 个文件", name, len(want))
		}
		for _, f := range archive.File {
			expected, ok := want[f.Name]
			if !ok {
				t.Errorf("%s 包含清单外文件 %s", name, f.Name)
			}
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil || string(data) != expected {
				t.Errorf("%s 内容错误: %v", f.Name, err)
			}
			if f.Name == artifact && f.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s 执行权限丢失", artifact)
			}
			delete(want, f.Name)
		}
		archive.Close()
		if len(want) != 0 {
			t.Errorf("%s 缺少文件: %v", name, want)
		}
	}
}

// TestCreateReleasesMissingInput 验证缺少输入时直接失败，不创建发布目录。
func TestCreateReleasesMissingInput(t *testing.T) {
	root := t.TempDir()
	if err := createReleases(root); err == nil {
		t.Fatal("缺少构建产物与许可文件时应失败")
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "release")); !os.IsNotExist(err) {
		t.Errorf("输入不完整时不应创建发布目录: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createReleases(root); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Errorf("输入为目录时应明确报错，得到 %v", err)
	}
}
