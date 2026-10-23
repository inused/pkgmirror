package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"pkgmirror/internal/manifest"
	"pkgmirror/internal/npm"
	"pkgmirror/internal/redact"
)

// DownloadNpmOptions 是 download npm 子命令的输入。
type DownloadNpmOptions struct {
	DownloadCommon
	// Packages 是命令行传入的 包名[@版本range] 列表（裸名由 npm 取最新
	// 满足版），与 LockFile 互斥。
	Packages []string
	// LockFile 是 -r 指定的 package.json 或 package-lock.json 路径，
	// 可为空（仅用命令行包名）。按内容识别，现成 lock 无需本地 npm。
	LockFile string
	// Nodes 是 --node 解析出的目标 Node.js 版本列表（完整 x.y.z 形式）。
	// 非空时对每个条目校验 packument 的 engines.node；版本选择不受影响
	// （npm 无按目标 node 解析的能力，见设计文档）。
	Nodes []string
}

// ParseNodeVersion 解析 --node 的版本参数为完整三段版本。
//
// 缺少的版本段补 0，如 "18" 为 18.0.0，"18.20" 为 18.20.0。
//
//	@param s string 版本串。
//	@return string 完整 x.y.z 版本。
//	@return error 格式非法时的错误。
func ParseNodeVersion(s string) (string, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("node 版本 %q 应形如 18、18.20 或 18.20.1", s)
	}
	nums := make([]string, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return "", fmt.Errorf("node 版本 %q 含非法数字段 %q", s, p)
		}
		nums[i] = p
	}
	for i := len(parts); i < 3; i++ {
		nums[i] = "0"
	}
	return strings.Join(nums, "."), nil
}

// RunDownloadNpm 执行 download npm：获取依赖 lock、下载 packument 与
// tarball、生成清单。任一文件下载失败即中止，已下载的文件保留，便于重跑续传。
//
//	@param opts DownloadNpmOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadNpm(opts DownloadNpmOptions) error {
	if len(opts.Packages) == 0 && opts.LockFile == "" {
		return fmt.Errorf("请指定至少一个包名（如 lodash 或 lodash@^4）或 -r package(-lock).json")
	}
	if opts.LockFile != "" && len(opts.Packages) > 0 {
		return fmt.Errorf("-r 文件与命令行包名不能同时指定；请将依赖写入同一 package.json 或统一使用命令行包名")
	}
	lockPath, cleanup, err := prepareNpmLock(opts)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	f, err := os.Open(lockPath)
	if err != nil {
		return fmt.Errorf("读取 lock 失败: %w", err)
	}
	defer f.Close()
	entries, skipped, err := npm.ParseLock(f)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		log.Printf("警告: 跳过非 registry 条目 %s（git/file 依赖无法经 registry 镜像，需人工处理）", redact.Text(s))
	}
	// 平台过滤：lock 标注 os/cpu 的平台二进制包按 --platform/--arch 过滤
	var kept []npm.Entry
	for _, e := range entries {
		if npm.MatchOSCPU(e, opts.Platforms, opts.Archs) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("过滤后没有可下载的条目（共 %d 条，--platform %v --arch %v）", len(entries), opts.Platforms, opts.Archs)
	}
	log.Printf("解析得到 %d 个依赖条目，平台过滤后 %d 个", len(entries), len(kept))

	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	mf := newManifest(opts.Version)
	// 按包分组，每包下载一份 packument + 各版本 tarball；names 保持排序
	groups := map[string][]npm.Entry{}
	var names []string
	for _, e := range kept {
		if _, ok := groups[e.Name]; !ok {
			names = append(names, e.Name)
		}
		groups[e.Name] = append(groups[e.Name], e)
	}
	// --node 校验：违规条目跳过下载，全部包扫完后统一报错（一次列全，
	// packument 本来就要下载，只浪费少量元数据流量）
	var nodeViolations []string
	for i, name := range names {
		log.Printf("[%d/%d] %s（%d 个版本）", i+1, len(names), name, len(groups[name]))
		body, err := npm.FetchPackument(ctx, client, opts.Registry, name)
		if err != nil {
			return err
		}
		// packument 每次取最新并覆盖（幂等），供服务端重写后分发
		pdest := filepath.Join(opts.OutDir, "npm", filepath.FromSlash(name), "packument.json")
		if err := fetchNpmRecord(ctx, client, npm.PackumentURL(opts.Registry, name), pdest, "", opts.OutDir, &mf); err != nil {
			return err
		}
		for _, e := range groups[name] {
			tarballURL, sri, nodeRng, err := npm.TarballInfo(body, e.Version)
			if err != nil {
				return err
			}
			if bad := nodeMismatches(e, nodeRng, opts.Nodes); len(bad) > 0 {
				nodeViolations = append(nodeViolations, bad...)
				continue
			}
			dest := filepath.Join(opts.OutDir, "npm", filepath.FromSlash(name), fileNameOfURL(tarballURL))
			if err := fetchNpmRecord(ctx, client, tarballURL, dest, sri, opts.OutDir, &mf); err != nil {
				return err
			}
		}
	}
	if len(nodeViolations) > 0 {
		return fmt.Errorf("%d 个条目与 --node 目标版本不兼容（调整版本 range 重新解析，或升级内网 node）：\n%s",
			len(nodeViolations), strings.Join(nodeViolations, "\n"))
	}
	return saveManifest(opts.OutDir, mf)
}

