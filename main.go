// pymirror 是 uv/pip 离线镜像工具：server 子命令提供镜像服务，
// download 与 upload 子命令完成按需下载与上传。
//
// 用法：
//
//	pymirror server  [--config server.toml]
//	pymirror download python 3.12 [3.12.4 ...] [--platform linux] [--arch x86_64]
//	pymirror download pypi --python 3.12 包名==版本... [-r FILE] [-o 目录]
//	pymirror upload --server http://user:pass@host:port <目录>
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"pymirror/internal/redact"
	"strings"

	"pymirror/internal/client"
	"pymirror/internal/server"
)

// Version 是 pymirror 版本号，写入 manifest 的 generator 字段。
const Version = "0.1.0"

func main() {
	log.SetFlags(0)
	log.SetPrefix("")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(os.Args[2:])
	case "download":
		err = runDownload(os.Args[2:])
	case "upload":
		err = runUpload(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", redact.Text(os.Args[1]))
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("错误: %s", redact.Text(err.Error()))
	}
}

// usage 打印顶层帮助：子命令一览与整体使用流程。
func usage() {
	fmt.Print(`pymirror ` + Version + ` - uv/pip 离线镜像工具

用法:
  pymirror <server|download|upload> [参数]
  各层级均支持 -h 查看该层用法

子命令:
  server    启动镜像服务端（pip/uv 安装源、文件浏览与上传页面），长期运行
  download  按需下载 PyPI 包与 CPython 安装包（代理/索引/反代可配），
            产出目录供 upload 上传
  upload    把 download 产出的目录上传到服务端

整体流程:
  1. pymirror download python 3.12                    下载 Python 安装包
     pymirror download pypi --python 3.12 six         下载 PyPI 包
  2. pymirror upload --server http://user:pass@host:port downloads
  3. 使用方配置（一次即可，之后 uv pip install / uv python install 走镜像）:
     export UV_INDEX_URL=http://user:pass@host:port/simple
     export UV_PYTHON_INSTALL_MIRROR=http://user:pass@host:port/python
`)
}

// usageDownload 打印 download 层帮助：两种下载类型概览与公共能力指引。
// 公共选项实际由 python/pypi 层解析（download 层不直接接受），
// 因此这里只概述能力并指明语法所在层级，避免帮助与行为不符。
func usageDownload() {
	fmt.Print(`用法:
  pymirror download <python|pypi> [参数] [选项]
  对 python 或 pypi 加 -h 查看各自完整的参数与选项语法

下载类型:
  python    下载 CPython 安装包（供 uv python install 使用）
            版本为位置参数（如 3.12 或 3.12.4），支持按操作系统与架构
            过滤，--all 可下载全部历史补丁版
  pypi      下载 PyPI 包，自动解析全部依赖
            包名为位置参数（如 six 或 six==1.17.0；裸包名自动取兼容
            目标 Python 的最新版），--python 指定目标版本并过滤 wheel

两类下载共有的能力（选项写在 python/pypi 之后，语法见各自 -h）:
  HTTP 代理（--proxy，或 HTTP_PROXY/HTTPS_PROXY 环境变量）、
  PyPI 索引切换（--index-url，或 UV_INDEX_URL/PIP_INDEX_URL/pip.conf）、
  GitHub 反代（--github-prefix）、客户端配置文件（--config，默认 client.toml）、
  输出目录（-o，默认固定 downloads/，重复执行自动增量合并清单）
`)
}

// usageCommonOptions 是 download 两个类型共有的选项说明。
const usageCommonOptions = `公共选项（python/pypi 通用）:
  --index-url URL   PyPI 索引；默认依次取 client.toml、UV_INDEX_URL、
                    PIP_INDEX_URL、pip.conf、官方源
  --github-prefix P GitHub 反代前缀（Python 安装包与 uv 元数据托管在 GitHub）
  --proxy URL       HTTP 代理；不指定时沿用 HTTP_PROXY/HTTPS_PROXY 环境变量
  --config FILE     客户端配置文件，默认当前目录 client.toml，
                    模板见 resources/client.example.toml
  -o DIR            下载产出目录；默认固定当前目录 downloads/，重复下载
                    自动增量合并清单；目录是累积的，过滤条件只影响本次新增`

