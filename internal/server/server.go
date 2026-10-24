// Package server 实现镜像服务端：PEP 503 simple 索引、CPython 安装包分发、
// npm registry 协议、Node dist 目录分发与浏览页面。数据即 root 下的文件
// 目录，无任何索引中间件。
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"pkgmirror/internal/node"
	"pkgmirror/internal/npm"
	"pkgmirror/internal/paths"
	"pkgmirror/internal/pypi"
)

// ufuzzyJS 是内嵌的 uFuzzy 库（列表页模糊搜索用），随二进制分发，
// 离线环境不产生外部请求。
//
//go:embed static/ufuzzy.min.js
var ufuzzyJS []byte

// Config 是服务端配置，对应 server.toml。
type Config struct {
	// Listen 是监听地址，默认 ":19999"。
	Listen string `toml:"listen"`
	// Root 是数据根目录，每种镜像类型占一个子目录，默认 "./data"。
	Root string `toml:"root"`
	// Users 是 Basic auth 用户表；空表表示不启用认证（启动时警告）。
	Users map[string]string `toml:"users"`
}

// LoadConfig 读取并解析配置文件，并把 Root 归一化为绝对路径。
//
// Root 的相对路径相对**配置文件所在目录**解析（配置在哪，数据跟在哪，
// 便携目录整体拷贝即迁移）；无配置文件时相对二进制目录。文件不存在时
// 不视为错误，返回内置默认值（由调用方决定是否警告）。
//
//	@param path string 配置文件路径，空串视为无配置文件。
//	@return Config 解析出的配置，Root 恒为绝对路径。
//	@return error 文件存在但 TOML 解析失败，或 Root 解析后为空时的错误。
func LoadConfig(path string) (Config, error) {
	cfg := Config{Listen: ":19999", Root: "./data", Users: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Root = absRoot("./data", paths.ExeDir())
			return cfg, nil
		}
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return cfg, fmt.Errorf("配置 %s 的 root 不能为空", path)
	}
	cfg.Root = absRoot(cfg.Root, filepath.Dir(path))
	return cfg, nil
}

// absRoot 把相对 root 解析为基于 base 的绝对路径；已是绝对路径时原样返回。
//
//	@param root string 配置中的 root 值。
//	@param base string 相对路径的基准目录（配置文件或二进制所在目录）。
//	@return string 绝对路径。
func absRoot(root, base string) string {
	if filepath.IsAbs(root) {
		return root
	}
	abs, err := filepath.Abs(filepath.Join(base, root))
	if err != nil {
		return root
	}
	return abs
}

// nameRe 规范化后的包名白名单（PEP 503 规范名只会包含小写字母数字与 - _ .）。
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// fileRe 发行文件与安装包文件名白名单（wheel/sdist/cpython 构建包均在此集合内）。
var fileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// tagRe 是 python-build-standalone 构建标签（8 位日期）。
var tagRe = regexp.MustCompile(`^[0-9]{8}$`)

// nodeVerRe 是 node 镜像的版本目录名（与官方 nodejs.org/dist 目录一致，v 前缀三段版本）。
var nodeVerRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Server 持有服务端配置并实现全部路由。
type Server struct {
	cfg Config
}

// New 创建服务端。构造后调用 Handler 获取可挂载的 http.Handler。
//
//	@param cfg Config 已解析的配置。
//	@return *Server 服务端实例。
func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

// Handler 组装全部路由并套上 Basic auth 中间件。
//
//	@return http.Handler 完整的 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/simple", s.handleSimpleRoot)
	mux.HandleFunc("/simple/", s.handleSimple)
	mux.HandleFunc("/pypi/", s.handlePypiFiles)
	mux.HandleFunc("/python", s.handlePythonBrowse)
	mux.HandleFunc("/python/", s.handlePython)
	mux.HandleFunc("/npm", s.handleNpm)
	mux.HandleFunc("/npm/", s.handleNpm)
	mux.HandleFunc("/node", s.handleNode)
	mux.HandleFunc("/node/", s.handleNode)
	mux.HandleFunc("/browse", s.handleBrowse)
	mux.HandleFunc("/browse/", s.handleBrowse)
	mux.HandleFunc("/upload", s.handleUploadPage)
	mux.HandleFunc("/api/upload", s.handleUploadAPI)
	// 精确路径注册：直接输出内嵌字节，不挂 FileServer，无目录遍历面
	mux.HandleFunc("/static/ufuzzy.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(ufuzzyJS)
	})
	return s.basicAuth(mux)
}

