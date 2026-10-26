// 打包工具从构建产物和项目文件中按固定清单生成 ZIP 及 SHA256SUMS。
package main

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// artifacts 列出两套构建脚本生成的四个平台文件名，顺序也是校验清单顺序。
var artifacts = []string{
	"pkgmirror-windows-amd64.exe",
	"pkgmirror-linux-amd64",
	"pkgmirror-linux-arm64",
	"pkgmirror-darwin-arm64",
}

// main 在项目根目录运行打包，失败时以非零状态退出。
func main() {
	if err := createReleases("."); err != nil {
		fmt.Fprintln(os.Stderr, "打包失败:", err)
		os.Exit(1)
	}
	fmt.Println("发布包及 SHA256SUMS 已生成到 dist/release/")
}

// createReleases 校验全部输入，再为每个平台生成发布包和 SHA256SUMS。
//
// 发布包包含程序、项目文档、两份许可文件和两份示例配置。只读取清单中的
// 文件，dist 下的镜像数据和其他文件不会进入发布包。
//
//	@param root string 项目根目录。
//	@return error 输入文件缺失、不是普通文件或打包、写盘失败时的错误。
func createReleases(root string) error {
	common := []string{
		"README.md", "LICENSE", "THIRD_PARTY_NOTICES",
		filepath.Join("resources", "server.example.toml"),
		filepath.Join("resources", "client.example.toml"),
		filepath.Join("docs", "使用指南.md"), filepath.Join("docs", "设计.md"),
		filepath.Join("docs", "npm设计.md"), filepath.Join("docs", "node设计.md"),
		filepath.Join("docs", "golang设计.md"), filepath.Join("docs", "路由设计.md"),
		filepath.Join("docs", "删除设计.md"), filepath.Join("docs", "页面与日志设计.md"),
	}
	inputs := append([]string{}, common...)
	for _, artifact := range artifacts {
		inputs = append(inputs, filepath.Join("dist", artifact))
	}
	for _, input := range inputs {
		info, err := os.Stat(filepath.Join(root, input))
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", input, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s 不是普通文件", input)
		}
	}
	outDir := filepath.Join(root, "dist", "release")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	var sums strings.Builder
	for _, artifact := range artifacts {
		files := []string{filepath.Join(root, "dist", artifact)}
		for _, input := range common {
			files = append(files, filepath.Join(root, input))
		}
		name := strings.TrimSuffix(artifact, ".exe") + ".zip"
		path := filepath.Join(outDir, name)
		if err := writeArchive(path, files); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", name, err)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, name)
	}
	return os.WriteFile(filepath.Join(outDir, "SHA256SUMS"), []byte(sums.String()), 0o644)
}

// writeArchive 按给定清单生成 ZIP，写完后替换目标文件，失败时清理临时文件。
//
//	@param dest string ZIP 输出路径，父目录须已存在。
//	@param files []string 输入文件路径，文档保留 docs/ 目录，其余以文件基名存放；程序权限为 0755，其余为 0644。
//	@return error 文件读取、压缩、关闭或重命名失败时的错误。
func writeArchive(dest string, files []string) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".release-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	w := zip.NewWriter(f)
	for _, path := range files {
		if err := addFile(w, path); err != nil {
			w.Close()
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// addFile 将一个文件写入 ZIP，读取结束或出错时关闭输入文件。
//
//	@param w *zip.Writer 归档写入器，由调用方关闭。
//	@param path string 输入文件路径。
//	@return error 文件打开、属性读取、ZIP 条目创建或复制失败时的错误。
func addFile(w *zip.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.Base(path)
	if filepath.Base(filepath.Dir(path)) == "docs" {
		header.Name = "docs/" + header.Name
	}
	header.Method = zip.Deflate
	// Windows 文件权限不含 Unix 执行位，跨平台打包时按发布文件用途设置。
	header.SetMode(0o644)
	if strings.HasPrefix(header.Name, "pkgmirror-") {
		header.SetMode(0o755)
	}
	entry, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, f)
	return err
}

// fileSHA256 计算发布包的 SHA256，读取结束或出错时关闭文件。
//
//	@param path string 发布包路径。
//	@return string 小写十六进制摘要。
//	@return error 文件打开或读取失败时的错误。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
