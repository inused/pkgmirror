package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pkgmirror/internal/manifest"
	"pkgmirror/internal/pymeta"
	"pkgmirror/internal/pypi"
	"pkgmirror/internal/redact"
)

// DownloadCommon 是各下载类型共用的网络、来源和产出选项。
type DownloadCommon struct {
	// Platforms 是 --platform 的 os 过滤列表（linux/windows/darwin），空为全平台。
	// 同时作用于 PyPI wheel 平台标签与 Python 安装包。
	Platforms []string
	// Archs 是 --arch 的架构过滤列表（x86_64/aarch64 等，amd64/arm64 为别名），
	// 空为全架构。同时作用于 PyPI wheel 平台标签与 Python 安装包。
	Archs []string
	// IndexURL 是 PyPI 索引地址（已解析配置文件、环境变量与 pip.conf）。
	IndexURL string
	// Registry 是 npm registry 地址（已解析配置文件与环境变量，仅 download npm 使用）。
	Registry string
	// DistURL 是 Node.js dist 根地址（已解析配置文件与环境变量，仅 download node
	// 使用），空表示未配置（RunDownloadNode 回退官方源）。
	DistURL string
	// GoDistURL 是 Go 发行归档根地址（已解析配置文件与环境变量，仅
	// download golang 使用），空表示未配置（RunDownloadGolang 回退官方源）。
	GoDistURL string
	// GoProxyURL 是上游 module proxy 地址（已解析配置文件与 GOPROXY 环境变量，
	// 仅 download gomod 使用），空表示未配置（RunDownloadGomod 回退官方源）。
	GoProxyURL string
	// GitHubPrefix 是 GitHub 反代前缀，拼在完整 URL 之前，空为直连。
	GitHubPrefix string
	// Proxy 是 HTTP 代理地址（如 http://127.0.0.1:7890），空表示走
	// HTTP_PROXY/HTTPS_PROXY 环境变量（http.ProxyFromEnvironment）。
	Proxy string
	// OutDir 是下载产出目录路径。
	OutDir string
	// Version 是 pkgmirror 版本号，写入 manifest 的 generator 字段。
	Version string
}

// DownloadPypiOptions 是 download pypi 子命令的输入。
type DownloadPypiOptions struct {
	DownloadCommon
	// RequirementsFile 是 -r 指定的需求文件路径，可为空（仅用命令行包名）。
	RequirementsFile string
	// Packages 是命令行传入的包名列表；带 ==版本 时锁定该版本，
	// 裸包名由 uv 解析为兼容目标 Python 区间的最新版。
	Packages []string
	// Pythons 是 --python 解析出的目标 Python 版本列表，必填：
	// 既是 uv 解析的版本下限，也是 wheel 文件的过滤条件。
	Pythons []pymeta.Spec
}

// DownloadPythonOptions 是 download python 子命令的输入。
type DownloadPythonOptions struct {
	DownloadCommon
	// Pythons 是命令行位置参数解析出的目标 Python 版本列表，必填。
	Pythons []pymeta.Spec
	// AllPatches 为 true 时对 Patch 为 0 的 Spec 下载全部历史 patch。
	AllPatches bool
}

// httpTimeout 覆盖单次 HTTP 请求（大文件下载）的最长时间。
const httpTimeout = 30 * time.Minute

// downloadRetries 是单个文件下载失败后的重试次数。
const downloadRetries = 2

// pkgmirrorUAVersion 是下载请求 User-Agent 中的版本号，由 main 通过变量注入。
var PkgMirrorUAVersion = "dev"

// ParsePythonSpec 解析 "3.11" 或 "3.11.9" 形式的版本串。
//
//	@param s string 版本串。
//	@return pymeta.Spec 解析结果，patch 省略时为 0（表示最新）。
//	@return error 格式非法时的错误。
func ParsePythonSpec(s string) (pymeta.Spec, error) {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return pymeta.Spec{}, fmt.Errorf("python 版本 %q 应形如 3.11 或 3.11.9", s)
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return pymeta.Spec{}, fmt.Errorf("python 版本 %q 含非法数字段 %q", s, p)
		}
		nums[i] = n
	}
	spec := pymeta.Spec{Major: nums[0], Minor: nums[1]}
	if len(nums) == 3 {
		spec.Patch = nums[2]
	}
	return spec, nil
}