// clientIP 返回请求来源 IP，供访问与上传日志排障使用。
//
// 依次取 X-Forwarded-For 首段（反代追加链最左的原始客户端）、X-Real-IP、
// RemoteAddr 主机部分。两个转发头均可被直连客户端伪造，仅在直连或可信
// 反代部署下准确；只用于排障定位，不作访问控制依据。
//
//	@param r *http.Request 当前请求。
//	@return string 来源 IP；RemoteAddr 无端口可解析时返回其原文。
func clientIP(r *http.Request) string {
	if ip, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(ip) != "" {
		return strings.TrimSpace(ip)
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// logAccess 记录文件下载与资源缺失事件，包含来源 IP、方法、路径和大小。
//
// 下载事件在 ServeFile 前记录，不表示传输完成。浏览页、索引及元数据的
// 成功请求不记录，npm 和 Go 模块元数据缺失时记录“缺失”。
//
//	@param r *http.Request 当前请求（取来源 IP、方法与路径）。
//	@param event string 事件类型："下载" 或 "缺失"。
//	@param status int HTTP 状态码。
//	@param size int64 响应文件字节数；无文件体时为 0。
func logAccess(r *http.Request, event string, status int, size int64) {
	log.Printf("%s ip=%s %s %s %d %s", event, clientIP(r), r.Method, r.URL.EscapedPath(), status, humanSize(size))
}

// ListenAndServe 启动 HTTP 服务，阻塞直至出错。
//
//	@return error 监听或服务过程中的错误。
func (s *Server) ListenAndServe() error {
	for _, d := range []string{filepath.Join(s.cfg.Root, "pypi"), filepath.Join(s.cfg.Root, "python"), filepath.Join(s.cfg.Root, "npm"), filepath.Join(s.cfg.Root, "node")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("创建数据目录 %s 失败: %w", d, err)
		}
	}
	log.Printf("pkgmirror 服务端启动 监听=%s 数据目录=%s 认证=%s", s.cfg.Listen, s.cfg.Root, authDesc(s.cfg.Users))
	return http.ListenAndServe(s.cfg.Listen, s.Handler())
}

// authDesc 描述认证状态，用于启动日志。
func authDesc(users map[string]string) string {
	if len(users) == 0 {
		return "未启用（警告：任何人可浏览、下载、上传和删除）"
	}
	return fmt.Sprintf("已启用（%d 个用户）", len(users))
}

// basicAuth 是全站 Basic auth 中间件；Users 为空时直接放行。
func (s *Server) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.Users) > 0 {
			user, pass, ok := r.BasicAuth()
			// 无论用户是否存在都执行一次比较，避免按响应时间探测用户名
			expected, known := s.cfg.Users[user]
			if !known {
				expected = "pkgmirror-timing-equalizer"
			}
			if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(expected)) != 1 || !known {
				w.Header().Set("WWW-Authenticate", `Basic realm="pkgmirror"`)
				http.Error(w, "未授权", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleRoot 渲染首页。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	pkgs, _ := s.listPackages()
	tags, _ := s.listPythonTags()
	npmPkgs, _ := s.listNpmPackages()
	nodeVers, _ := s.listNodeVersions()
	// Host 取请求头，页面用它生成可直接复制的接入地址
	render(w, indexTpl, map[string]any{"Packages": len(pkgs), "Tags": len(tags), "NpmPackages": len(npmPkgs), "NodeVersions": len(nodeVers), "Host": r.Host})
}

// handleSimpleRoot 渲染 PEP 503 根索引（全部包名 anchor 列表）。
func (s *Server) handleSimpleRoot(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.listPackages()
	if err != nil {
		http.Error(w, "读取包目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"1.0\"><title>Simple index</title></head><body>\n")
	for _, name := range pkgs {
		fmt.Fprintf(&b, "<a href=\"/simple/%s/\">%s</a><br/>\n", name, name)
	}
	b.WriteString("</body></html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, b.String())
}

// handleSimple 渲染单个包的 PEP 503 文件页，href 指向 /pypi/ 并带 #sha256= 片段。
func (s *Server) handleSimple(w http.ResponseWriter, r *http.Request) {
	// /simple/ 与 /simple 均视为根索引（mux 前缀匹配会把两者都送进本函数）
	if r.URL.Path == "/simple/" || r.URL.Path == "/simple" {
		s.handleSimpleRoot(w, r)
		return
	}
	name := pypi.NormalizeName(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/simple/"), "/"))
	if !nameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	files, err := s.listDistFiles(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"1.0\"><title>Links for %s</title></head><body>\n<h1>Links for %s</h1>\n", name, name)
	for _, f := range files {
		fmt.Fprintf(&b, "<a href=\"/pypi/%s/%s#sha256=%s\">%s</a><br/>\n", name, f.Name, f.SHA256, f.Name)
	}
	b.WriteString("</body></html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, b.String())
}

// distFile 是包目录下的一个发行文件及其 sha256。
// 字段需导出：浏览页模板（html/template）无法访问未导出字段。
type distFile struct {
	Name   string
	SHA256 string
	Size   int64
}

// listDistFiles 列出某包目录下全部发行文件（跳过 .sha256 缓存与 .part 临时文件）。
//
//	@param name string PEP 503 规范包名。
//	@return []distFile 按文件名排序的文件列表，sha256 经缓存或现场计算。
//	@return error 目录不存在或读取失败时的错误。
func (s *Server) listDistFiles(name string) ([]distFile, error) {
	dir := filepath.Join(s.cfg.Root, "pypi", name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []distFile
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".sha256") || strings.HasPrefix(e.Name(), ".part-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		sum, err := s.fileSHA256(path)
		if err != nil {
			return nil, err
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, distFile{Name: e.Name(), SHA256: sum, Size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// fileSHA256 返回文件的 sha256（十六进制小写）。
//
// 优先读同名 .sha256 缓存文件；无缓存时现场计算并 best-effort 写回缓存，
// 缓存写失败只影响下次重复计算，不影响返回值。缓存文件的用途是让 simple
// 页面不必每次请求都重算大文件哈希；手工放入的文件首次访问时自动补缓存。
//
//	@param path string 文件绝对或相对路径。
//	@return string sha256 十六进制小写。
//	@return error 文件读取失败时的错误。
func (s *Server) fileSHA256(path string) (string, error) {
	if b, err := os.ReadFile(path + ".sha256"); err == nil {
		if sum := strings.TrimSpace(string(b)); sum != "" {
			return sum, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	_ = os.WriteFile(path+".sha256", []byte(sum+"\n"), 0o644)
	return sum, nil
}

// listPackages 列出 pypi/ 下全部规范包名。
//
//	@return []string 排序后的包名列表。
//	@return error 目录读取失败时的错误。
func (s *Server) listPackages() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "pypi"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && nameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// listPythonTags 列出 python/ 下全部构建标签。
//
//	@return []string 排序后的标签列表。
//	@return error 目录读取失败时的错误。
func (s *Server) listPythonTags() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "python"))
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, e := range entries {
		if e.IsDir() && tagRe.MatchString(e.Name()) {
			tags = append(tags, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tags)))
	return tags, nil
}

// handlePypiFiles 提供 PyPI 包文件下载。包名与文件名走白名单校验，天然无路径穿越。
// 下载与缺失均记录访问日志。
func (s *Server) handlePypiFiles(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/pypi/")
	name, filename, ok := strings.Cut(rest, "/")
	if !ok || !nameRe.MatchString(name) || !fileRe.MatchString(filename) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.Root, "pypi", name, filename)
	info, err := os.Stat(path)
	if err != nil {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	logAccess(r, "下载", http.StatusOK, info.Size())
	http.ServeFile(w, r, path)
}

// handlePython 提供安装包浏览列表与文件下载。
//
// /python/ 与 /python/{tag}/ 返回列表页；/python/{tag}/{file} 返回文件。
// 文件不存在时返回 404 状态码 + HTML 提示页，提示先在外网补下载再上传。
func (s *Server) handlePython(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/python/"), "/")
	if rest == "" {
		s.handlePythonBrowse(w, r)
		return
	}
	tag, filename, hasFile := strings.Cut(rest, "/")
	if !tagRe.MatchString(tag) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	if !hasFile {
		s.handlePythonBrowse(w, r)
		return
	}
	if !fileRe.MatchString(filename) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.Root, "python", tag, filename)
	info, err := os.Stat(path)
	if err != nil {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		renderBody(w, python404Tpl, map[string]any{"Path": r.URL.Path})
		return
	}
	logAccess(r, "下载", http.StatusOK, info.Size())
	http.ServeFile(w, r, path)
}

// pythonVersionOf 从 python-build-standalone 文件名提取 Python 版本。
//
// 文件名形如 cpython-3.12.15+20261021-aarch64-apple-darwin-install_only.tar.gz，
// 版本取第二段并去掉 +构建号 部分；无法识别时返回空串。
//
//	@param filename string 安装包文件名。
//	@return string 三段版本号（如 3.12.15）；无法解析时为空串。
func pythonVersionOf(filename string) string {
	parts := strings.SplitN(filename, "-", 3)
	if len(parts) < 2 || parts[0] != "cpython" {
		return ""
	}
	ver, _, _ := strings.Cut(parts[1], "+")
	return ver
}

// handlePythonBrowse 渲染安装包列表页（全部 tag 平铺）。
func (s *Server) handlePythonBrowse(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Tag      string
		Version  string
		Filename string
		Size     int64
	}
	var rows []row
	tags, err := s.listPythonTags()
	if err == nil {
		for _, tag := range tags {
			entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "python", tag))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || strings.HasSuffix(e.Name(), ".sha256") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				rows = append(rows, row{Tag: tag, Version: pythonVersionOf(e.Name()), Filename: e.Name(), Size: info.Size()})
			}
		}
	}
	render(w, pythonTpl, map[string]any{"Rows": rows, "Host": r.Host})
}

// handleNode 提供 Node 发行版分发：/node/ 与 /node/{vX.Y.Z}/ 为浏览页，
// /node/{vX.Y.Z}/{file} 为文件下载（含 SHASUMS256.txt）。
//
// 目录布局与官方 nodejs.org/dist 一致，nvm/nvm-windows/n/mise 等版本管理器
// 把镜像变量指向 /node 即可直接安装。文件不存在时返回 404 + HTML 提示页。
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/node/"), "/")
	if rest == "" || !strings.Contains(rest, "/") {
		s.handleNodeBrowse(w, r)
		return
	}
	version, filename, _ := strings.Cut(rest, "/")
	if !nodeVerRe.MatchString(version) || !fileRe.MatchString(filename) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.Root, "node", version, filename)
	info, err := os.Stat(path)
	if err != nil {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		renderBody(w, node404Tpl, map[string]any{"Path": r.URL.Path})
		return
	}
	logAccess(r, "下载", http.StatusOK, info.Size())
	http.ServeFile(w, r, path)
}

// listNodeVersions 列出 node/ 下全部版本目录（按 semver 降序）。
//
//	@return []string 版本目录名列表。
//	@return error 目录读取失败时的错误。
func (s *Server) listNodeVersions() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "node"))
	if err != nil {
		return nil, err
	}
	var vers []string
	for _, e := range entries {
		if e.IsDir() && nodeVerRe.MatchString(e.Name()) {
			vers = append(vers, e.Name())
		}
	}
	sort.Slice(vers, func(i, j int) bool { return node.CompareVersion(vers[i], vers[j]) > 0 })
	return vers, nil
}

