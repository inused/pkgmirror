package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pkgmirror/internal/gomod"
	"pkgmirror/internal/manifest"
)

// DefaultGoProxy 是官方 module proxy，未配置镜像源时的兜底值。
const DefaultGoProxy = "https://proxy.golang.org"

// ResolveGoProxy 按三级来源合成上游 module proxy 地址：
// 命令行 --goproxy > client.toml goproxy > GOPROXY 环境变量（取逗号
// 分隔首段，忽略 direct 等非 URL 项）> 官方源。
//
//	@param explicit string 命令行参数值，可为空。
//	@param fromConfig string 配置文件值，可为空。
//	@return string 实际使用的 proxy 根地址。
func ResolveGoProxy(explicit, fromConfig string) string {
	for _, v := range []string{explicit, fromConfig} {
		if v != "" {
			return firstProxyURL(v)
		}
	}
	if v := os.Getenv("GOPROXY"); v != "" {
		if u := firstProxyURL(v); u != "" {
			return u
		}
	}
	return DefaultGoProxy
}

// firstProxyURL 从 GOPROXY 形式的取值（逗号分隔的 URL 与 direct/off 混合）
// 中取第一个 URL 项；全为 direct/off 时返回空串。
//
//	@param v string GOPROXY 形式的取值。
//	@return string 首个 URL 项；无 URL 项时为空串。
func firstProxyURL(v string) string {
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item != "" && item != "direct" && item != "off" &&
			(strings.HasPrefix(item, "http://") || strings.HasPrefix(item, "https://")) {
			return strings.TrimSuffix(item, "/")
		}
	}
	return ""
}

// DownloadGomodOptions 是 download gomod 子命令的输入。
type DownloadGomodOptions struct {
	DownloadCommon
	// GoSumFile 是 -r 指定的 go.sum 路径，可为空（仅用命令行模块）。
	GoSumFile string
	// Modules 是命令行传入的模块引用列表，支持四种形式：
	// 模块（自动取最新稳定版）、模块@latest、模块@v1.8（该 minor 线最新
	// patch）、模块@v1.8.1（精确）。命令行目标会递归展开依赖闭包。
	Modules []string
}

// goRef 是一条待解析的命令行模块引用。
type goRef struct {
	Module string // 原始模块路径
	Ref    string // 版本引用：空（=latest）、"latest"、"v1.8" 前缀或 "v1.8.1" 精确
}

