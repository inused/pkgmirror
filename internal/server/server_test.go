package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
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
	for _, d := range []string{"pypi", "python", "npm"} {
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

func TestNpmUploadAndServe(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	// scoped 包的 packument 与 tarball：tarball 文件名不含 scope 前缀
	packument := `{"dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"dist":{"tarball":"https://registry.npmjs.org/@scope/pkg/-/pkg-1.0.0.tgz","integrity":"sha512-x"}}}}`
	tarball := []byte("fake tgz content")
	pSum := sha256.Sum256([]byte(packument))
	tSum := sha256.Sum256(tarball)

	resp := postUpload(t, ts.URL, "", "", "npm", "@scope/pkg", "", hex.EncodeToString(pSum[:]), "packument.json", []byte(packument))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("packument 上传失败: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = postUpload(t, ts.URL, "", "", "npm", "@scope/pkg", "", hex.EncodeToString(tSum[:]), "pkg-1.0.0.tgz", tarball)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tarball 上传失败: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "npm", "@scope", "pkg", "packument.json")); err != nil {
		t.Fatalf("packument 未按 scoped 路径落盘: %v", err)
	}

	// packument 请求（%2F 编码形式）：tarball 地址重写为本镜像
	host := strings.TrimPrefix(ts.URL, "http://")
	resp, _ = http.Get(ts.URL + "/npm/@scope%2Fpkg")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("packument 应 200, 得到 %d", resp.StatusCode)
	}
	want := "http://" + host + "/npm/@scope/pkg/-/pkg-1.0.0.tgz"
	if !strings.Contains(string(body), want) {
		t.Errorf("tarball 地址未重写为 %s: %s", want, body)
	}
	if strings.Contains(string(body), "registry.npmjs.org") {
		t.Errorf("packument 仍含官方地址: %s", body)
	}

	// tarball 下载：编码与未编码两种路径形式
	for _, p := range []string{"/npm/@scope%2Fpkg/-/pkg-1.0.0.tgz", "/npm/@scope/pkg/-/pkg-1.0.0.tgz"} {
		resp, _ = http.Get(ts.URL + p)
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(got) != string(tarball) {
			t.Errorf("tarball 下载失败 %s: %d", p, resp.StatusCode)
		}
	}

	// 非 scoped 包 packument（无 /-/ 段）
	resp, _ = http.Get(ts.URL + "/npm/@scope%2Fpkg")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("非文件形式请求应返回 packument, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 未同步包 → 404 提示页
	resp, _ = http.Get(ts.URL + "/npm/missing-pkg")
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(page), "文件未同步") {
		t.Errorf("未同步包应 404+提示页, 得到 %d", resp.StatusCode)
	}

	// npm 上传必须提供包名（tarball 名不含 scope，无法反推）
	resp = postUpload(t, ts.URL, "", "", "npm", "", "", "", "pkg-1.0.0.tgz", tarball)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("npm 无包名应 400, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	// 非法包名
	resp = postUpload(t, ts.URL, "", "", "npm", "../evil", "", "", "pkg-1.0.0.tgz", tarball)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 npm 包名应 400, 得到 %d", resp.StatusCode)
	}
	resp.Body.Close()
	// 首页与浏览页包含 npm 统计
	resp, _ = http.Get(ts.URL + "/npm/")
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "@scope/pkg") {
		t.Errorf("npm 浏览页应列出包, 得到 %d", resp.StatusCode)
	}
	// 包详情页展示文件列表与下载链接
	resp, _ = http.Get(ts.URL + "/browse/npm/@scope/pkg/")
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(page), "pkg-1.0.0.tgz") ||
		!strings.Contains(string(page), `href="/npm/@scope/pkg/-/packument.json"`) {
		t.Errorf("npm 详情页应列出文件与下载链接, 得到 %d: %s", resp.StatusCode, page)
	}
	// packument.json 经 /-/ 路由可下载原始文件（与 packument 协议端点互不影响）
	resp, _ = http.Get(ts.URL + "/npm/@scope%2Fpkg/-/packument.json")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "registry.npmjs.org") {
		t.Errorf("原始 packument 下载失败或内容不符: %d", resp.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	// X-Forwarded-For 多段：取最左一段（原始客户端），去除空白
	req.RemoteAddr, req.Header = "10.0.0.2:1234", http.Header{}
	req.Header.Set("X-Forwarded-For", " 192.168.1.10 , 10.0.0.1 ")
	if got := clientIP(req); got != "192.168.1.10" {
		t.Errorf("XFF 多段应取最左: %q", got)
	}
	// X-Forwarded-For 首段为空时回退 X-Real-IP
	req.Header.Set("X-Forwarded-For", ", 10.0.0.1")
	req.Header.Set("X-Real-IP", "192.168.1.20")
	if got := clientIP(req); got != "192.168.1.20" {
		t.Errorf("XFF 空段应回退 X-Real-IP: %q", got)
	}
	// 无转发头：取 RemoteAddr 主机部分
	req.Header = http.Header{}
	if got := clientIP(req); got != "10.0.0.2" {
		t.Errorf("应取 RemoteAddr 主机: %q", got)
	}
	// RemoteAddr 无端口：返回原文
	req.RemoteAddr = "unix-socket"
	if got := clientIP(req); got != "unix-socket" {
		t.Errorf("无端口时应返回原文: %q", got)
	}
}

// captureLog 把标准日志重定向到缓冲，返回恢复函数与缓冲。
func captureLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	return &buf, func() { log.SetOutput(old) }
}