// handleNodeBrowse 渲染 Node 安装包浏览页（全部版本平铺）。
func (s *Server) handleNodeBrowse(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Version  string
		Filename string
		Size     int64
	}
	var rows []row
	vers, err := s.listNodeVersions()
	if err == nil {
		for _, ver := range vers {
			entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "node", ver))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || strings.HasSuffix(e.Name(), ".sha256") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				rows = append(rows, row{Version: ver, Filename: e.Name(), Size: info.Size()})
			}
		}
	}
	render(w, nodeTpl, map[string]any{"Rows": rows, "Host": r.Host})
}

// handleNpm 提供 npm registry 协议：/npm/ 为浏览页，/npm/{name} 返回
// packument（tarball 地址重写为本镜像），/npm/{name}/-/{file} 返回 tarball。
//
// 必须用 r.URL.EscapedPath() 而非 r.URL.Path：scoped 包名的 / 在请求中
// 编码为 %2F，net/http 解码后 Path 中的 / 会与 tarball 路径的多段结构
// 混淆；EscapedPath 保留 %2F，先按 "/-/" 段切出文件部分，再解码包名。
// 两种请求形式（%2F 编码与裸 /）均兼容。
func (s *Server) handleNpm(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.EscapedPath(), "/npm/"), "/")
	if rest == "" {
		s.handleNpmBrowse(w, r)
		return
	}
	rawName, file, isFile := strings.Cut(rest, "/-/")
	name, err := url.PathUnescape(rawName)
	if err != nil || !npm.ValidName(name) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	if !isFile {
		s.servePackument(w, r, name)
		return
	}
	if !fileRe.MatchString(file) {
		logAccess(r, "缺失", http.StatusNotFound, 0)
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.Root, "npm", filepath.FromSlash(name), file)
	info, err := os.Stat(path)
	if err != nil {
		s.npmNotFound(w, r)
		return
	}
	logAccess(r, "下载", http.StatusOK, info.Size())
	http.ServeFile(w, r, path)
}