// nodeMismatches 返回条目 engines.node 不满足的全部目标 node 版本的描述。
//
//	@param e npm.Entry lock 条目。
//	@param nodeRng string engines.node 的 range 原文，空表示未声明约束。
//	@param nodes []string 目标 node 版本列表（完整 x.y.z）。
//	@return []string 违规描述列表，无违规时为空。
func nodeMismatches(e npm.Entry, nodeRng string, nodes []string) []string {
	var out []string
	for _, nv := range nodes {
		if !npm.NodeSatisfies(nv, nodeRng) {
			out = append(out, fmt.Sprintf("  %s@%s 需要 node %s（engines.node=%q），目标 %s 不满足", e.Name, e.Version, nodeRng, nodeRng, nv))
		}
	}
	return out
}

// fetchNpmRecord 下载一个 npm 文件（packument 或 tarball）并登记清单。
//
// tarball 传 SRI（sha512）做哈希校验（来源为 packument 的 dist.integrity），
// 本地已存在且 SRI 一致时跳过下载支持增量重跑；packument 无先验哈希，传空
// 每次重新下载取最新。清单条目按 sha256 登记（与服务端契约一致，下载后计算）。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@param url string 下载地址。
//	@param dest string 本地目标路径。
//	@param sri string 期望的 SRI（sha512-base64），空串表示不校验。
//	@param outDir string 下载产出目录（用于清单 path）。
//	@param mf *manifest.Manifest 待追加的清单。
//	@return error 下载或校验失败的原因。
func fetchNpmRecord(ctx context.Context, client *http.Client, url, dest, sri, outDir string, mf *manifest.Manifest) error {
	if sri != "" {
		if err := npm.VerifySRIFile(dest, sri); err == nil {
			log.Printf("  已存在，跳过: %s", filepath.Base(dest))
		} else if err := downloadNpmOnce(ctx, client, url, dest, sri); err != nil {
			return err
		}
	} else if _, err := downloadTo(ctx, client, url, dest, ""); err != nil {
		return err
	}
	rel := relPath(dest, outDir)
	for _, f := range mf.Files {
		if f.Path == rel {
			return nil
		}
	}
	sum, err := fileSHA256(dest)
	if err != nil {
		return err
	}
	// 包名从 path 推导：npm/@scope/name/{file} → @scope/name（rel 为斜杠分隔，
	// scoped 包的 scope 与名是两层目录，不能只取一层父目录）
	entry := manifest.File{
		Type:     "npm",
		Name:     path.Dir(strings.TrimPrefix(rel, "npm/")),
		Filename: filepath.Base(dest),
		Path:     rel,
		SHA256:   sum,
		Size:     fileSize(dest),
	}
	mf.Files = append(mf.Files, entry)
	return nil
}

// downloadNpmOnce 下载 tarball 并校验 SRI，校验失败时删除残留文件。
//
// downloadTo 无先验 sha256 可传（SRI 是 sha512），下载本身不校验，落盘后
// 再整体校验；失败必须删除，否则下次重跑会把坏文件误判为已下载。
func downloadNpmOnce(ctx context.Context, client *http.Client, url, dest, sri string) error {
	if _, err := downloadTo(ctx, client, url, dest, ""); err != nil {
		return err
	}
	if err := npm.VerifySRIFile(dest, sri); err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}

// prepareNpmLock 产出可解析的 package-lock.json 路径。
//
// -r 为现成 lock 时直接返回其路径（无需本地 npm）；为 package.json 或仅有
// 命令行包名时在外网机调 npm 生成临时 lock（要求 npm ≥ 7）。
//
//	@param opts DownloadNpmOptions npm 子命令选项（LockFile 与 Packages 已互斥校验）。
//	@return string package-lock.json 路径。
//	@return func() 临时产物清理函数；无临时产物时为 nil。
//	@return error 识别失败、npm 缺失或生成失败的原因。
func prepareNpmLock(opts DownloadNpmOptions) (string, func(), error) {
	if opts.LockFile == "" {
		return generateNpmLock(opts.Packages, "", opts.Registry)
	}
	kind, err := detectReqFile(opts.LockFile)
	if err != nil {
		return "", nil, err
	}
	if kind == "lock" {
		return opts.LockFile, nil, nil
	}
	return generateNpmLock(opts.Packages, opts.LockFile, opts.Registry)
}

