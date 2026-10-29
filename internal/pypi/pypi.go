// Package pypi 实现 PyPI 协议相关的纯函数：PEP 503 包名规范化、
// uv 锁定文件解析、simple 索引（JSON/HTML 双格式）解析与发行文件名解析。
package pypi

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultIndexURL 是未做任何配置时的 PyPI 索引地址。
const DefaultIndexURL = "https://pypi.org/simple"

// sepRe 匹配包名中需要折叠的连续分隔符（PEP 503：- _ . 连续出现折叠为单个 -）。
var sepRe = regexp.MustCompile(`[-_.]+`)

// NormalizeName 返回 PEP 503 规范包名。
//
//	@param name string 原始包名，大小写与分隔符不限。
//	@return string 小写且将连续的 -_. 折叠为单个 - 的规范名。
func NormalizeName(name string) string {
	return sepRe.ReplaceAllString(strings.ToLower(name), "-")
}

// Req 表示锁定文件解析出的一条精确依赖（name==version）。
type Req struct {
	Name    string
	Version string
}

// ParseLock 解析 uv pip compile 输出的 requirements 锁定文件，提取全部 name==version。
//
// 仅识别行首非空白且形如 name==version 的行；uv 输出中缩进的 "# via ..." 注释行、
// "--hash=..." 续行、行内 "; marker" 与 "# 注释" 均忽略。结果按 name==version 去重。
//
//	@param r io.Reader 锁定文件内容。
//	@return []Req 去重后的依赖列表，顺序与文件中出现顺序一致。
//	@return error 读取失败时的错误；格式不识别的行静默跳过，不算错误。
func ParseLock(r io.Reader) ([]Req, error) {
	var reqs []Req
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "--") {
			continue
		}
		if i := strings.IndexAny(line, ";#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		name, version, ok := strings.Cut(line, "==")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		version = strings.TrimSpace(version)
		if name == "" || version == "" {
			continue
		}
		key := name + "==" + version
		if seen[key] {
			continue
		}
		seen[key] = true
		reqs = append(reqs, Req{Name: name, Version: version})
	}
	return reqs, sc.Err()
}

// File 描述 simple 索引中的一个发行文件。
type File struct {
	Filename     string // 文件名，如 requests-2.31.0-py3-none-any.whl
	URL          string // 下载直链
	SHA256       string // 十六进制小写哈希；来源缺失时为空串
	Yanked       bool   // 是否已被上游撤回（PEP 592）；JSON yanked 为真值或 HTML data-yanked 属性存在时为 true
	YankedReason string // 撤回原因；来源为布尔 yanked 或无值 data-yanked 属性时为空串
}

// simpleJSON 是 PEP 691 JSON simple 响应中用到的字段子集。
type simpleJSON struct {
	Files []struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		RequiresPython *string           `json:"requires-python"`
		Yanked         yankedValue       `json:"yanked"`
	} `json:"files"`
}

// yankedValue 是 PEP 691 JSON 索引的 yanked 字段值：布尔 false 表示未撤回；
// 非空字符串表示已撤回，内容为撤回原因（如 "Had a vulnerability"）。
type yankedValue struct {
	yanked bool
	reason string
}

// UnmarshalJSON 按 PEP 691 允许的两种形态解析 yanked 字段：布尔取其值，
// 字符串一律视为已撤回并保留原因；其他类型（含 null）不在规范允许范围内，报错。
func (y *yankedValue) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		y.yanked, y.reason = t, ""
	case string:
		y.yanked, y.reason = true, t
	default:
		return fmt.Errorf("yanked 应为布尔或字符串, 实际为 %T", v)
	}
	return nil
}

// anchorRe 从 PEP 503 HTML 索引中提取文件链接：捕获 a 标签的完整属性区与链接文本。
// 属性区整体捕获而非只锚定 href，使 data-yanked 等属性与 href 的先后顺序不影响解析。
var (
	anchorRe = regexp.MustCompile(`<a\s+([^>]*)>([^<]+)</a>`)
	hrefRe   = regexp.MustCompile(`href="([^"]*)"`)
)