// servePackument 返回包的 packument，tarball 地址重写为本镜像地址。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param r *http.Request 请求（取 Host 拼镜像地址）。
//	@param name string 完整包名，可含 scope。
func (s *Server) servePackument(w http.ResponseWriter, r *http.Request, name string) {
	path := filepath.Join(s.cfg.Root, "npm", filepath.FromSlash(name), "packument.json")
	body, err := os.ReadFile(path)
	if err != nil {
		s.npmNotFound(w, r)
		return
	}
	// 镜像仅提供 HTTP（无 TLS），地址直接用 http + 请求 Host
	out, err := npm.RewriteTarballs(body, "http://"+r.Host, name)
	if err != nil {
		// 非法 packument：按原文返回并告警，npm 端会因版本缺失自行报错
		log.Printf("重写 %s 的 packument 失败，按原文返回: %v", name, err)
		out = body
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// npmNotFound 返回 404 状态码 + HTML 提示页，引导外网补下载，
// 并记录缺失日志（packument 与 tarball 的 404 都经由本函数）。
func (s *Server) npmNotFound(w http.ResponseWriter, r *http.Request) {
	logAccess(r, "缺失", http.StatusNotFound, 0)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	renderBody(w, npm404Tpl, map[string]any{"Path": r.URL.Path})
}

// npmPkgRow 是 npm 浏览页与首页统计的一行包数据。
type npmPkgRow struct {
	Name  string
	Count int
	Size  int64
}

// listNpmPackages 列出 npm/ 下全部包（含 scoped 包）及文件统计。
//
// 目录约定：普通包为 npm/{name}/；scope 目录 npm/@scope/{name}/ 表示
// 包 @scope/name。非目录项与非法包名跳过。
//
//	@return []npmPkgRow 按包名排序的列表。
//	@return error npm/ 目录读取失败时的错误。
func (s *Server) listNpmPackages() ([]npmPkgRow, error) {
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "npm"))
	if err != nil {
		return nil, err
	}
	var rows []npmPkgRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "@") {
			// scope 目录：其下每个子目录是一个包
			subs, err := os.ReadDir(filepath.Join(s.cfg.Root, "npm", e.Name()))
			if err != nil {
				continue
			}
			for _, se := range subs {
				name := e.Name() + "/" + se.Name()
				if !se.IsDir() || !npm.ValidName(name) {
					continue
				}
				rows = append(rows, s.npmPkgRow(name))
			}
			continue
		}
		if !npm.ValidName(e.Name()) {
			continue
		}
		rows = append(rows, s.npmPkgRow(e.Name()))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// npmPkgRow 统计单个包目录的文件数与总大小（跳过 .sha256 缓存与 .part 临时文件）。