// PythonFloor 计算传给 uv pip compile 的 python 版本下限。
//
// 取目标版本中最小的 major.minor；列表为空时退回 3.9（正常流程 pypi 子命令
// 已强制要求 --python，此处仅作防御）。
//
//	@param specs []pymeta.Spec --python 解析出的目标列表。
//	@return string 形如 "3.9" 的下限版本。
func PythonFloor(specs []pymeta.Spec) string {
	if len(specs) == 0 {
		return "3.9"
	}
	min := specs[0]
	for _, s := range specs[1:] {
		if s.Major < min.Major || (s.Major == min.Major && s.Minor < min.Minor) {
			min = s
		}
	}
	return fmt.Sprintf("%d.%d", min.Major, min.Minor)
}

// specMinors 提取目标版本的 minor 列表，用于 wheel 的 python 标签过滤。
//
//	@param specs []pymeta.Spec 目标版本列表。
//	@return []int 去重后的 minor 列表（如 [11, 12]）。
func specMinors(specs []pymeta.Spec) []int {
	seen := map[int]bool{}
	var minors []int
	for _, s := range specs {
		if !seen[s.Minor] {
			seen[s.Minor] = true
			minors = append(minors, s.Minor)
		}
	}
	return minors
}

// newManifest 构造本次下载的清单骨架。
//
//	@param version string pkgmirror 版本号。
//	@return manifest.Manifest 含元信息的空清单。
func newManifest(version string) manifest.Manifest {
	return manifest.Manifest{Version: 1, Generator: "pkgmirror " + version, Created: time.Now()}
}

// saveManifest 合并保存清单并输出完成日志。
//
//	@param outDir string 下载产出目录（不存在则创建）。
//	@param mf manifest.Manifest 本次构建的清单，保存前与目录中已有清单增量合并。
//	@return error 目录创建或清单写入失败的原因。
func saveManifest(outDir string, mf manifest.Manifest) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	mf = mergeManifest(outDir, mf)
	if err := manifest.Save(outDir, mf); err != nil {
		return fmt.Errorf("写清单失败: %w", err)
	}
	log.Printf("完成：%s（%d 个文件）。上传命令: pkgmirror upload --server URL %s", outDir, len(mf.Files), outDir)
	return nil
}

// RunDownloadPypi 执行 download pypi：依赖解析、wheel/sdist 下载与清单生成。
// 任一文件下载失败即中止，已下载的文件保留，便于重跑续传。
//
//	@param opts DownloadPypiOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadPypi(opts DownloadPypiOptions) error {
	if len(opts.Packages) == 0 && opts.RequirementsFile == "" {
		return fmt.Errorf("请指定至少一个包名（如 six 或 six==1.17.0）或 -r 需求文件")
	}
	if len(opts.Pythons) == 0 {
		return fmt.Errorf("必须用 --python 指定目标 Python 版本（如 --python 3.12 或 --python 3.11,3.12）")
	}
	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	mf := newManifest(opts.Version)

	reqs, err := resolveReqs(ctx, opts)
	if err != nil {
		return err
	}
	log.Printf("解析得到 %d 个锁定依赖", len(reqs))
	minors := specMinors(opts.Pythons)
	for i, req := range reqs {
		files, err := pypi.FetchSimple(ctx, client, opts.IndexURL, req.Name)
		if err != nil {
			return err
		}
		var matched []pypi.File
		for _, f := range files {
			if f.Yanked || !pypi.MatchesReq(f, req.Name, req.Version) {
				continue
			}
			// wheel 按目标 Python 版本与平台架构过滤；sdist 与平台无关，始终保留
			if strings.HasSuffix(strings.ToLower(f.Filename), ".whl") {
				if !pypi.WheelMatchesPython(f.Filename, minors) {
					continue
				}
				if !pypi.WheelMatchesPlatformArch(f.Filename, opts.Platforms, opts.Archs) {
					continue
				}
			}
			matched = append(matched, f)
		}
		if len(matched) == 0 {
			return fmt.Errorf("%s==%s 在索引 %s 中没有任何兼容文件（--python %v --platform %v --arch %v）",
				req.Name, req.Version, opts.IndexURL, minors, opts.Platforms, opts.Archs)
		}
		for _, f := range matched {
			dest := filepath.Join(opts.OutDir, "pypi", pypi.NormalizeName(req.Name), f.Filename)
			log.Printf("[%d/%d] %s", i+1, len(reqs), f.Filename)
			if err := fetchAndRecord(ctx, client, f.URL, dest, f.SHA256, opts.OutDir, &mf); err != nil {
				return err
			}
		}
	}
	return saveManifest(opts.OutDir, mf)
}