// FetchSimple 请求索引上某包的 simple 页面并解析文件列表。
//
// 优先请求 PEP 691 JSON（Accept 协商），响应 Content-Type 含 json 时按 JSON 解析，
// 否则按 PEP 503 HTML 解析（sha256 取自 href 的 #sha256= 片段）。
//
//	@param ctx context.Context 控制请求生命周期的上下文。
//	@param client *http.Client 发起请求的客户端，超时等由其控制。
//	@param indexURL string 索引根地址，如 https://pypi.org/simple，尾斜杠可有可无。
//	@param name string 包名，任意大小写与分隔符写法，内部先规范化。
//	@return []File 该包全部发行文件，按文件名排序；包不存在时返回空切片。
//	@return error HTTP 请求失败或状态码非 2xx、JSON 解析失败时的错误。
func FetchSimple(ctx context.Context, client *http.Client, indexURL, name string) ([]File, error) {
	pageURL := strings.TrimRight(indexURL, "/") + "/" + NormalizeName(name) + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json, application/vnd.pypi.simple.v1+html; q=0.5, text/html; q=0.01")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 返回 %s", pageURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var files []File
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var sj simpleJSON
		if err := json.Unmarshal(body, &sj); err != nil {
			return nil, fmt.Errorf("解析 %s 的 JSON 失败: %w", pageURL, err)
		}
		for _, f := range sj.Files {
			files = append(files, File{
				Filename:     f.Filename,
				URL:          resolveURL(pageURL, f.URL),
				SHA256:       f.Hashes["sha256"],
				Yanked:       f.Yanked.yanked,
				YankedReason: f.Yanked.reason,
			})
		}
	} else {
		files = parseSimpleHTML(pageURL, body)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
	return files, nil
}

// resolveURL 将索引返回的相对地址解析为绝对地址。
//
// PEP 691 允许 JSON/HTML 中的 url/href 使用相对路径（相对 simple 页面地址），
// 官方源返回绝对地址，部分镜像（如清华源）返回相对地址，需统一处理。
//
//	@param pageURL string simple 页面地址，作为相对地址的基准。
//	@param raw string 索引返回的原始地址。
//	@return string 可直接请求的绝对地址；解析失败时原样返回。
func resolveURL(pageURL, raw string) string {
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if ref.IsAbs() {
		return raw
	}
	if base, err := url.Parse(pageURL); err == nil {
		return base.ResolveReference(ref).String()
	}
	return raw
}

// parseSimpleHTML 解析 PEP 503 HTML simple 页面。
//
//	@param pageURL string simple 页面地址，作为相对链接的基准。
//	@param body []byte 页面原始内容。
//	@return []File 提取到的文件列表；链接文本为文件名，sha256 取 href 片段，缺失为空；
//	data-yanked 属性（PEP 592）存在即标记撤回，属性值为撤回原因。
func parseSimpleHTML(pageURL string, body []byte) []File {
	var files []File
	for _, m := range anchorRe.FindAllStringSubmatch(string(body), -1) {
		attrs, text := m[1], strings.TrimSpace(m[2])
		if text == "" {
			continue
		}
		hm := hrefRe.FindStringSubmatch(attrs)
		if hm == nil {
			continue
		}
		href := hm[1]
		f := File{Filename: text, URL: resolveURL(pageURL, href)}
		if i := strings.Index(href, "#sha256="); i >= 0 {
			f.SHA256 = strings.TrimPrefix(href[i:], "#sha256=")
		}
		f.Yanked, f.YankedReason = yankedFromAttrs(attrs)
		files = append(files, f)
	}
	return files
}