//
//	@param name string 完整包名（斜杠分隔，可为 scoped 包）。
//	@return npmPkgRow 包名与统计值；目录不可读时计数为零。
func (s *Server) npmPkgRow(name string) npmPkgRow {
	row := npmPkgRow{Name: name}
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "npm", filepath.FromSlash(name)))
	if err != nil {
		return row
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".sha256") || strings.HasPrefix(e.Name(), ".part-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		row.Count++
		row.Size += info.Size()
	}
	return row
}

// handleNpmBrowse 渲染 npm 包浏览列表页。
func (s *Server) handleNpmBrowse(w http.ResponseWriter, r *http.Request) {
	rows, err := s.listNpmPackages()
	if err != nil {
		http.Error(w, "读取 npm 目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, npmBrowseTpl, map[string]any{"Rows": rows, "Host": r.Host})
}

// handleNpmPkgPage 渲染单个 npm 包的详情页（文件列表与下载链接）。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param r *http.Request 请求。
//	@param name string 完整包名，可含 scope；非法或未同步时 404。
func (s *Server) handleNpmPkgPage(w http.ResponseWriter, r *http.Request, name string) {
	if !npm.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(s.cfg.Root, "npm", filepath.FromSlash(name))
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.npmNotFound(w, r)
		return
	}
	var files []distFile
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".sha256") || strings.HasPrefix(e.Name(), ".part-") {
			continue
		}
		sum, err := s.fileSHA256(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, distFile{Name: e.Name(), SHA256: sum, Size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	render(w, npmPkgTpl, map[string]any{"Name": name, "Files": files})
}

// handleBrowse 渲染人用的包浏览页。
//
// /browse/ 为 PyPI 包列表；/browse/npm/ 转发 npm 包列表；
// /browse/npm/{name}/ 为 npm 包详情；其余 /browse/{name}/ 为 PyPI 包详情。
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/browse/"), "/")
	if rest == "npm" {
		s.handleNpmBrowse(w, r)
		return
	}
	if name, ok := strings.CutPrefix(rest, "npm/"); ok && name != "" {
		s.handleNpmPkgPage(w, r, name)
		return
	}
	if rest == "" {
		type row struct {
			Name  string
			Count int
			Size  int64
		}
		var rows []row
		names, err := s.listPackages()
		if err == nil {
			for _, name := range names {
				files, err := s.listDistFiles(name)
				if err != nil {
					continue
				}
				var total int64
				for _, f := range files {
					total += f.Size
				}
				rows = append(rows, row{Name: name, Count: len(files), Size: total})
			}
		}
		render(w, browseTpl, map[string]any{"Rows": rows, "Host": r.Host})
		return
	}
	name := pypi.NormalizeName(rest)
	files, err := s.listDistFiles(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	render(w, browsePkgTpl, map[string]any{"Name": name, "Files": files})
}

// handleUploadPage 渲染上传表单页；POST 也接收（表单直接提交到本页路径，便于带结果提示）。
func (s *Server) handleUploadPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleUploadAPI(w, r)
		return
	}
	render(w, uploadTpl, map[string]any{"Msg": r.URL.Query().Get("msg")})
}