// exactVerRe 匹配完整三段精确版本（无需上游查询即可定位）。
var exactVerRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// RunDownloadGomod 执行 download gomod：收集 go.sum 与命令行目标，命令行
// 引用先向上游解析出具体版本（@latest 或 minor 前缀查询），再以 .mod 的
// require 声明为边全量递归展开依赖闭包（展开时顺带下载 .mod），最后统一
// 下载 .zip/.info。任一文件下载失败即中止，已下载的文件保留，便于重跑续传。
//
// go.sum 条目与命令行目标均递归展开 require，保留遇到的各模块版本。
// 下载端计算 sha256 写入 manifest；zip 的 h1 校验由内网 Go 命令按项目
// go.sum 执行。
//
//	@param opts DownloadGomodOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadGomod(opts DownloadGomodOptions) error {
	refs, static, err := splitGoRefs(opts)
	if err != nil {
		return err
	}
	if len(refs) == 0 && len(static) == 0 {
		return fmt.Errorf("请指定模块：-r go.sum 或命令行 模块[@版本]（如 github.com/spf13/cobra 或 ...@v1.8.1）")
	}
	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	proxy := strings.TrimSuffix(opts.GoProxyURL, "/")
	if proxy == "" {
		proxy = DefaultGoProxy
	}
	mf := newManifest(opts.Version)
	seen := map[gomod.ModuleVersion]bool{}
	for _, mv := range static {
		seen[mv] = true
	}
	// work 是待展开的模块版本队列（.mod 拉取 + require 解析）
	work := append([]gomod.ModuleVersion{}, static...)
	log.Printf("go.sum 与精确条目共 %d 个模块版本", len(static))

	// 命令行引用解析出具体版本后并入展开队列
	for _, ref := range refs {
		ver, err := resolveGoRef(ctx, client, proxy, ref)
		if err != nil {
			return err
		}
		mv := gomod.ModuleVersion{Module: ref.Module, Version: ver}
		log.Printf("%s: %s → %s", ref.Module, refDesc(ref), ver)
		if !seen[mv] {
			seen[mv] = true
			work = append(work, mv)
		}
	}

	// 阶段一：递归展开——逐个拉 .mod（落盘登记）并解析 require 入队
	if len(refs) > 0 {
		log.Printf("开始按 .mod 的 require 递归展开依赖闭包")
	}
	for i := 0; i < len(work); i++ {
		t := work[i]
		if err := fetchGoFile(ctx, client, proxy, opts.OutDir, t, "mod", &mf); err != nil {
			return err
		}
		content, err := os.ReadFile(filepath.Join(opts.OutDir, "gomod", filepath.FromSlash(t.Module), "@v", t.Version+".mod"))
		if err != nil {
			return err
		}
		for _, dep := range gomod.ParseRequires(string(content)) {
			if !seen[dep] {
				seen[dep] = true
				work = append(work, dep)
			}
		}
	}
	if len(refs) > 0 {
		log.Printf("展开完成：共 %d 个模块版本（含依赖）", len(work))
	}

	// 阶段二：补齐 .zip 与 .info（.mod 已在展开阶段落盘）
	for _, t := range work {
		if err := fetchGoFile(ctx, client, proxy, opts.OutDir, t, "info", &mf); err != nil {
			return err
		}
		if err := fetchGoFile(ctx, client, proxy, opts.OutDir, t, "zip", &mf); err != nil {
			return err
		}
	}
	return saveManifest(opts.OutDir, mf)
}

// fetchGoFile 下载一个模块版本的单个伴随文件（.mod/.zip/.info）并登记清单。
//
// @param ctx context.Context 上下文。
// @param client *http.Client HTTP 客户端。
// @param proxy string 上游 proxy 根地址。
// @param outDir string 下载产出目录。
// @param t gomod.ModuleVersion 目标模块版本。
// @param ext string 文件扩展（mod/zip/info）。
// @param mf *manifest.Manifest 待追加的清单。
// @return error 下载失败的原因。
func fetchGoFile(ctx context.Context, client *http.Client, proxy, outDir string, t gomod.ModuleVersion, ext string, mf *manifest.Manifest) error {
	filename := t.Version + "." + ext
	log.Printf("%s@%s.%s", t.Module, t.Version, ext)
	url := fmt.Sprintf("%s/%s/@v/%s", proxy, gomod.EscapeModulePath(t.Module), filename)
	dest := filepath.Join(outDir, "gomod", filepath.FromSlash(t.Module), "@v", filename)
	// 上游不提供 sha256（go.sum 的 h1 是域分隔哈希，不可直接比对），
	// 传空跳过校验，落盘后由 fetchAndRecord 计算实际哈希入清单
	return fetchAndRecord(ctx, client, url, dest, "", outDir, mf)
}

