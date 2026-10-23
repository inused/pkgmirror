package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer 建立临时数据目录的测试服务端。
func newTestServer(t *testing.T, users map[string]string) (*Server, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"pypi", "python"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if users == nil {
		users = map[string]string{}
	}
	s := New(Config{Listen: ":0", Root: root, Users: users})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, root
}

// postUpload 模拟一次上传请求。
func postUpload(t *testing.T, url, user, pass, typ, name, tag, wantSHA, filename string, content []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("type", typ)
	mw.WriteField("name", name)
	mw.WriteField("tag", tag)
	mw.WriteField("sha256", wantSHA)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(content)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, url+"/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuth(t *testing.T) {
	_, ts, _ := newTestServer(t, map[string]string{"testuser": "test-password"})
	// 无凭据 401
	resp, _ := http.Get(ts.URL + "/simple/")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无凭据应 401, 得到 %d", resp.StatusCode)
	}
	// 错误密码 401
	resp = postUpload(t, ts.URL, "testuser", "wrong", "pypi", "x", "", "", "a-1.0.tar.gz", []byte("x"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("错误密码应 401, 得到 %d", resp.StatusCode)
	}
	// 正确凭据 200
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/simple/", nil)
	req.SetBasicAuth("testuser", "test-password")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("正确凭据应 200, 得到 %d", resp.StatusCode)
	}
}

func TestUploadPypiAndSimple(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	content := []byte("fake wheel content")
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	// name 留空，服务端应从文件名解析包名并规范化
	resp := postUpload(t, ts.URL, "", "", "pypi", "", "", want, "Zope_Interface-6.1-py3-none-any.whl", content)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("pypi 上传失败: %d %s", resp.StatusCode, body)
	}
	// 落盘路径：规范化包名
	if _, err := os.Stat(filepath.Join(root, "pypi", "zope-interface", "Zope_Interface-6.1-py3-none-any.whl")); err != nil {
		t.Fatalf("未按规范包名落盘: %v", err)
	}

	// simple 根索引与包页面
	resp, _ = http.Get(ts.URL + "/simple/")
	rootPage, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(rootPage), ">zope-interface<") {
		t.Error("simple 根索引缺少包名")
	}
	resp, _ = http.Get(ts.URL + "/simple/zope-interface/")
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), `href="/pypi/zope-interface/Zope_Interface-6.1-py3-none-any.whl#sha256=`+want) {
		t.Errorf("simple 包页面 anchor 错误: %s", page)
	}
	if !strings.Contains(string(page), "content=\"1.0\"") {
		t.Error("simple 页面缺少 pypi:repository-version meta")
	}

	// 文件下载
	resp, _ = http.Get(ts.URL + "/pypi/zope-interface/Zope_Interface-6.1-py3-none-any.whl")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("包文件下载应 200, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUploadIdempotentAndConflict(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	// 第一次上传
	resp := postUpload(t, ts.URL, "", "", "pypi", "demo", "", "", "demo-1.0.tar.gz", []byte("v1"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("首次上传应 ok: %s", body)
	}
	// 相同内容重复上传 → skipped
	resp = postUpload(t, ts.URL, "", "", "pypi", "demo", "", "", "demo-1.0.tar.gz", []byte("v1"))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"status":"skipped"`) {
		t.Fatalf("重复上传应 skipped: %s", body)
	}
	// 同名不同内容 → 409，且原文件不被覆盖
	resp = postUpload(t, ts.URL, "", "", "pypi", "demo", "", "", "demo-1.0.tar.gz", []byte("v2"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("内容冲突应 409, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	got, _ := os.ReadFile(filepath.Join(root, "pypi", "demo", "demo-1.0.tar.gz"))
	if string(got) != "v1" {
		t.Error("冲突上传不应覆盖原文件")
	}
	// sha256 校验失败 → 400
	bad := strings.Repeat("f", 64)
	resp = postUpload(t, ts.URL, "", "", "pypi", "demo2", "", bad, "demo2-1.0.tar.gz", []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("哈希不符应 400, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUploadPythonAndValidation(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	resp := postUpload(t, ts.URL, "", "", "python", "", "20261021", "", "cpython-3.13.1-linux.tar.gz", []byte("py"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("python 上传失败: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "python", "20261021", "cpython-3.13.1-linux.tar.gz")); err != nil {
		t.Fatal("python 未按 tag 落盘")
	}
	// 下载成功
	resp, _ = http.Get(ts.URL + "/python/20261021/cpython-3.13.1-linux.tar.gz")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("python 文件下载应 200, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	// 未同步文件 → 404 提示页
	resp, _ = http.Get(ts.URL + "/python/20260101/cpython-3.12.9-windows.tar.gz")
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(page), "文件未同步") {
		t.Errorf("未同步文件应 404+提示页, 得到 %d", resp.StatusCode)
	}
	// 非法 tag → 400
	resp = postUpload(t, ts.URL, "", "", "python", "", "../../etc", "", "a.tar.gz", []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 tag 应 400, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	// 路径穿越文件名：Go 的 multipart 在服务端侧已剥离路径部分，
	// 实际行为是按基名接收且不得落盘到包目录之外
	resp = postUpload(t, ts.URL, "", "", "pypi", "demo", "", "", "../../escape.tar.gz", []byte("x"))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("基名剥离后应正常接收, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "pypi", "demo", "escape.tar.gz")); err != nil {
		t.Error("应按基名落盘在包目录内")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.tar.gz")); err == nil {
		t.Error("出现目录逃逸")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	os.WriteFile(path, []byte("listen = \":9999\"\nroot = \"./d\"\n[users]\ntestuser = \"p\"\n"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9999" || cfg.Root != "./d" || cfg.Users["testuser"] != "p" {
		t.Errorf("配置解析错误: %+v", cfg)
	}
	// 不存在 → 默认值
	cfg, err = LoadConfig(filepath.Join(dir, "none.toml"))
	if err != nil || cfg.Listen != ":8080" || cfg.Root != "./data" {
		t.Errorf("缺失配置应返回默认值: %+v err=%v", cfg, err)
	}
	// 非法 TOML → 报错
	bad := filepath.Join(dir, "bad.toml")
	os.WriteFile(bad, []byte("listen = "), 0o644)
	if _, err := LoadConfig(bad); err == nil {
		t.Error("非法 TOML 应报错")
	}
}
