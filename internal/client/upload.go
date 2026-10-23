package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"pymirror/internal/redact"
	"strings"
	"time"

	"pymirror/internal/manifest"
)

// UploadOptions 是 upload 子命令的全部输入。
type UploadOptions struct {
	// Dir 是下载产出目录路径（内含 manifest.json）。
	Dir string
	// Server 是服务端基础地址，如 http://mirror.internal:8080，
	// 可内嵌凭据（http://user:pass@host），内嵌凭据优先于 User/Pass。
	Server string
	// User 是 Basic auth 用户名。
	User string
	// Pass 是 Basic auth 密码。
	Pass string
}

// uploadClientTimeout 覆盖单次上传请求（大文件）的最长时间。
const uploadClientTimeout = 30 * time.Minute

// RunUpload 按 manifest 将下载产出目录内容上传到服务端。
//
// 服务端对已存在且哈希一致的文件幂等跳过，可安全重复执行；
// 任一文件上传失败时继续尝试其余文件，最后汇总失败清单并报错。
//
//	@param opts UploadOptions 已解析的选项。
//	@return error 存在上传失败文件时的汇总错误；全部成功（含跳过）时为 nil。
func RunUpload(opts UploadOptions) error {
	m, err := manifest.Load(opts.Dir)
	if err != nil {
		return err
	}
	base, user, pass, err := parseServer(opts)
	if err != nil {
		return err
	}
	// 上传目标是内网地址，显式禁用代理，避免外网代理环境变量劫持内网请求
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	client := &http.Client{Timeout: uploadClientTimeout, Transport: tr}

	var okCount, skipCount int
	var failures []string
	for i, f := range m.Files {
		localPath := filepath.Join(opts.Dir, filepath.FromSlash(f.Path))
		status, err := uploadOne(client, base, user, pass, localPath, f)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", f.Path, err))
			log.Printf("[%d/%d] 失败 %s: %s", i+1, len(m.Files), f.Path, redact.Text(err.Error()))
			continue
		}
		if status == "skipped" {
			skipCount++
			log.Printf("[%d/%d] 跳过 %s（服务端已存在相同文件）", i+1, len(m.Files), f.Path)
		} else {
			okCount++
			log.Printf("[%d/%d] 完成 %s", i+1, len(m.Files), f.Path)
		}
	}
	log.Printf("上传完成：共 %d 个文件，新增 %d，跳过 %d，失败 %d", len(m.Files), okCount, skipCount, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("以下 %d 个文件上传失败：\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return nil
}

// parseServer 解析服务端地址与凭据。
//
// URL 内嵌凭据优先于 --user/--pass 参数。
//
//	@param opts UploadOptions 上传选项。
//	@return string 服务端基础地址（已去除内嵌凭据）。
//	@return string Basic auth 用户名，未配置时为空。
//	@return string Basic auth 密码，未配置时为空。
//	@return error 服务端地址格式非法时的错误。
func parseServer(opts UploadOptions) (base, user, pass string, err error) {
	raw := strings.TrimRight(opts.Server, "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("服务端地址 %q 非法", redact.URL(opts.Server))
	}
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	if user == "" {
		user, pass = opts.User, opts.Pass
	}
	u.User = nil
	return u.String(), user, pass, nil
}

// uploadOne 上传单个文件。
//
//	@param client *http.Client HTTP 客户端。
//	@param base string 服务端基础地址。
//	@param user string Basic auth 用户名，可空。
//	@param pass string Basic auth 密码，可空。
//	@param path string 本地文件路径。
//	@param f manifest.File 对应的清单条目（提供 type/name/tag/sha256）。
//	@return string 服务端处理结果："ok"（新增）或 "skipped"（已存在同哈希）。
//	@return error 本地读取、网络请求、非 2xx 响应或响应解析失败时的原因。
func uploadOne(client *http.Client, base, user, pass, path string, f manifest.File) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取本地文件失败: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("type", f.Type)
	mw.WriteField("name", f.Name)
	mw.WriteField("tag", f.Tag)
	mw.WriteField("sha256", f.SHA256)
	fw, err := mw.CreateFormFile("file", f.Filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, file); err != nil {
		return "", fmt.Errorf("读取本地文件失败: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/upload", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("服务端返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析服务端响应失败: %w", err)
	}
	return result.Status, nil
}