// yankedFromAttrs 从 a 标签属性区提取撤回标记（PEP 592 的 data-yanked 属性）。
//
// 属性存在即已撤回，与 href 的先后顺序无关；属性可无值（布尔形式）或以
// 引号携带原因文本。原因按 HTML 实体解码（源页面中 >、& 等已转义）。
//
//	@param attrs string a 标签的完整属性文本。
//	@return bool 已撤回时为 true。
//	@return string 撤回原因；属性无值或未撤回时为空串。
func yankedFromAttrs(attrs string) (bool, string) {
	i := strings.Index(attrs, "data-yanked")
	if i < 0 {
		return false, ""
	}
	rest := strings.TrimLeft(attrs[i+len("data-yanked"):], " \t\r\n")
	if !strings.HasPrefix(rest, "=") {
		return true, "" // 无值的布尔属性形式：data-yanked
	}
	val := strings.TrimLeft(rest[1:], " \t\r\n")
	if len(val) > 0 && (val[0] == '"' || val[0] == '\'') {
		if j := strings.IndexByte(val[1:], val[0]); j >= 0 {
			return true, html.UnescapeString(val[1 : 1+j])
		}
		return true, "" // 引号未闭合，视为无值
	}
	if j := strings.IndexAny(val, " \t\r\n>"); j >= 0 {
		val = val[:j]
	}
	return true, html.UnescapeString(val)
}

// DistNameVersion 从 wheel 或 sdist 文件名中提取包名与版本。
//
//	@param filename string 发行文件名。
//	@return string 包名原文（未规范化）。
//	@return string 版本原文（wheel 文件名中 - 已被替换为 _）。
//	@return bool 文件名格式可识别时为 true；扩展名未知或段数不足时为 false。
func DistNameVersion(filename string) (string, string, bool) {
	lower := strings.ToLower(filename)
	// wheel：{name}-{version}[-{build}]-{python}-{abi}-{platform}.whl，至少 5 段，name/version 恒为前两段
	if strings.HasSuffix(lower, ".whl") {
		parts := strings.Split(strings.TrimSuffix(filename, ".whl"), "-")
		if len(parts) < 5 {
			return "", "", false
		}
		return parts[0], parts[1], true
	}
	// sdist：{name}-{version}.tar.gz|.tar.bz2|.zip，两段
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			name, version, ok := strings.Cut(strings.TrimSuffix(filename, ext), "-")
			if !ok || name == "" || version == "" {
				return "", "", false
			}
			return name, version, true
		}
	}
	return "", "", false
}

// MatchesReq 判断发行文件是否属于指定 name==version。
//
// 文件名中的包名按 PEP 503 规范化后比较；版本比较前将 - 统一替换为 _
// （wheel 文件名规范要求版本中的 - 写作 _）。
//
//	@param f File 待判断的发行文件。
//	@param name string 锁定文件中的包名。
//	@param version string 锁定文件中的版本号。
//	@return bool 文件属于该 name==version 时为 true。
func MatchesReq(f File, name, version string) bool {
	fname, fver, ok := DistNameVersion(f.Filename)
	if !ok {
		return false
	}
	if NormalizeName(fname) != NormalizeName(name) {
		return false
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "-", "_") }
	return norm(fver) == norm(version)
}

// WheelParts 按 PEP 427 拆分 wheel 文件名。
//
//	@param filename string wheel 文件名。
//	@return pythonTags []string python 标签（点分多值已展开，如 py2、py3）。
//	@return abi string ABI 段（如 none、cp312、abi3）。
//	@return platforms []string 平台标签（点分多值已展开，如 manylinux_2_17_x86_64）。
//	@return ok bool 文件名不是合法 wheel 时为 false。
func WheelParts(filename string) (pythonTags []string, abi string, platforms []string, ok bool) {
	if !strings.HasSuffix(strings.ToLower(filename), ".whl") {
		return nil, "", nil, false
	}
	parts := strings.Split(strings.TrimSuffix(filename, ".whl"), "-")
	if len(parts) < 5 {
		return nil, "", nil, false
	}
	// 有 build tag 时为 6 段，python/abi/platform 依次取末三段
	return strings.Split(parts[len(parts)-3], "."),
		parts[len(parts)-2],
		strings.Split(parts[len(parts)-1], "."),
		true
}