// resolveGoRef 把命令行模块引用解析为具体版本。
//
// 裸名与 @latest 请求上游 @latest 端点（最新稳定版）；v 开头的前缀引用
// （如 v1.8）请求 @v/list 后取该前缀线上最新稳定 patch。两者都是上游
// proxy 的官方端点，不依赖本地 go 命令。
//
// @param ctx context.Context 上下文。
// @param client *http.Client HTTP 客户端。
// @param proxy string 上游 proxy 根地址。
// @param r goRef 待解析的引用。
// @return string 解析出的具体版本（如 v1.8.1）。
// @return error 上游不可达、模块不存在或前缀无匹配版本时的错误。
func resolveGoRef(ctx context.Context, client *http.Client, proxy string, r goRef) (string, error) {
	base := fmt.Sprintf("%s/%s/@v", proxy, gomod.EscapeModulePath(r.Module))
	if r.Ref == "" || r.Ref == "latest" {
		resp, err := fetchGoURL(ctx, client, strings.TrimSuffix(base, "/@v")+"/@latest")
		if err != nil {
			return "", fmt.Errorf("查询 %s 的最新版本失败（模块可能不存在）: %w", r.Module, err)
		}
		defer resp.Body.Close()
		var info struct {
			Version string `json:"Version"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Version == "" {
			return "", fmt.Errorf("解析 %s 的 @latest 响应失败", r.Module)
		}
		return info.Version, nil
	}
	// minor 前缀引用：拉 @v/list 取该线最新稳定版
	resp, err := fetchGoURL(ctx, client, base+"/list")
	if err != nil {
		return "", fmt.Errorf("查询 %s 的版本列表失败: %w", r.Module, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	prefix := r.Ref + "."
	var cands []string
	for _, line := range strings.Split(string(data), "\n") {
		v := strings.TrimSpace(line)
		if strings.HasPrefix(v, prefix) && gomod.IsStablePrefix(v) {
			cands = append(cands, v)
		}
	}
	if best := gomod.LatestStable(cands); best != "" {
		return best, nil
	}
	return "", fmt.Errorf("%s 没有 %s 前缀的稳定版本", r.Module, r.Ref)
}

// fetchGoURL 发起一次 GET 并校验状态码。
//
// @param ctx context.Context 上下文。
// @param client *http.Client HTTP 客户端。
// @param url string 请求地址。
// @return *http.Response 200 响应（调用方负责关闭 Body）。
// @return error 请求失败或非 200 时的错误。
func fetchGoURL(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("上游返回 %s", resp.Status)
	}
	return resp, nil
}

// splitGoRefs 把输入拆分为：需上游解析的引用列表与已确定版本的静态目标
// （go.sum 全部条目 + 命令行精确条目）。
//
//	@param opts DownloadGomodOptions 已解析的选项。
//	@return []goRef 需要解析的命令行引用（裸名 / @latest / minor 前缀）。
//	@return []gomod.ModuleVersion 静态目标集合。
//	@return error go.sum 读取失败或命令行条目非法时的错误。
func splitGoRefs(opts DownloadGomodOptions) ([]goRef, []gomod.ModuleVersion, error) {
	var refs []goRef
	seen := map[gomod.ModuleVersion]bool{}
	var static []gomod.ModuleVersion
	if opts.GoSumFile != "" {
		f, err := os.Open(opts.GoSumFile)
		if err != nil {
			return nil, nil, fmt.Errorf("读取 go.sum 失败: %w", err)
		}
		mvs, err := gomod.ParseGoSum(f)
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		for _, mv := range mvs {
			if !seen[mv] {
				seen[mv] = true
				static = append(static, mv)
			}
		}
		log.Printf("go.sum 解析得到 %d 个模块版本", len(mvs))
	}
	for _, arg := range opts.Modules {
		mod, ref, _ := strings.Cut(arg, "@")
		if !gomod.ValidModulePath(mod) {
			return nil, nil, fmt.Errorf("模块 %q 的路径非法", arg)
		}
		switch {
		case ref == "" || ref == "latest":
			refs = append(refs, goRef{Module: mod, Ref: ref})
		case exactVerRe.MatchString(ref):
			mv := gomod.ModuleVersion{Module: mod, Version: ref}
			if !seen[mv] {
				seen[mv] = true
				static = append(static, mv)
			}
		case strings.HasPrefix(ref, "v"):
			refs = append(refs, goRef{Module: mod, Ref: ref})
		default:
			return nil, nil, fmt.Errorf("模块 %q 的版本引用 %q 非法（支持 latest、v1.8 前缀或 v1.8.1 精确）", arg, ref)
		}
	}
	sort.Slice(static, func(i, j int) bool {
		if static[i].Module != static[j].Module {
			return static[i].Module < static[j].Module
		}
		return gomod.CompareVersion(static[i].Version, static[j].Version) < 0
	})
	return refs, static, nil
}

// refDesc 返回引用的日志描述（空引用按 latest 展示）。
//
//	@param r goRef 命令行引用。
//	@return string 日志描述。
func refDesc(r goRef) string {
	if r.Ref == "" {
		return "latest"
	}
	return r.Ref
}