// usageDownloadPython 打印 download python 层帮助。
func usageDownloadPython() {
	fmt.Print(`用法:
  pymirror download python <版本>... [选项]

下载 CPython 安装包（供 uv python install 使用）。

参数与选项:
  版本             一个或多个，空格或逗号分隔；3.12 取该系列最新补丁版，
                   3.12.4 精确到补丁版
  --platform LIST  按操作系统过滤：linux,windows,darwin；默认全部
  --arch LIST      按架构过滤：x86_64,aarch64（别名 amd64/arm64）；默认全部
  --all            对未指定补丁号的版本，下载该系列全部历史补丁版

` + usageCommonOptions + `

示例:
  pymirror download python 3.12
  pymirror download python 3.12.4 3.13.1 --platform linux --arch x86_64
`)
}

// usageDownloadPypi 打印 download pypi 层帮助。
func usageDownloadPypi() {
	fmt.Print(`用法:
  pymirror download pypi <包名[==版本]>... [选项]

下载 PyPI 包，自动解析全部依赖。

参数与选项:
  包名             一个或多个，也可用 -r 需求文件；带 ==版本 时锁定版本，
                   裸包名自动取兼容目标 Python 版本的最新版
  --python LIST    目标 Python 版本，必填（用于依赖解析与 wheel 过滤，只下载
                   兼容该版本的 wheel；纯 Python 包与 sdist 始终保留）
  -r FILE          需求文件（每行一个包名或 包名==版本）
  --platform LIST  按操作系统过滤 wheel：linux,windows,darwin；默认全部
  --arch LIST      按架构过滤 wheel：x86_64,aarch64；默认全部

` + usageCommonOptions + `

示例:
  pymirror download pypi --python 3.12 six
  pymirror download pypi --python 3.12 six==1.17.0
  pymirror download pypi --python 3.11,3.12 --platform windows -r requirements.in
`)
}

// usageServer 打印 server 层帮助。
func usageServer() {
	fmt.Print(`用法:
  pymirror server [--config FILE]

启动镜像服务端，提供 PyPI simple 索引、Python 安装包分发、
文件浏览与上传页面，长期运行。

  --config FILE  配置文件路径，默认当前目录 server.toml，
                 模板见 resources/server.example.toml
`)
}

// usageUpload 打印 upload 层帮助。
func usageUpload() {
	fmt.Print(`用法:
  pymirror upload --server URL [选项] [目录]

把 download 产出的目录（含 manifest.json）上传到服务端，
已存在且内容相同的文件自动跳过，可重复执行。
目录缺省为 download 的默认产出目录 downloads/；目录或清单不存在时报错。

  --server URL   服务端地址，如 http://user:pass@host:port（端口以服务端
                 配置为准），凭据可直接嵌在地址里
  --user NAME    Basic auth 用户名（URL 已嵌凭据时可省略）
  --pass PASS    Basic auth 密码
`)
}

// runServer 启动服务端子命令。
//
//	@param args []string 子命令参数（已去掉子命令名）。
//	@return error 配置加载或服务运行失败的原因。
func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.Usage = usageServer
	configPath := fs.String("config", "server.toml", "服务端配置文件路径")
	fs.Parse(args)
	if _, err := os.Stat(*configPath); err != nil {
		log.Printf("提示: 未找到配置文件 %s，使用默认值（监听 :8080，数据目录 ./data，未启用认证）", *configPath)
	}
	cfg, err := server.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	return server.New(cfg).ListenAndServe()
}

// runDownload 按镜像类型分发到对应下载命令。
//
//	@param args []string 子命令参数，首项为 python 或 pypi。
//	@return error 选项解析或下载失败的原因。
func runDownload(args []string) error {
	if len(args) == 0 {
		usageDownload()
		return fmt.Errorf("请指定下载类型：python 或 pypi")
	}
	// 允许在 download 一层直接求助
	switch args[0] {
	case "-h", "--help", "help":
		usageDownload()
		return nil
	case "python":
		return runDownloadPython(args[1:])
	case "pypi":
		return runDownloadPypi(args[1:])
	default:
		return fmt.Errorf("未知的下载类型 %q（应为 python 或 pypi）", args[0])
	}
}

// dlFlags 汇总 download 两个子命令共享的选项定义。
type dlFlags struct {
	fs           *flag.FlagSet
	platforms    *string
	archs        *string
	indexURL     *string
	githubPrefix *string
	proxy        *string
	configFile   *string
	outDir       *string
}