// osHints 把 --platform 的 os 值映射为平台标签中的特征子串。
var osHints = map[string][]string{
	"linux":   {"linux"},
	"windows": {"win"},
	"darwin":  {"macosx", "darwin"},
}

// archHints 把常见架构写法映射为平台标签中的特征子串。
var archHints = map[string][]string{
	"x86_64":  {"x86_64", "amd64"},
	"amd64":   {"x86_64", "amd64"},
	"aarch64": {"aarch64", "arm64"},
	"arm64":   {"aarch64", "arm64"},
	"i686":    {"i686", "win32"},
}

// WheelMatchesPython 判断 wheel 是否兼容指定的任一 CPython minor 版本。
//
// 兼容规则：通用标签 py3 匹配全部 3.x；py2 仅匹配 Python 2（恒不匹配）；
// pyN/cpN 精确匹配 minor N；ABI 段含 abi3 时 cpN 匹配 N 及更高 minor
// （abi3 向后兼容，如 cp311-abi3 的 wheel 可在 3.12 使用）。
//
//	@param filename string wheel 文件名。
//	@param minors []int 目标 CPython minor 版本列表（如 [11, 12]）。
//	@return bool 兼容任一目标版本时为 true。
func WheelMatchesPython(filename string, minors []int) bool {
	tags, abi, _, ok := WheelParts(filename)
	if !ok {
		return false
	}
	abi3 := strings.Contains(abi, "abi3")
	for _, tag := range tags {
		if tag == "py3" || tag == "py" {
			return true
		}
		if !strings.HasPrefix(tag, "py") && !strings.HasPrefix(tag, "cp") {
			continue
		}
		// 标签数字是 major+minor 的连写：py39 是 3.9（两位），cp312 是 3.12（三位）
		digits := tag[2:]
		n, err := strconv.Atoi(digits)
		if err != nil {
			continue
		}
		var major, minor int
		switch len(digits) {
		case 2:
			major, minor = n/10, n%10
		case 3:
			major, minor = n/100, n%100
		default:
			continue
		}
		if major != 3 {
			continue // 目标是 CPython 3.x；py2/cp2x 等 2.x 标签不兼容
		}
		for _, m := range minors {
			if minor == m || (strings.HasPrefix(tag, "cp") && abi3 && minor <= m) {
				return true
			}
		}
	}
	return false
}

// WheelMatchesPlatformArch 判断 wheel 平台标签是否命中 os 与架构过滤。
//
// 任一平台标签（复合标签已展开）同时命中两套过滤即匹配；"any" 标签
// （纯 Python wheel）不挑平台恒匹配。过滤集为空表示不过滤。
// --platform 值支持 linux/windows/darwin；--arch 值支持
// x86_64/amd64/aarch64/arm64/i686。
//
//	@param filename string wheel 文件名。
//	@param platforms []string os 过滤列表，空表示全部。
//	@param archs []string 架构过滤列表，空表示全部。
//	@return bool 命中过滤时为 true。
func WheelMatchesPlatformArch(filename string, platforms, archs []string) bool {
	_, _, tags, ok := WheelParts(filename)
	if !ok {
		return false
	}
	for _, tag := range tags {
		if tag == "any" {
			return true
		}
		lower := strings.ToLower(tag)
		osHit := len(platforms) == 0
		for _, osName := range platforms {
			for _, hint := range osHints[strings.ToLower(osName)] {
				if strings.Contains(lower, hint) {
					osHit = true
				}
			}
		}
		archHit := len(archs) == 0
		for _, archName := range archs {
			for _, hint := range archHints[strings.ToLower(archName)] {
				if strings.Contains(lower, hint) {
					archHit = true
				}
			}
		}
		if osHit && archHit {
			return true
		}
	}
	return false
}