func TestAccessLog(t *testing.T) {
	_, ts, _ := newTestServer(t, nil)
	buf, restore := captureLog(t)
	defer restore()

	// pypi 文件下载成功 → 下载日志（含来源 IP 与 humanSize 大小）
	content := []byte("wheel bytes")
	sum := sha256.Sum256(content)
	resp := postUpload(t, ts.URL, "", "", "pypi", "demo", "", hex.EncodeToString(sum[:]), "demo-1.0.tar.gz", content)
	resp.Body.Close()
	resp, _ = http.Get(ts.URL + "/pypi/demo/demo-1.0.tar.gz")
	resp.Body.Close()
	logs := buf.String()
	if !strings.Contains(logs, "已接收 ip=127.0.0.1 pypi/demo/demo-1.0.tar.gz") {
		t.Errorf("上传日志应含来源 IP: %q", logs)
	}
	if !strings.Contains(logs, "下载 ip=127.0.0.1 GET /pypi/demo/demo-1.0.tar.gz 200 11B") {
		t.Errorf("下载日志格式不符: %q", logs)
	}

	// python 未同步 → 缺失日志
	buf.Reset()
	resp, _ = http.Get(ts.URL + "/python/20260101/cpython-3.12.9-linux.tar.gz")
	resp.Body.Close()
	if logs = buf.String(); !strings.Contains(logs, "缺失 ip=127.0.0.1 GET /python/20260101/cpython-3.12.9-linux.tar.gz 404 0B") {
		t.Errorf("缺失日志格式不符: %q", logs)
	}

	// npm packument 未同步 → 缺失日志（经 npmNotFound）
	buf.Reset()
	resp, _ = http.Get(ts.URL + "/npm/missing-pkg")
	resp.Body.Close()
	if logs = buf.String(); !strings.Contains(logs, "缺失 ip=127.0.0.1 GET /npm/missing-pkg 404 0B") {
		t.Errorf("npm 缺失日志格式不符: %q", logs)
	}

	// 反代场景：X-Forwarded-For 透传到下载日志
	buf.Reset()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/pypi/demo/demo-1.0.tar.gz", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.30, 10.0.0.1")
	http.DefaultClient.Do(req)
	if logs = buf.String(); !strings.Contains(logs, "下载 ip=192.168.1.30") {
		t.Errorf("XFF 应透传到下载日志: %q", logs)
	}
}