// RunDownloadPython 执行 download python：按元数据筛选并下载 CPython 安装包。
//
//	@param opts DownloadPythonOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadPython(opts DownloadPythonOptions) error {
	if len(opts.Pythons) == 0 {
		return fmt.Errorf("请指定 Python 版本（如 pkgmirror download python 3.12 或 3.12.4 3.13.1）")
	}
	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	mf := newManifest(opts.Version)

	meta, err := fetchMetadata(ctx, client, opts.GitHubPrefix)
	if err != nil {
		return err
	}
	entries, err := meta.Select(opts.Pythons, opts.Platforms, opts.Archs, opts.AllPatches)
	if err != nil {
		return err
	}
	log.Printf("筛选得到 %d 个 CPython 安装包文件", len(entries))
	for i, e := range entries {
		filename := fileNameOfURL(e.URL)
		dest := filepath.Join(opts.OutDir, "python", e.Build, filename)
		log.Printf("[%d/%d] %s", i+1, len(entries), filename)
		if err := fetchAndRecord(ctx, client, applyPrefix(opts.GitHubPrefix, e.URL), dest, e.SHA256, opts.OutDir, &mf); err != nil {
			return err
		}
	}
	return saveManifest(opts.OutDir, mf)
}

// fetchAndRecord 下载一个文件并在清单中登记。
//
// 目标已存在且哈希一致时跳过下载（增量重跑），清单条目按 path 去重后补写。
// sha 为空表示源不提供哈希（下载不校验，登记时计算实际哈希）。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@param url string 下载地址。
//	@param dest string 本地目标路径。
//	@param sha string 期望 sha256。
//	@param outDir string 下载产出目录（用于计算清单条目的 path）。
//	@param mf *manifest.Manifest 待追加的清单。
//	@return error 下载失败的原因。
func fetchAndRecord(ctx context.Context, client *http.Client, url, dest, sha, outDir string, mf *manifest.Manifest) error {
	skipped, err := downloadTo(ctx, client, url, dest, sha)
	if err != nil {
		return err
	}
	_ = skipped // 跳过与否只影响日志，条目统一按 path 去重登记
	rel := relPath(dest, outDir)
	for _, f := range mf.Files {
		if f.Path == rel {
			return nil
		}
	}
	entry := manifest.File{Filename: filepath.Base(dest), Path: rel, SHA256: sha, Size: fileSize(dest)}
	// 源不提供哈希时（如 node 的 SHASUMS256.txt 本身）计算实际哈希登记，
	// 清单条目缺哈希会被 manifest.Load 拒绝
	if sha == "" {
		sum, err := fileSHA256(dest)
		if err != nil {
			return err
		}
		entry.SHA256 = sum
	}
	switch {
	case strings.HasPrefix(rel, "python/"):
		entry.Type = "python"
		entry.Tag = filepath.Base(filepath.Dir(dest))
	case strings.HasPrefix(rel, "node/"):
		entry.Type = "node"
		entry.Tag = filepath.Base(filepath.Dir(dest))
	case strings.HasPrefix(rel, "golang/"):
		// flat 布局：文件名即含版本，不使用 name/tag
		entry.Type = "golang"
	case strings.HasPrefix(rel, "gomod/"):
		// rel 形如 gomod/{模块}/@v/{文件}，模块路径是中间全部段
		// （模块路径可含多段斜杠）
		entry.Type = "gomod"
		entry.Name = strings.TrimSuffix(strings.TrimPrefix(rel, "gomod/"), "/@v/"+entry.Filename)
	default:
		entry.Type = "pypi"
		entry.Name = filepath.Base(filepath.Dir(dest))
	}
	mf.Files = append(mf.Files, entry)
	return nil
}

// mergeManifest 把目录中已有的 manifest.json 与本次结果合并。
//
// 当前条目覆盖相同 Path 的已有条目；已有条目对应文件仍在磁盘时保留，
// 文件已删除时剔除。upload 按合并后的整份清单上传。
//
//	@param dir string 下载产出目录。
//	@param cur manifest.Manifest 本次下载构建的清单。
//	@return manifest.Manifest 合并后的清单，元信息（生成时间等）取本次。
func mergeManifest(dir string, cur manifest.Manifest) manifest.Manifest {
	old, err := manifest.Load(dir)
	if err != nil {
		return cur // 无旧清单或旧清单不可读，退化为仅本次内容
	}
	merged := cur
	seen := map[string]bool{}
	for _, f := range cur.Files {
		seen[f.Path] = true
	}
	for _, f := range old.Files {
		if seen[f.Path] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil {
			continue
		}
		merged.Files = append(merged.Files, f)
		seen[f.Path] = true
	}
	return merged
}