// DepNamesFromArchive 从发行文件提取声明的依赖包名（级联删除建图用）。
//
// 支持三种形态：wheel（.whl，读 zip 内 *.dist-info/METADATA 单条目）、
// sdist tar 包（.tar.gz，顺序扫描取首条 PKG-INFO）、sdist zip（.zip）。
// zip 经 central directory 定位、tar 扫到即停，均不解压全文件。带环境
// marker 与 extras 的 Requires-Dist 全部计入（保守超集：边越多级联保留
// 越多，宁可少删）。依赖名经 PEP 503 规范化，与镜像目录名一致。
//
//	@param path string 发行文件路径（.whl/.tar.gz/.zip）。
//	@return []string 排序去重后的规范依赖包名；其他扩展名、无元数据或无依赖声明时为空。
//	@return error 归档打开、解压、定位或打开元数据条目失败时的错误。
func DepNamesFromArchive(path string) ([]string, error) {
	switch {
	case strings.HasSuffix(path, ".whl"):
		return depNamesFromZip(path, ".dist-info/METADATA")
	case strings.HasSuffix(path, ".tar.gz"):
		return depNamesFromSdistTar(path)
	case strings.HasSuffix(path, ".zip"):
		return depNamesFromZip(path, "/PKG-INFO")
	default:
		return nil, nil
	}
}

// depNamesFromZip 打开 zip 归档并读取名字以 suffix 结尾的首个条目。
//
// wheel 按 .dist-info/METADATA 后缀定位；sdist 按 /PKG-INFO 后缀定位，
// 同时接受根目录下名为 PKG-INFO 的条目。
//
//	@param path string zip 文件路径。
//	@param suffix string 目标条目名的结尾匹配（含斜杠）。
//	@return []string 解析出的依赖包名。
//	@return error 归档打开或条目读取失败时的错误。
func depNamesFromZip(path, suffix string) ([]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		ok := strings.HasSuffix(f.Name, suffix)
		// sdist 根 PKG-INFO（无斜杠）与内层 PKG-INFO 都接受
		if !ok && (suffix == "/PKG-INFO" && f.Name == "PKG-INFO") {
			ok = true
		}
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out := parseRequiresDist(rc)
		rc.Close()
		return out, nil
	}
	return nil, nil
}

// depNamesFromSdistTar 顺序扫描 sdist tar.gz，读首条 PKG-INFO。
//
//	@param path string tar.gz 文件路径。
//	@return []string 解析出的依赖包名。
//	@return error 文件打开、解压或读取失败时的错误。
func depNamesFromSdistTar(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "PKG-INFO" || strings.HasSuffix(hdr.Name, "/PKG-INFO") {
			return parseRequiresDist(tr), nil
		}
	}
}

// parseRequiresDist 从 METADATA / PKG-INFO 文本提取 Requires-Dist 声明的
// 依赖包名（PEP 503 规范化，排序去重）。
//
// 头部区以首个空行结束；Requires-Dist 值形如 "name (spec) ; marker" 或
// "name spec"，包名取首个空白或版本比较符之前的部分。头部名按不区分
// 大小写匹配（RFC 822 语义）。
//
//	@param r io.Reader 元数据文本。
//	@return []string 规范依赖包名列表。
func parseRequiresDist(r io.Reader) []string {
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Requires-Dist:") {
			continue
		}
		name := depNameOf(fields[1])
		if name == "" {
			continue
		}
		name = NormalizeName(name)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// depNameOf 从 Requires-Dist 的首字段剥离版本说明，得到裸包名。
//
// 兼容 "idna>=2.5"（无空格连写）、"idna (>=2.5,<4)"、'"weird name"' 等
// 形式；取 '('、引号与全部版本比较符（=<>!~）之前的部分。
//
//	@param field string Requires-Dist 值的首个空白分隔字段。
//	@return string 裸包名；剥离后为空时返回空串。
func depNameOf(field string) string {
	if i := strings.IndexAny(field, "(<>!=~\"'"); i >= 0 {
		field = field[:i]
	}
	return strings.TrimSpace(field)
}