func TestNavGroupAndFilter(t *testing.T) {
	_, ts, _ := newTestServer(t, nil)
	// 造一个 pypi 包让列表页输出表格与过滤框
	content := []byte("x")
	sum := sha256.Sum256(content)
	resp := postUpload(t, ts.URL, "", "", "pypi", "demo", "", hex.EncodeToString(sum[:]), "demo-1.0.tar.gz", content)
	resp.Body.Close()

	// 导航分组：无品牌文字；组名带 cut 分隔类，语言本体在前、包在后
	page, _ := http.Get(ts.URL + "/browse/")
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	html := string(body)
	pyPos := strings.Index(html, `<nav><a href="/">首页</a><b class="cut">Python</b><a href="/python/">安装包</a><a href="/browse/">PyPI 包</a><b class="cut">Node.js</b><a href="/npm/">npm 包</a><a class="cut" href="/upload">上传</a></nav>`)
	if pyPos < 0 || strings.Contains(html, `<b>pkgmirror</b>`) {
		t.Errorf("导航分组结构不符:\n%s", html)
	}

	// 首页导航位于标题块之前
	page, _ = http.Get(ts.URL + "/")
	body, _ = io.ReadAll(page.Body)
	page.Body.Close()
	home := string(body)
	navPos := strings.Index(home, `<nav><a href="/">首页</a>`)
	titlePos := strings.Index(home, `<h1>pkgmirror</h1>`)
	if navPos < 0 || titlePos < navPos {
		t.Errorf("首页导航应在标题块之前: nav@%d title@%d", navPos, titlePos)
	}

	// 列表页含过滤框与 uFuzzy 脚本引用
	for _, must := range []string{`id="pkg-filter"`, `id="pkg-filter-hint"`, `src="/static/ufuzzy.min.js"`} {
		if !strings.Contains(html, must) {
			t.Errorf("列表页缺少 %s", must)
		}
	}

	// 静态资源返回完整内嵌内容
	page, _ = http.Get(ts.URL + "/static/ufuzzy.min.js")
	body, _ = io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || len(body) != len(ufuzzyJS) {
		t.Errorf("ufuzzy.min.js 应 200 且内容完整: %d %d/%d", page.StatusCode, len(body), len(ufuzzyJS))
	}

	// Python 列表页同样具备过滤能力（无数据时无表格，本例只验证脚本引用）
	page, _ = http.Get(ts.URL + "/python/")
	body, _ = io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), `src="/static/ufuzzy.min.js"`) {
		t.Error("python 列表页缺少 uFuzzy 脚本引用")
	}
}

// TestLoadConfig 覆盖配置解析、数据路径基准、默认值与非法 TOML。
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	os.WriteFile(path, []byte("listen = \":9999\"\nroot = \"./d\"\n[users]\ntestuser = \"p\"\n"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// root 相对路径相对配置文件目录解析，归一化为绝对路径
	if cfg.Listen != ":9999" || cfg.Root != filepath.Join(dir, "d") || cfg.Users["testuser"] != "p" {
		t.Errorf("配置解析错误: %+v", cfg)
	}
	// 配置内的绝对 root 原样保留
	os.WriteFile(path, []byte("root = \"/srv/mirror\"\n"), 0o644)
	cfg, err = LoadConfig(path)
	if err != nil || cfg.Root != "/srv/mirror" {
		t.Errorf("绝对 root 应原样保留: %+v err=%v", cfg, err)
	}
	// 不存在 → 默认值（root 相对二进制目录，绝对路径）
	cfg, err = LoadConfig(filepath.Join(dir, "none.toml"))
	if err != nil || cfg.Listen != ":19999" || !filepath.IsAbs(cfg.Root) || filepath.Base(cfg.Root) != "data" {
		t.Errorf("缺失配置应返回默认值: %+v err=%v", cfg, err)
	}
	// 非法 TOML → 报错
	bad := filepath.Join(dir, "bad.toml")
	os.WriteFile(bad, []byte("listen = "), 0o644)
	if _, err := LoadConfig(bad); err == nil {
		t.Error("非法 TOML 应报错")
	}
}

// TestNavCurrentScript 验证导航高亮：全部人用页面均输出 cur 样式与
// 内联匹配脚本（匹配逻辑由浏览器执行，这里只验证注入完整）。
func TestNavCurrentScript(t *testing.T) {
	_, ts, _ := newTestServer(t, nil)
	pages := []string{"/", "/python/", "/browse/", "/npm/", "/upload"}
	for _, p := range pages {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "nav a.cur{") {
			t.Errorf("%s 应含选中态样式", p)
		}
		if !strings.Contains(string(body), "links[best].className+=' cur'") {
			t.Errorf("%s 应含高亮脚本", p)
		}
	}
}