// resolveReqs 合并需求来源并生成依赖列表。
//
// 输入全部为 name==version 时直接返回，传递依赖须包含在输入中；
// 存在裸包名或版本范围时调用 uv 生成 universal 锁文件并解析。
//
//	@param ctx context.Context 上下文（用于超时控制，仅 uv 路径使用）。
//	@param opts DownloadPypiOptions pypi 子命令选项。
//	@return []pypi.Req name==version 列表。
//	@return error 输入读取、uv 调用或锁定文件解析失败的原因。
func resolveReqs(ctx context.Context, opts DownloadPypiOptions) ([]pypi.Req, error) {
	lines, err := collectReqLines(opts)
	if err != nil {
		return nil, err
	}
	if reqs := exactReqs(lines); reqs != nil {
		if len(reqs) == 0 {
			return nil, fmt.Errorf("输入中没有可下载的包（全部为空行或注释）")
		}
		log.Printf("按精确版本清单下载，不解析传递依赖")
		return reqs, nil
	}
	if _, err := exec.LookPath("uv"); err != nil {
		return nil, fmt.Errorf("未找到 uv，请安装（如 pip install uv，或 https://docs.astral.sh/uv/getting-started/installation/）；或将全部包写成 包名==版本 精确形式以跳过解析")
	}
	var in strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&in, "%s\n", l)
	}
	inPath := filepath.Join(os.TempDir(), fmt.Sprintf("pkgmirror-%d.in", time.Now().UnixNano()))
	lockPath := inPath + ".lock"
	if err := os.WriteFile(inPath, []byte(in.String()), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(inPath)
	defer os.Remove(lockPath)

	args := []string{"pip", "compile", "--universal", "--no-header",
		"--index-url", opts.IndexURL, "-p", PythonFloor(opts.Pythons), inPath, "-o", lockPath}
	cmd := exec.CommandContext(ctx, "uv", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("uv pip compile 失败: %w\n%s", err, out)
	}
	lockFile, err := os.Open(lockPath)
	if err != nil {
		return nil, err
	}
	defer lockFile.Close()
	return pypi.ParseLock(lockFile)
}

// collectReqLines 汇总需求来源：-r 需求文件内容（逐行）在前，
// 命令行包名在后。
//
//	@param opts DownloadPypiOptions pypi 子命令选项。
//	@return []string 原始输入行（未清洗）。
//	@return error 需求文件读取失败的原因。
func collectReqLines(opts DownloadPypiOptions) ([]string, error) {
	var lines []string
	if opts.RequirementsFile != "" {
		data, err := os.ReadFile(opts.RequirementsFile)
		if err != nil {
			return nil, fmt.Errorf("读取需求文件失败: %w", err)
		}
		lines = append(lines, strings.Split(string(data), "\n")...)
	}
	return append(lines, opts.Packages...), nil
}

// exactReqs 判断输入行是否全部为精确形式（name==version）并解析。
//
// 空行、# 注释与 -- 选项行忽略；任一行是裸包名或版本范围（不含 ==，
// 或 == 两侧有空值）时返回 nil，表示需要 uv 解析。行内 "; marker" 与
// "# 注释" 截断规则与 pypi.ParseLock 一致。
//
//	@param lines []string 原始输入行。
//	@return []pypi.Req 精确依赖列表；存在非精确行时为 nil。
func exactReqs(lines []string) []pypi.Req {
	var reqs []pypi.Req
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "--") {
			continue
		}
		if i := strings.IndexAny(line, ";#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		name, version, ok := strings.Cut(line, "==")
		name = strings.TrimSpace(name)
		version = strings.TrimSpace(version)
		if !ok || name == "" || version == "" {
			return nil
		}
		reqs = append(reqs, pypi.Req{Name: name, Version: version})
	}
	return reqs
}

// newHTTPClient 构造带可选代理的 HTTP 客户端。
//
// proxy 为空时通过 http.ProxyFromEnvironment 读取
// HTTP_PROXY、HTTPS_PROXY、NO_PROXY 环境变量。
//
//	@param proxy string 代理地址（http/https/socks5），空表示不显式指定。
//	@param timeout time.Duration 单次请求超时。
//	@return *http.Client 构造好的客户端。
//	@return error 代理地址无法解析时的错误。
func newHTTPClient(proxy string, timeout time.Duration) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("代理地址 %q 非法", proxy)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

