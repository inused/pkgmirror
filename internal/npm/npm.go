// Package npm 实现 npm registry 协议相关的纯函数与请求：包名校验、
// package-lock 解析、packument 获取、tarball 地址重写与 SRI 哈希校验。
// 服务端与下载客户端共用本包，不持有状态。
package npm

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// DefaultRegistry 是未做任何配置时的 npm registry 地址。
const DefaultRegistry = "https://registry.npmjs.org"

// nameRe 是合法 npm 包名：可选 @scope/ 前缀，scope 与名各以小写字母、
// 数字、- 或 ~ 开头，后续允许小写字母数字与 -._~。
var nameRe = regexp.MustCompile(`^(@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)

// ValidName 判断包名是否为合法 npm 包名（scoped 形如 @scope/name）。
//
// 214 是 npm 对包名（含 scope）的总长度上限。
//
//	@param name string 待校验的包名。
//	@return bool 合法时为 true。
func ValidName(name string) bool {
	return len(name) <= 214 && nameRe.MatchString(name)
}

// EscapeName 把包名编码为 registry 请求路径段：scope 中的 / 编码为 %2F，
// 非 scoped 包名原样返回。
//
//	@param name string 完整包名，可含 scope。
//	@return string 可直接拼在 registry 地址后的路径段。
func EscapeName(name string) string {
	return url.PathEscape(name)
}

// Entry 是 package-lock 解析出的一条依赖。
type Entry struct {
	Name      string   // 完整包名，scoped 时为 @scope/name
	Version   string   // 锁定版本号
	Integrity string   // SRI（sha512-base64）；老 lock 可能缺失，下载时以 packument 为准
	OS        []string // lock 标注的兼容 os（npm 取值：linux/win32/darwin 等），空为不限
	CPU       []string // lock 标注的兼容 cpu（npm 取值：x64/arm64/ia32 等），空为不限
}

// lockPkg 是 package-lock.json packages 节点中用到的字段子集。
type lockPkg struct {
	Version   string   `json:"version"`
	Resolved  string   `json:"resolved"`
	Integrity string   `json:"integrity"`
	Link      bool     `json:"link"`
	OS        []string `json:"os"`
	CPU       []string `json:"cpu"`
}

// lockJSON 是 package-lock.json 顶层用到的字段子集。
type lockJSON struct {
	LockfileVersion int                  `json:"lockfileVersion"`
	Packages        map[string]lockPkg   `json:"packages"`
	Dependencies    map[string]lockPkgV1 `json:"dependencies"`
}

// lockPkgV1 占位：lockfileVersion 1 的嵌套 dependencies 结构不解析，仅用于探测。
type lockPkgV1 struct{}

// ParseLock 解析 package-lock.json（lockfileVersion 2/3）的 packages 节点，
// 提取全部依赖条目。
//
// 条目 key 形如 node_modules/{name} 或嵌套 node_modules/a/node_modules/{name}，
// 包名取最后一个 node_modules/ 之后的段（scoped 包保留 @scope/ 前缀）；
// 根条目（空 key）、workspace 链接（link）、无 resolved 的 bundled 依赖跳过；
// git+/file: 协议条目无法经 registry 镜像，跳过并记入 skipped 供调用方警告。
// 结果按 name@version 去重、按包名排序。
//
//	@param r io.Reader package-lock.json 内容。
//	@return []Entry 去重排序后的依赖条目。
//	@return []string 被跳过的非 registry 条目描述（如 "@scope/pkg@1.0.0 (git+...)")。
//	@return error JSON 解析失败，或 lockfileVersion 不在支持范围时的错误。
func ParseLock(r io.Reader) (entries []Entry, skipped []string, err error) {
	var lj lockJSON
	if err := json.NewDecoder(r).Decode(&lj); err != nil {
		return nil, nil, fmt.Errorf("解析 package-lock.json 失败: %w", err)
	}
	if len(lj.Packages) == 0 {
		if lj.LockfileVersion == 1 && len(lj.Dependencies) > 0 {
			return nil, nil, fmt.Errorf("lockfileVersion 1 的 lock 不支持，请用 npm ≥ 7 重新生成（npm install --package-lock-only）")
		}
		return nil, nil, fmt.Errorf("package-lock.json 缺少 packages 节点（lockfileVersion=%d）", lj.LockfileVersion)
	}
	seen := map[string]bool{}
	for key, p := range lj.Packages {
		// 包名取最后一个 node_modules/ 之后的段；空 key 是项目根条目
		i := strings.LastIndex(key, "node_modules/")
		if i < 0 {
			continue
		}
		name := key[i+len("node_modules/"):]
		if !ValidName(name) {
			continue
		}
		// workspace 链接与 bundled 依赖（无 resolved）不在 registry 上：链接
		// 无法镜像，bundled 内容已在父包 tarball 内，均静默跳过
		if p.Link || p.Version == "" || p.Resolved == "" {
			continue
		}
		if strings.HasPrefix(p.Resolved, "git+") || strings.HasPrefix(p.Resolved, "file:") {
			skipped = append(skipped, fmt.Sprintf("%s@%s (%s)", name, p.Version, p.Resolved))
			continue
		}
		key := name + "@" + p.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, Entry{
			Name: name, Version: p.Version, Integrity: p.Integrity,
			OS: positiveValues(p.OS), CPU: positiveValues(p.CPU),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Version < entries[j].Version
	})
	return entries, skipped, nil
}

// positiveValues 过滤 os/cpu 列表中的否定值（! 前缀）：仅保留正向值，
// 全否定或空列表返回空（按"未标注"处理，交由 npm 安装端自行跳过）。
func positiveValues(vals []string) []string {
	var out []string
	for _, v := range vals {
		if !strings.HasPrefix(v, "!") {
			out = append(out, v)
		}
	}
	return out
}

// osValues 把 --platform 的 os 值映射为 npm 的 os 取值（windows 对应 win32）。
var osValues = map[string][]string{
	"linux":   {"linux"},
	"windows": {"win32"},
	"darwin":  {"darwin"},
}

// cpuValues 把 --arch 的架构值映射为 npm 的 cpu 取值。
var cpuValues = map[string][]string{
	"x86_64":  {"x64"},
	"amd64":   {"x64"},
	"aarch64": {"arm64"},
	"arm64":   {"arm64"},
	"i686":    {"ia32"},
}

// MatchOSCPU 判断 lock 条目标注的 os/cpu 是否命中过滤条件。
//
// 条目未标注（空列表）视为不限平台恒匹配；过滤集为空表示不过滤恒匹配；
// 条目标注多个值时任一命中即可（正向值语义，否定值已在解析时剔除）。
//
//	@param e Entry lock 条目。
//	@param platforms []string --platform 的 os 过滤列表（linux/windows/darwin）。
//	@param archs []string --arch 的架构过滤列表（x86_64/aarch64 等，别名归一在映射表内）。
//	@return bool 命中过滤（该条目应下载）时为 true。
func MatchOSCPU(e Entry, platforms, archs []string) bool {
	return matchValues(e.OS, platforms, osValues) && matchValues(e.CPU, archs, cpuValues)
}

// matchValues 判断条目标注值是否命中过滤：任一标注值映射后等于任一过滤值的目标集。
func matchValues(vals, filters []string, mapping map[string][]string) bool {
	if len(vals) == 0 || len(filters) == 0 {
		return true
	}
	for _, v := range vals {
		for _, f := range filters {
			for _, m := range mapping[strings.ToLower(f)] {
				if v == m {
					return true
				}
			}
		}
	}
	return false
}

// packumentJSON 是 packument 中用到的字段子集。
type packumentJSON struct {
	Versions map[string]struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	} `json:"versions"`
}

// PackumentURL 拼接包的 packument 请求地址。
//
//	@param registry string registry 根地址，尾斜杠可有可无。
//	@param name string 完整包名，可含 scope。
//	@return string packument 请求 URL。
func PackumentURL(registry, name string) string {
	return strings.TrimRight(registry, "/") + "/" + EscapeName(name)
}

// FetchPackument 下载包的 packument（corgi 安装版）。
//
// 请求带 Accept: application/vnd.npm.install-v1+json（npm 自身安装使用的
// 精简格式，缺 readme/maintainers 等字段）；registry 忽略该头时返回完整版，
// 对安装无影响。返回原始响应体，由调用方落盘或解析。
//
//	@param ctx context.Context 控制请求生命周期的上下文。
//	@param client *http.Client 发起请求的客户端。
//	@param registry string registry 根地址。
//	@param name string 完整包名，可含 scope。
//	@return []byte packument 原始 JSON。
//	@return error 请求失败、状态码非 200 或包不存在（404）时的错误。
func FetchPackument(ctx context.Context, client *http.Client, registry, name string) ([]byte, error) {
	pageURL := PackumentURL(registry, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	req.Header.Set("User-Agent", "pkgmirror")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("包 %s 不在 registry %s（私有包或已下架；本镜像仅支持公共 registry 包）", name, registry)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 返回 %s", pageURL, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// TarballInfo 从 packument 提取指定版本的 tarball 下载地址、SRI 与
// engines.node 约束。
//
// integrity 以 packument 为准（比 lock 文件更权威，且老 lock 可能缺失）；
// enginesNode 为空表示该版本未声明 node 版本约束。
//
//	@param body []byte packument 原始 JSON。
//	@param version string 目标版本号。
//	@return string tarball 下载地址。
//	@return string SRI（sha512-base64），registry 未提供时为空串。
//	@return string engines.node 的 semver range 原文，未声明时为空串。
//	@return error packument 格式非法或不含该版本时的错误。
func TarballInfo(body []byte, version string) (tarballURL, integrity, enginesNode string, err error) {
	var pj packumentJSON
	if err := json.Unmarshal(body, &pj); err != nil {
		return "", "", "", fmt.Errorf("解析 packument 失败: %w", err)
	}
	v, ok := pj.Versions[version]
	if !ok || v.Dist.Tarball == "" {
		return "", "", "", fmt.Errorf("packument 不含版本 %s（可能已被撤回）", version)
	}
	return v.Dist.Tarball, v.Dist.Integrity, v.Engines.Node, nil
}

// RewriteTarballs 把 packument 中全部版本的 dist.tarball 重写为镜像地址。
//
// 服务端返回 packument 前调用：npm 按包内 URL 下载 tarball，不重写则会
// 绕过镜像直连官方源。文件名取原 URL 尾段（与官方命名一致），重写后的
// URL 形如 {schemeHost}/mirror/npm/{name}/-/{file}，与官方 URL 结构相同
// （scope 不编码，npm 请求时自行处理）。packument 各字段均为字符串或
// 对象，map 反序列化再序列化无损。
//
//	@param body []byte packument 原始 JSON。
//	@param schemeHost string 镜像对外地址（如 http://host:8080），不含路径。
//	@param name string 完整包名，可含 scope。
//	@return string 重写后的 packument JSON。
//	@return error body 不是合法 JSON 对象时的错误（此时调用方应返回原文件并告警）。
func RewriteTarballs(body []byte, schemeHost, name string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if versions, ok := doc["versions"].(map[string]any); ok {
		for _, v := range versions {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			dm, ok := vm["dist"].(map[string]any)
			if !ok {
				continue
			}
			tb, _ := dm["tarball"].(string)
			if tb == "" {
				continue
			}
			dm["tarball"] = schemeHost + "/mirror/npm/" + name + "/-/" + fileNameOf(tb)
		}
	}
	return json.Marshal(doc)
}

// fileNameOf 从 URL 尾段提取文件名，查询串与片段不计入。
//
//	@param rawURL string tarball 原始 URL。
//	@return string 文件名。
func fileNameOf(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return path.Base(u)
}

// SRIDigest 解析 SRI 串，返回算法名与十六进制摘要。
//
// SRI 形如 sha512-<base64>，多算法以空格分隔时取第一个受支持的
// （sha512/sha256）；其它算法（如 sha1）返回 ok=false，由调用方跳过校验。
//
//	@param sri string SRI 串。
//	@return string 算法名（sha512 或 sha256）。
//	@return string 摘要的十六进制小写。
//	@return bool 存在受支持算法且 base64 解码成功时为 true。
func SRIDigest(sri string) (algo, hexDigest string, ok bool) {
	for _, part := range strings.Fields(sri) {
		algo, b64, found := strings.Cut(part, "-")
		if !found || (algo != "sha512" && algo != "sha256") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		return algo, hex.EncodeToString(raw), true
	}
	return "", "", false
}

// VerifySRIFile 计算文件哈希并与 SRI 对比。
//
// SRI 为空或不含受支持算法时不校验（返回 nil），避免对极老的 sha1 条目
// 误报；算法受支持但哈希不一致时返回错误。
//
//	@param path string 本地文件路径，不存在时返回错误。
//	@param sri string 期望的 SRI 串。
//	@return error 文件读取失败或哈希不一致时的错误。
func VerifySRIFile(path, sri string) error {
	algo, want, ok := SRIDigest(sri)
	if !ok {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var sum []byte
	switch algo {
	case "sha512":
		h := sha512.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		sum = h.Sum(nil)
	default:
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		sum = h.Sum(nil)
	}
	if got := hex.EncodeToString(sum); got != want {
		return fmt.Errorf("SRI 校验失败（%s）: 期望 %s 实际 %s", algo, want[:12]+"…", got[:12]+"…")
	}
	return nil
}

// depPackument 包含按已镜像 tarball 提取依赖所需的 packument 字段。
type depPackument struct {
	Versions map[string]struct {
		Dist struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
		// Dependencies 与 OptionalDependencies 的键是依赖包名（完整名，
		// scoped 含 @scope/ 前缀），值是 semver range（此处不关心）。
		Dependencies         map[string]string `json:"dependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	} `json:"versions"`
}

// DepNamesForTarballs 提取已镜像版本的依赖包名（级联删除建图用）。
//
// 对 dist.tarball 文件名命中 exists 的每个版本，取 dependencies ∪
// optionalDependencies 的键并集，可选依赖也参与级联保留判断。
// bundledDependencies 的内容位于父包 tarball 内。
//
//	@param body []byte packument 原始 JSON。
//	@param exists func(string) bool 判断 tarball 文件名是否已镜像
//	 （调用方传包目录内的 .tgz 文件名集合）。
//	@return []string 排序去重后的依赖包名（scoped 含前缀，与镜像目录名一致）；
//	 无已镜像版本或无依赖声明时为空。
//	@return error packument 不是合法 JSON 时的错误。
func DepNamesForTarballs(body []byte, exists func(string) bool) ([]string, error) {
	var pj depPackument
	if err := json.Unmarshal(body, &pj); err != nil {
		return nil, fmt.Errorf("解析 packument 失败: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range pj.Versions {
		if !exists(fileNameOf(v.Dist.Tarball)) {
			continue
		}
		for name := range v.Dependencies {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		for name := range v.OptionalDependencies {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