// uploadResult 是 /api/upload 的 JSON 响应。
type uploadResult struct {
	Status string `json:"status"` // ok（新增）或 skipped（已存在同哈希）
	Path   string `json:"path"`   // 落盘相对路径
}

// handleUploadAPI 处理上传（客户端与网页共用）。
//
// multipart 表单字段：type（pypi|python|npm|node）、name（pypi/npm 包名）、
// tag（python 构建标签或 node 版本号）、sha256（期望哈希，可选）、file（文件
// 本体）。落盘前校验期望哈希；目标已存在且哈希一致时幂等跳过，不一致时返回
// 409 拒绝覆盖。写入采用 .part 临时文件 + rename 原子落盘。
func (s *Server) handleUploadAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "解析上传表单失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	typ := r.FormValue("type")
	name := r.FormValue("name")
	tag := r.FormValue("tag")
	wantHash := strings.ToLower(strings.TrimSpace(r.FormValue("sha256")))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "缺少文件字段 file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	// 纵深防御：Go 的 multipart 已将文件名剥为基名，此处再拦一次带分隔符的形式
	if strings.ContainsAny(hdr.Filename, "/\\") {
		http.Error(w, "文件名不能包含路径分隔符", http.StatusBadRequest)
		return
	}
	filename := filepath.Base(hdr.Filename)
	if !fileRe.MatchString(filename) {
		http.Error(w, "文件名包含非法字符", http.StatusBadRequest)
		return
	}

	var dest string // 相对 root 的落盘路径
	switch typ {
	case "pypi":
		if name == "" {
			// 网页上传内部包：从文件名首段 "-" 前取包名再规范化
			head, _, _ := strings.Cut(filename, "-")
			if head == "" {
				http.Error(w, "无法从文件名解析包名，请填写包名", http.StatusBadRequest)
				return
			}
			name = head
		}
		name = pypi.NormalizeName(name)
		if !nameRe.MatchString(name) {
			http.Error(w, "包名包含非法字符", http.StatusBadRequest)
			return
		}
		dest = filepath.Join("pypi", name, filename)
	case "python":
		if !tagRe.MatchString(tag) {
			http.Error(w, "python 上传的 tag 必须是 8 位日期数字", http.StatusBadRequest)
			return
		}
		dest = filepath.Join("python", tag, filename)
	case "npm":
		if name == "" {
			// scoped 包的 tarball 文件名不含 scope 前缀，无法从文件名反推包名
			http.Error(w, "npm 上传必须提供包名（scoped 包含 @scope/ 前缀）", http.StatusBadRequest)
			return
		}
		if !npm.ValidName(name) {
			http.Error(w, "npm 包名包含非法字符", http.StatusBadRequest)
			return
		}
		dest = filepath.Join("npm", filepath.FromSlash(name), filename)
	case "node":
		if !nodeVerRe.MatchString(tag) {
			http.Error(w, "node 上传的 tag 必须是 v 开头的三段版本（如 v22.14.0）", http.StatusBadRequest)
			return
		}
		dest = filepath.Join("node", tag, filename)
	default:
		http.Error(w, "type 必须是 pypi、python、npm 或 node", http.StatusBadRequest)
		return
	}

	result, status, err := s.storeUpload(dest, wantHash, clientIP(r), file)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") && r.Referer() != "" {
		// 网页表单提交：带结果提示重定向回上传页
		msg := fmt.Sprintf("上传成功: %s (%s)", result.Path, result.Status)
		http.Redirect(w, r, "/upload?msg="+msg, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// storeUpload 将上传内容原子写入 dest，处理哈希校验与幂等跳过。
//
//	@param dest string 相对 root 的目标路径（已通过白名单校验）。
//	@param wantHex string 调用方提供的期望 sha256，空串表示不校验。
//	@param ip string 上传来源 IP（clientIP 的结果），仅用于成功日志。
//	@param src io.Reader 文件内容。
//	@return uploadResult 落盘结果。
//	@return int 错误时的 HTTP 状态码，成功时忽略。
//	@return error 处理失败的原因；目标已存在但哈希不一致时状态码为 409。
func (s *Server) storeUpload(dest, wantHex, ip string, src io.Reader) (uploadResult, int, error) {
	abs := filepath.Join(s.cfg.Root, dest)
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return uploadResult{}, http.StatusInternalServerError, fmt.Errorf("创建目录失败: %w", err)
	}
	// 先写临时文件并计算实际哈希，再决定跳过/冲突/落盘
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return uploadResult{}, http.StatusInternalServerError, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后 Remove 报错可忽略
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), src); err != nil {
		tmp.Close()
		return uploadResult{}, http.StatusInternalServerError, fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return uploadResult{}, http.StatusInternalServerError, err
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if wantHex != "" && wantHex != gotHex {
		return uploadResult{}, http.StatusBadRequest, fmt.Errorf("哈希校验失败: 期望 %s 实际 %s", wantHex, gotHex)
	}
	if existing, err := s.fileSHA256(abs); err == nil {
		if existing == gotHex {
			return uploadResult{Status: "skipped", Path: dest}, 0, nil
		}
		return uploadResult{}, http.StatusConflict, fmt.Errorf("目标已存在且内容不同，如需覆盖请先人工删除: %s", abs)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return uploadResult{}, http.StatusInternalServerError, fmt.Errorf("落盘失败: %w", err)
	}
	_ = os.WriteFile(abs+".sha256", []byte(gotHex+"\n"), 0o644)
	log.Printf("已接收 ip=%s %s (sha256=%s)", ip, dest, gotHex[:12])
	return uploadResult{Status: "ok", Path: dest}, 0, nil
}