// newDownloadFlags 注册两个子命令共有的选项。
//
//	@param name string 子命令名（python 或 pypi）。
//	@return dlFlags 已注册公共选项的选项集。
func newDownloadFlags(name string) dlFlags {
	fs := flag.NewFlagSet("download "+name, flag.ExitOnError)
	// 该层 -h 只展示本层用法；由入口再指定 python 或 pypi 的专属帮助
	return dlFlags{
		fs:           fs,
		platforms:    fs.String("platform", "", "按操作系统过滤（linux/windows/darwin），逗号分隔，默认全部"),
		archs:        fs.String("arch", "", "按架构过滤（x86_64/aarch64，别名 amd64/arm64），逗号分隔，默认全部"),
		indexURL:     fs.String("index-url", "", "PyPI 索引地址"),
		githubPrefix: fs.String("github-prefix", "", "GitHub 反代前缀"),
		proxy:        fs.String("proxy", "", "HTTP 代理地址，如 http://127.0.0.1:7890"),
		configFile:   fs.String("config", "client.toml", "客户端配置文件路径，不存在则跳过"),
		outDir:       fs.String("o", "", "下载产出目录，默认固定 downloads/"),
	}
}

// dlStringFlags 是 download 子命令中需要带值的选项名集合。
var dlStringFlags = map[string]bool{
	"platform": true, "arch": true, "index-url": true, "github-prefix": true,
	"proxy": true, "config": true, "o": true, "r": true, "python": true,
}

// dlBoolFlags 是 download 子命令中的布尔选项名集合（不带值）。
var dlBoolFlags = map[string]bool{"all": true}

// reorderArgs 把选项（连同其值）移动到位置参数之前，再交给 flag 包解析。
//
// Go 的 flag 包遇到首个位置参数即停止解析，用户习惯把选项写在位置参数
// 之后（如 download python 3.12 --platform linux），重排后两种顺序都可用。
// 值与选项名合并（--platform=linux）或分离（--platform linux）均可识别；
// 未知选项原样保留在前段，由 flag 包报“未定义”错误；带值选项位于参数
// 末尾无处取值时在此报错——若放任其进入重排结果，flag 包会把后面的
// 位置参数误当它的值（或静默丢弃），错误信息会严重误导。
//
//	@param args []string 原始参数。
//	@param stringFlags map[string]bool 需要带值的选项名集合。
//	@param boolFlags map[string]bool 不带值的选项名集合。
//	@return []string 重排后的参数（选项在前，位置参数在后）。
//	@return error 带值选项缺少值时的错误。
func reorderArgs(args []string, stringFlags, boolFlags map[string]bool) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			hasValue := strings.Contains(name, "=")
			if hasValue || boolFlags[name] || !stringFlags[name] {
				// -x=v、布尔选项、未知选项：单参数自包含
				flags = append(flags, a)
				continue
			}
			// 分离写法的带值选项：连同下一个参数一起前移；末尾无值可配对时报错
			if i+1 >= len(args) {
				return nil, fmt.Errorf("选项 %s 缺少值", a)
			}
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...), nil
}

// resolveCommon 按三级来源合成公共配置。
//
//	@param f dlFlags 选项集（须已 Parse）。
//	@return client.DownloadCommon 合成后的公共配置。
//	@return error 配置文件解析失败的原因。
func (f dlFlags) resolveCommon() (client.DownloadCommon, error) {
	cfg, err := client.LoadClientConfig(*f.configFile)
	if err != nil {
		return client.DownloadCommon{}, err
	}
	return client.DownloadCommon{
		Platforms:    splitList(*f.platforms),
		Archs:        splitList(*f.archs),
		IndexURL:     client.ResolveIndexURL(*f.indexURL, cfg.IndexURL),
		GitHubPrefix: firstNonEmpty(*f.githubPrefix, cfg.GitHubPrefix, os.Getenv("GITHUB_PREFIX")),
		Proxy:        firstNonEmpty(*f.proxy, cfg.Proxy),
		OutDir:       *f.outDir,
		Version:      Version,
	}, nil
}