// detectReqFile 按 JSON 内容识别 -r 文件类型。
//
// 有 lockfileVersion 字段视为 package-lock.json；有 dependencies 且无
// lockfileVersion 视为 package.json；其余（含 yarn.lock / pnpm-lock.yaml
// 等非 JSON 格式）报错。
//
//	@param path string -r 文件路径。
//	@return string "lock" 或 "manifest"。
//	@return error 文件读取失败或无法识别时的错误。
func detectReqFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 -r 文件失败: %w", err)
	}
	var probe struct {
		LockfileVersion int            `json:"lockfileVersion"`
		Dependencies    map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", fmt.Errorf("无法识别 %s：不是合法 JSON（yarn.lock / pnpm-lock.yaml 请先在外网机执行 npm install --package-lock-only 转为 package-lock.json）", path)
	}
	if probe.LockfileVersion != 0 {
		return "lock", nil
	}
	if probe.Dependencies != nil {
		return "manifest", nil
	}
	return "", fmt.Errorf("无法识别 %s：既无 lockfileVersion 也无 dependencies 字段", path)
}

// generateNpmLock 调外网机 npm 生成 package-lock.json。
//
// 依赖来源合并写入临时目录的 package.json（文件依赖在前、命令行包名在后，
// 同名键命令行优先）；npm 不存在或版本低于 7 时报错，同时提示改用现成 lock。
//
//	@param packages []string 命令行 包名[@range] 列表。
//	@param manifestFile string 项目 package.json 路径，可为空。
//	@param registry string npm registry 地址。
//	@return string 生成的 lock 路径（临时目录内）。
//	@return func() 清理临时目录的函数。
//	@return error npm 缺失、版本过低、输入为空或生成失败的原因。
func generateNpmLock(packages []string, manifestFile, registry string) (string, func(), error) {
	if _, err := exec.LookPath("npm"); err != nil {
		return "", nil, fmt.Errorf("未找到 npm；请安装 npm ≥ 7，或用 -r 提供现成的 package-lock.json（无需 npm）")
	}
	major, err := npmMajorVersion()
	if err != nil {
		return "", nil, err
	}
	if major < 7 {
		return "", nil, fmt.Errorf("npm 版本为 %d，需 ≥ 7 才能生成 lockfileVersion 2/3 的 lock；或用 -r 提供现成的 package-lock.json", major)
	}
	deps := map[string]string{}
	if manifestFile != "" {
		if err := readPkgDeps(manifestFile, deps); err != nil {
			return "", nil, err
		}
	}
	for _, spec := range packages {
		name, rng := splitNpmSpec(spec)
		if !npm.ValidName(name) {
			return "", nil, fmt.Errorf("非法 npm 包名 %q（scoped 包形如 @scope/name）", name)
		}
		if rng == "" {
			rng = "*"
		}
		deps[name] = rng
	}
	if len(deps) == 0 {
		return "", nil, fmt.Errorf("没有可下载的包（package.json 无任何依赖段）")
	}
	tmpDir, err := os.MkdirTemp("", "pkgmirror-npm-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmpDir) }
	pkg := struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}{Name: "pkgmirror-download", Version: "0.0.0", Dependencies: deps}
	data, err := json.Marshal(pkg)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), data, 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	// --package-lock-only 只生成 lock 不安装；--no-audit/--no-fund 跳过无关节点
	cmd := exec.Command("npm", "install", "--package-lock-only", "--no-audit", "--no-fund", "--registry", registry)
	cmd.Dir = tmpDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("npm install --package-lock-only 失败: %w\n%s", err, out)
	}
	lockPath := filepath.Join(tmpDir, "package-lock.json")
	if _, err := os.Stat(lockPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("npm 未生成 package-lock.json（输出：%s）", strings.TrimSpace(string(out)))
	}
	return lockPath, cleanup, nil
}

// npmMajorVersion 返回外网机 npm 的主版本号。
//
//	@return int 主版本（如 npm 10.8.2 返回 10）。
//	@return error npm 执行失败或版本号无法解析时的错误。
func npmMajorVersion() (int, error) {
	out, err := exec.Command("npm", "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("执行 npm --version 失败: %w", err)
	}
	major, _, _ := strings.Cut(strings.TrimSpace(string(out)), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, fmt.Errorf("无法解析 npm 版本号 %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// readPkgDeps 读取 package.json 的三个依赖段合并进 deps。
//
// 全量收 dependencies + devDependencies + optionalDependencies（离线环境
// 需要完整构建链）；已存在的键不覆盖（调用方保证命令行包名后写入从而优先）。
//
//	@param path string package.json 路径。
//	@param deps map[string]string 依赖合并目标（包名 → 版本 range）。
//	@return error 文件读取或 JSON 解析失败的原因。
func readPkgDeps(path string, deps map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 package.json 失败: %w", err)
	}
	var pj struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.OptionalDependencies} {
		for k, v := range m {
			deps[k] = v
		}
	}
	return nil
}

// splitNpmSpec 拆分 包名[@range]；scoped 包名开头的 @ 是 scope 前缀，
// 不作为分隔符。
//
//	@param spec string 原始参数（如 lodash、lodash@^4、@babel/core@^7）。
//	@return string 包名（@babel/core@^7 → @babel/core）。
//	@return string 版本 range（裸包名为空串）。
func splitNpmSpec(spec string) (name, rng string) {
	rest := spec
	if strings.HasPrefix(spec, "@") {
		rest = spec[1:]
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		cut := i + len(spec) - len(rest)
		return spec[:cut], spec[cut+1:]
	}
	return spec, ""
}