// fetchMetadata 拉取 uv 的托管 Python 下载元数据。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@param prefix string GitHub 反代前缀，空为直连。
//	@return pymeta.Metadata 解析后的元数据。
//	@return error 下载或解析失败的原因。
func fetchMetadata(ctx context.Context, client *http.Client, prefix string) (pymeta.Metadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, applyPrefix(prefix, pymeta.MetaURL), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 uv 元数据失败（可用 --github-prefix 指定反代）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 uv 元数据返回 %s", resp.Status)
	}
	return pymeta.ParseMetadata(resp.Body)
}

// applyPrefix 在 URL 前拼接反代前缀；prefix 为空返回原 URL。
//
//	@param prefix string 反代前缀（如 https://ghproxy.net/），空为直连。
//	@param rawURL string 原始完整 URL。
//	@return string 实际请求的 URL。
func applyPrefix(prefix, rawURL string) string {
	return prefix + rawURL
}

// fileNameOfURL 从 URL 尾段提取文件名，查询串与片段不计入。
//
//	@param rawURL string 原始 URL。
//	@return string 文件名；URL 无尾段时返回原串。
func fileNameOfURL(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	// GitHub release 文件名中的 + 以 %2B 编码出现，落盘名必须解码还原，
	// 否则与 uv 按 UV_PYTHON_INSTALL_MIRROR 请求的文件名不一致
	if decoded, err := url.PathUnescape(u); err == nil {
		u = decoded
	}
	return filepath.Base(u)
}

// downloadTo 下载 url 到 dest（临时文件 + rename），sha256 不符报错。
//
// dest 已存在且哈希一致时跳过，支持对同一输出目录重复执行时增量续传。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@param url string 下载地址。
//	@param dest string 目标文件路径。
//	@param wantSHA string 期望的 sha256 十六进制，空串跳过校验。
//	@return bool 目标已存在且一致被跳过时为 true。
//	@return error 下载失败、校验失败或写盘失败的原因（已含重试）。
func downloadTo(ctx context.Context, client *http.Client, url, dest, wantSHA string) (bool, error) {
	if wantSHA != "" {
		if sum, err := fileSHA256(dest); err == nil && sum == wantSHA {
			log.Printf("  已存在，跳过: %s", filepath.Base(dest))
			return true, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	var lastErr error
	for attempt := 0; attempt <= downloadRetries; attempt++ {
		if attempt > 0 {
			log.Printf("  重试 %d/%d: %s", attempt, downloadRetries, redact.URL(url))
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		err := func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			// 部分镜像（如清华源）对无 UA 的请求直接 403，始终声明身份
			req.Header.Set("User-Agent", "pkgmirror/"+PkgMirrorUAVersion)
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("下载返回 %s", resp.Status)
			}
			tmp, err := os.CreateTemp(filepath.Dir(dest), ".part-*")
			if err != nil {
				return err
			}
			tmpName := tmp.Name()
			defer os.Remove(tmpName)
			h := sha256.New()
			if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
				tmp.Close()
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			got := hex.EncodeToString(h.Sum(nil))
			if wantSHA != "" && got != wantSHA {
				return fmt.Errorf("sha256 不符: 期望 %s 实际 %s", wantSHA, got)
			}
			return os.Rename(tmpName, dest)
		}()
		if err == nil {
			return false, nil
		}
		lastErr = err
	}
	return false, fmt.Errorf("下载 %s 失败: %w", url, lastErr)
}

// fileSHA256 计算文件 sha256，文件不存在时报错。
//
//	@param path string 文件路径。
//	@return string sha256 十六进制小写。
//	@return error 读取失败时的错误。
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
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileSize 返回文件大小，失败返回 0。
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// relPath 计算 dest 相对 outDir 的斜杠分隔路径，用于 manifest 的 path 字段。
//
//	@param dest string 文件绝对或相对路径。
//	@param outDir string 下载产出目录。
//	@return string 下载产出目录内相对路径（/ 分隔）。
func relPath(dest, outDir string) string {
	rel, err := filepath.Rel(outDir, dest)
	if err != nil {
		return filepath.ToSlash(dest)
	}
	return filepath.ToSlash(rel)
}