// runDownloadPython 执行 download python 子命令（位置参数为版本列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadPython(args []string) error {
	f := newDownloadFlags("python")
	f.fs.Usage = usageDownloadPython
	all := f.fs.Bool("all", false, "对未指定补丁号的版本下载全部历史补丁")
	reordered, err := reorderArgs(args, dlStringFlags, dlBoolFlags)
	if err != nil {
		return err
	}
	f.fs.Parse(reordered)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadPythonOptions{DownloadCommon: common, AllPatches: *all}
	// 版本作为位置参数：每个参数支持逗号分隔多个版本
	for _, v := range f.fs.Args() {
		for _, item := range splitList(v) {
			spec, err := client.ParsePythonSpec(item)
			if err != nil {
				return err
			}
			opts.Pythons = append(opts.Pythons, spec)
		}
	}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PymirrorUAVersion = Version
	log.Printf("Python 安装包下载，代理: %s", proxyDesc(opts.Proxy))
	return client.RunDownloadPython(opts)
}

// runDownloadPypi 执行 download pypi 子命令（位置参数为 包名==版本 列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadPypi(args []string) error {
	f := newDownloadFlags("pypi")
	reqFile := f.fs.String("r", "", "需求文件路径（每行一个 包名==版本）")
	f.fs.Usage = usageDownloadPypi
	pythons := f.fs.String("python", "", "目标 Python 版本，逗号分隔，必填（依赖解析与 wheel 过滤）")
	reordered, err := reorderArgs(args, dlStringFlags, dlBoolFlags)
	if err != nil {
		return err
	}
	f.fs.Parse(reordered)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadPypiOptions{DownloadCommon: common, RequirementsFile: *reqFile, Packages: f.fs.Args()}
	for _, v := range splitList(*pythons) {
		spec, err := client.ParsePythonSpec(v)
		if err != nil {
			return err
		}
		opts.Pythons = append(opts.Pythons, spec)
	}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PymirrorUAVersion = Version
	// 打印目标 Python 版本，让 --python 的接收与作用可被直接观察
	var pythonDesc []string
	for _, spec := range opts.Pythons {
		pythonDesc = append(pythonDesc, spec.String())
	}
	log.Printf("PyPI 索引: %s，目标 Python: %s，代理: %s",
		redact.URL(opts.IndexURL), strings.Join(pythonDesc, ","), proxyDesc(opts.Proxy))
	return client.RunDownloadPypi(opts)
}

// runUpload 执行上传子命令。
//
//	@param args []string 子命令参数（已去掉子命令名）。
//	@return error 选项解析或上传失败的原因。
func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	fs.Usage = usageUpload
	serverURL := fs.String("server", "", "服务端地址")
	user := fs.String("user", "", "Basic auth 用户名")
	pass := fs.String("pass", "", "Basic auth 密码")
	reordered, err := reorderArgs(args, map[string]bool{"server": true, "user": true, "pass": true}, nil)
	if err != nil {
		return err
	}
	fs.Parse(reordered)
	if fs.NArg() > 1 {
		return fmt.Errorf("只能指定一个目录参数；或加 -h 查看完整说明")
	}
	if *serverURL == "" {
		return fmt.Errorf("必须用 --server 指定服务端地址，如 http://user:pass@host:port")
	}
	// 目录缺省时使用 download 的默认产出目录；目录或其清单不存在都会在读取时报错
	dir := fs.Arg(0)
	if dir == "" {
		dir = "downloads"
	}
	return client.RunUpload(client.UploadOptions{
		Dir:    dir,
		Server: *serverURL,
		User:   *user,
		Pass:   *pass,
	})
}

// proxyDesc 描述实际生效的代理，用于启动日志。
//
//	@param proxy string 已解析的代理地址。
//	@return string 隐藏凭据的代理地址；空串时描述环境变量回退行为。
func proxyDesc(proxy string) string {
	if proxy != "" {
		return redact.URL(proxy)
	}
	if os.Getenv("HTTPS_PROXY") != "" || os.Getenv("https_proxy") != "" ||
		os.Getenv("HTTP_PROXY") != "" || os.Getenv("http_proxy") != "" {
		return "沿用 HTTP_PROXY/HTTPS_PROXY 环境变量"
	}
	return "直连"
}

// firstNonEmpty 返回第一个非空字符串，全空返回空串。
//
//	@param vals ...string 候选值，按优先级从高到低。
//	@return string 选中的值。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitList 拆分逗号分隔的列表参数，去除空白项。
//
//	@param s string 原始参数值。
//	@return []string 拆分结果，空参数返回空切片。
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
