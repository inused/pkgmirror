// pkgmirror 是离线包镜像工具（Python / Node.js / Go）：server 子命令提供镜像服务，
// download、upload、delete 子命令分别下载、上传和删除镜像资源。
//
// 用法：
//
//	pkgmirror server  [--config server.toml]
//	pkgmirror download python 3.12 [3.12.4 ...] [--platform linux] [--arch x86_64]
//	pkgmirror download pypi --python 3.12 包名==版本... [-r FILE] [-o 目录]
//	pkgmirror download node 22 [--lts] [--dist-url URL] [-o 目录]
//	pkgmirror upload --server http://user:pass@host:port <目录>
package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"pkgmirror/internal/client"
	"pkgmirror/internal/golang"
	"pkgmirror/internal/node"
	"pkgmirror/internal/paths"
	"pkgmirror/internal/redact"
	"pkgmirror/internal/server"
)

// Version 是 pkgmirror 版本号，写入 manifest 的 generator 字段。
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
	case "delete":
		err = runDelete(os.Args[2:])
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
	fmt.Print(`pkgmirror ` + Version + ` - 离线包镜像工具（Python / Node.js / Go）

用法:
  pkgmirror <server|download|upload|delete> [参数]
  任何一层都可以加 -h 查看这一层的用法

子命令:
  server    启动镜像服务端，长期运行。
            内网机器从它这里装 Python / Node / Go，以及 pip、npm、go 的包
  download  在外网机器上按需下载，产出目录拷回内网后用 upload 上传
  upload    把 download 产出的目录上传到服务端
  delete    删除服务端上已镜像的内容，可以连不再有人用的依赖一起删

整体流程:
  1. 外网机下载（按需选类型执行）:
     pkgmirror download python 3.12                  下载 Python 安装包
     pkgmirror download pypi --python 3.12 six       下载 PyPI 包
     pkgmirror download node 22                      下载 Node 安装包
     pkgmirror download npm lodash                   下载 npm 包
  2. pkgmirror upload --server http://user:pass@host:port downloads
  3. 内网机器按服务端首页给出的配置接入（配一次即可），
     之后 uv pip install、uv python install、npm install、go build 都走镜像
`)
}

// usageDownload 打印 download 层帮助：下载类型概览与公共能力指引。
// 选项由各类型子命令解析，此处列出名称和适用类型。
func usageDownload() {
	fmt.Print(`用法:
  pkgmirror download <python|pypi|node|npm|golang|gomod> [参数] [选项]
  对每个类型加 -h 查看它的参数和选项

下载类型:
  python  下载 CPython 安装包，给内网 uv python install 用。
          版本直接写在命令后面（如 3.12 或 3.12.4），可按操作系统和架构筛；
          --all 把该系列的历史补丁版全部下载
  pypi    下载 PyPI 包，输入含裸包名或版本范围时解析依赖。
          包名直接写在命令后面（如 six 或 six==1.17.0）；
          只写包名时自动选兼容目标 Python 的最新版；
          全部写成 包名==版本 时直接下载清单中的版本，无需 uv
  node    下载 Node.js 官方压缩包，内网解压配好 PATH 即可用，
          版本管理器也能从镜像装。版本直接写在命令后面（如 22 或 22.11.0）；
          --lts 下载当前最新 LTS
  npm     下载 npm 包，自动带上全部依赖（@scope/ 开头的包也支持）。
          包名直接写在命令后面（如 lodash 或 lodash@^4）；
          -r 可直接给 package.json 或现成的 package-lock.json
  golang  下载 Go 官方发行压缩包，内网解压配好 PATH 即可用，
          mise / asdf-golang 也能从镜像装。
          版本直接写在命令后面（如 1.23 或 1.23.4）；
          默认只下主流平台，可指定任意官方平台
  gomod   下载 Go 模块（.mod / .zip / .info 三个文件），给内网 GOPROXY 用。
          -r 指定项目 go.sum 即可下载它记录的全部模块，无需其他工具；
          直接给模块名时取最新稳定版，并按 .mod 里的 require 一层层带出全部依赖

下载选项与适用类型（写在类型后面，完整语法见各自 -h）:
  --proxy URL           HTTP 代理；不指定时用 HTTP_PROXY / HTTPS_PROXY 环境变量
  -i, --index-url URL   PyPI 索引地址（pypi 层）；
                        也可用 UV_INDEX_URL / PIP_INDEX_URL 环境变量或 pip.conf
  --dist-url URL        下载源地址：node 层用（或 NODE_DIST_URL 环境变量）、
                        golang 层用（或 GOLANG_DIST_URL 环境变量）
  -R, --registry URL    npm registry 地址（npm 层；或 NPM_CONFIG_REGISTRY）
  --goproxy URL         Go 模块代理地址（gomod 层；或 GOPROXY 环境变量）
  --github-prefix P     GitHub 反代前缀（CPython 安装包与 uv 元数据托管在 GitHub）
  -c, --config FILE     客户端配置文件，
                        默认找当前目录和二进制目录下的 client.toml，
                        模板见 resources/client.example.toml
  -o DIR                下载产出目录，默认当前目录下的 downloads/；
                        重复执行不重下已有文件，清单自动合并
`)
}

// usageDownloadNpm 打印 download npm 层帮助。
func usageDownloadNpm() {
	fmt.Print(`用法:
  pkgmirror download npm <包名[@版本]>... [选项]

下载 npm 包和它的全部依赖（包括依赖的依赖，@scope/ 开头的包也支持）。
内网 npm / pnpm / yarn 配好 registry 后就能安装，接入配置见服务端首页。

参数与选项:
  包名                 一个或多个，写法有三种：
                         lodash        取最新版
                         lodash@4      取 4 这个大版本的最新版
                         lodash@^4     按 ^4 规则选版本
                       带 @scope/ 前缀的写法相同，如 @babel/core@^7
  -r FILE              给 package.json 或现成的 package-lock.json：
                       package.json 把 dependencies、devDependencies、
                       optionalDependencies 三类依赖全部收进来；
                       package-lock.json 直接按锁文件内容下载，本机不用装 npm。
                       两者都与命令行的包名二选一
  -R, --registry URL   npm registry 地址。默认依次取：client.toml、
                       NPM_CONFIG_REGISTRY 环境变量、官方源
  -p, --platform LIST  只下指定操作系统的二进制包：linux,windows,darwin；
                       默认全部
  --arch LIST          只下指定架构的二进制包：x86_64,aarch64；默认全部
  --node LIST          目标 Node.js 版本（如 18 或 18.20.1），
                       校验每个包要求的 engines.node 兼不兼容：
                         18        按 18.0.0 校验（要求兼容最老的 18.x）
                         18.20.1   按具体版本校验
                       目标版本只用于校验，不兼容时需调整依赖或 Node.js 版本

只写包名或给 package.json 时，外网机需要装有 npm ≥ 7（用它解析依赖）；
给 package-lock.json 时无需本机 npm。

示例:
  pkgmirror download npm lodash
  pkgmirror download npm lodash@^4 @babel/core
  pkgmirror download npm -r /path/to/package-lock.json --platform windows
  pkgmirror download npm --node 18.20.1 express
`)
}

// usageDownloadNode 打印 download node 层帮助。
func usageDownloadNode() {
	fmt.Print(`用法:
  pkgmirror download node [版本]... [选项]

下载 Node.js 官方发行版（nodejs.org/dist）的压缩包和官方校验文件 SHASUMS256.txt。
内网解压后把 bin 目录加进 PATH 就能用；服务端按官方的目录结构存放，
nvm / nvm-windows / n / mise 换个镜像地址就能直接装（接入配置见服务端首页）。

参数与选项:
  版本                 零个或多个，空格或逗号分隔。三种写法：
                         22        取 22 的最新补丁版
                         22.11     取 22.11 的最新补丁版
                         22.11.0   就下这个版本
  --lts                下载当前最新的 LTS 版本（与版本参数二选一）
  -p, --platform LIST  按操作系统过滤：linux,darwin,windows；默认全部
  --arch LIST          按架构过滤：x86_64,aarch64（amd64/arm64 是别名）；默认全部
  --dist-url URL       下载源地址。默认依次取：client.toml、
                       NODE_DIST_URL 环境变量、官方源（https://nodejs.org/dist）

只下可移植压缩包（linux / macos 是 tar.xz，windows 是 zip），
不下 msi / pkg 安装器、musl 变体和源码。文件按官方 SHASUMS256.txt 校验后才落盘。

示例:
  pkgmirror download node 22
  pkgmirror download node --lts --platform linux
  pkgmirror download node 20.18.2 22 --platform linux,darwin --arch x86_64
`)
}

// usageCommonOptions 是 download python 和 pypi 的公共选项说明。
const usageCommonOptions = `公共选项（download python 和 pypi 共用）:
  -i, --index-url URL     PyPI 索引地址。默认依次取：client.toml、UV_INDEX_URL、
                          PIP_INDEX_URL、pip.conf、官方源
  -g, --github-prefix P   GitHub 反代前缀（CPython 安装包和 uv 元数据放在 GitHub）
  -x, --proxy URL         HTTP 代理。不指定时用 HTTP_PROXY / HTTPS_PROXY 环境变量
  -c, --config FILE       客户端配置文件，
                          默认找当前目录和二进制目录下的 client.toml，
                          模板见 resources/client.example.toml
  -o DIR                  下载产出目录，默认当前目录下的 downloads/；
                          重复执行不重下已有文件，清单自动合并，
                          过滤条件只影响本次新增`

// usageDownloadGolang 打印 download golang 层帮助。
func usageDownloadGolang() {
	fmt.Print(`用法:
  pkgmirror download golang <版本>... [选项]

下载 Go 官方发行压缩包（dl.google.com/go 的文件）。
内网解压后把其中的 go/bin 目录加入 PATH；mise / asdf-golang 也能直接从镜像装。

参数与选项:
  版本                 一个或多个，空格或逗号分隔。两种写法：
                         1.23      取 1.23 的最新补丁版
                         1.23.4    就下这个版本
                       rc 等预发布版不下载
  -p, --platform LIST  按操作系统过滤：linux,darwin,windows（macos 是 darwin 的别名），
                       也可以写 aix、freebsd 等任意官方平台；不指定时默认 linux,darwin,windows
  --arch LIST          按架构过滤：amd64,arm64（x86_64/aarch64 是别名）；
                       也可以写 386、mips64le 等。不指定时默认 amd64,arm64
  --src                每个版本额外下载源码包（goX.Y.Z.src.tar.gz）
  --all-platforms      放开平台和架构的默认白名单，按指定条件选择官方归档
  --dist-url URL       下载源地址。默认依次取：client.toml 的 golang-dist-url、
                       GOLANG_DIST_URL 环境变量、官方源（dl.google.com/go）

平台和架构是两个独立的过滤条件：给了的按给定的精确匹配，
没给的用默认白名单，文件数量和大小取决于所选版本；
某个组合在该版本没有产物时自动跳过（比如 darwin 没有 386）。

示例:
  pkgmirror download golang 1.23
  pkgmirror download golang 1.23.4 --platform linux --arch amd64
  pkgmirror download golang 1.21 --all-platforms --src
`)
}

// usageDownloadGomod 打印 download gomod 层帮助。
func usageDownloadGomod() {
	fmt.Print(`用法:
  pkgmirror download gomod [-r go.sum] [模块[@版本]]... [选项]

下载 Go 模块的三个文件（.mod / .zip / .info）。内网把 GOPROXY 指向镜像后，
go build / go mod download 直接可用（接入配置见服务端首页）。

参数与选项:
  -r FILE        go.sum 文件路径，下载其中记录的模块版本及其 require 依赖，
                 无需本地 Go 命令
  模块           只写模块名（如 github.com/spf13/cobra）时，
                 查上游 @latest 取最新稳定版
  模块@latest    同上，写法更明确
  模块@v1.8      取 1.8 这条线的最新稳定补丁版（预发布不算）
  模块@v1.8.1    就下这个版本
  --goproxy URL  上游模块代理地址。默认依次取：client.toml 的 goproxy、
                 GOPROXY 环境变量（取第一段 URL）、官方源（proxy.golang.org）

go.sum 与命令行目标均按 .mod 中的 require 递归下载依赖，保留遇到的各模块版本。
内网 Go 命令按项目 go.sum 校验模块 zip 内容。

示例:
  pkgmirror download gomod -r /path/to/go.sum
  pkgmirror download gomod github.com/spf13/cobra
  pkgmirror download gomod github.com/spf13/cobra@v1.8
`)
}

// usageDownloadPython 打印 download python 层帮助。
func usageDownloadPython() {
	fmt.Print(`用法:
  pkgmirror download python <版本>... [选项]

下载 CPython 安装包（python-build-standalone 构建），给内网 uv python install 用。

参数与选项:
  版本                 一个或多个，空格或逗号分隔。两种写法：
                         3.12      取 3.12 的最新补丁版
                         3.12.4    就下这个版本
  -p, --platform LIST  按操作系统过滤：linux,windows,darwin；默认全部
  --arch LIST          按架构过滤：x86_64,aarch64（amd64/arm64 是别名）；默认全部
  --all                对只写到 3.12 这类版本，把该系列的历史补丁版全部下载

` + usageCommonOptions + `

示例:
  pkgmirror download python 3.12
  pkgmirror download python 3.12.4 3.13.1 --platform linux --arch x86_64
`)
}

// usageDownloadPypi 打印 download pypi 层帮助。
func usageDownloadPypi() {
	fmt.Print(`用法:
  pkgmirror download pypi <包名[==版本]>... [选项]

下载 PyPI 包；输入含裸包名或版本范围时，调用 uv 解析传递依赖。

参数与选项:
  包名                 一个或多个，也可以用 -r 从文件读。两种写法：
                         six          自动选兼容目标 Python 的最新版
                         six==1.17.0  就下这个版本
                       全部写成 包名==版本 时直接下载，传递依赖须一并列出
  -P, --python LIST    目标 Python 版本，必填。用来解析依赖和筛 wheel：
                       只下兼容该版本的 wheel；纯 Python 包和 sdist 始终保留
  -r FILE              需求文件，每行一个 包名 或 包名==版本
  -p, --platform LIST  按操作系统筛 wheel：linux,windows,darwin；默认全部
  --arch LIST          按架构筛 wheel：x86_64,aarch64；默认全部

` + usageCommonOptions + `

示例:
  pkgmirror download pypi --python 3.12 six
  pkgmirror download pypi --python 3.12 six==1.17.0
  pkgmirror download pypi --python 3.11,3.12 --platform windows -r requirements.in
`)
}

// usageServer 打印 server 层帮助。
func usageServer() {
	fmt.Print(`用法:
  pkgmirror server [--config FILE]

启动 Python、Node.js、Go 的安装包与包仓库镜像服务，
提供文件浏览、搜索、上传和删除预览页面。

  -c, --config FILE  配置文件路径。默认找当前目录和二进制目录下的 server.toml，
                     模板见 resources/server.example.toml
`)
}

// usageUpload 打印 upload 层帮助。
func usageUpload() {
	fmt.Print(`用法:
  pkgmirror upload --server URL [选项] [目录]

把 download 产出的目录（里面有 manifest.json）上传到服务端。
内容相同的文件自动跳过，重复执行是安全的。
目录不写时默认是 download 的产出目录 downloads/；目录或清单不存在时报错。

  -s, --server URL  服务端地址，如 http://user:pass@host:port，
                    端口以服务端配置为准，账号密码可以直接写在地址里
  --user NAME       Basic auth 用户名（地址里已带账号密码时可省略）
  --pass PASS       Basic auth 密码
`)
}

// usageDelete 打印 delete 层帮助。
func usageDelete() {
	fmt.Print(`用法:
  pkgmirror delete <pypi|npm|gomod> <包名|模块路径> [--deps] [选项]
  pkgmirror delete <python|node|golang> <版本|归档文件名> [--deps] [选项]

删除服务端上已镜像的内容。先打印要删的清单，确认后才执行（-y 跳过确认）。
第一行删包和模块（按名字），第二行删安装包（按版本或单个归档文件名）。

删除单位:
  pypi 包名            整个包目录（全部版本的文件）
  npm 包名             整个包目录（带 @scope/ 前缀的要写全）
  gomod 模块路径       整个模块（@v 下全部版本的三个文件）
  python 版本/文件名   按版本删全部平台安装包（3.12.15 精确；3.12 匹配 3.12.*）；
                       按文件名删单个归档及伴随 .sha256
  node 版本/文件名     按版本删目录（v22.14.0 精确；v22 匹配 v22.*）；
                       按文件名删单个归档（SHASUMS256.txt 在该版本归档
                       删光时才一并删除，官方文件不改写）
  golang 版本/文件名   按版本删全部平台归档（1.23.4 精确；1.23 匹配 go1.23.*）；
                       按文件名删单个归档及伴随 .sha256

  --deps               连带删除：pypi/npm/gomod 删掉目标后，只被它（直接或
                       间接）依赖、镜像里没有别人在用的包一并删除（依赖关系
                       从包内元数据实时推导）；安装包仅在按文件名删除时有效
                       （扩展为整个版本组），按版本删除本身已是整组，会报错
  -y, --yes            跳过确认直接删（写脚本用）

  -s, --server URL     服务端地址，如 http://user:pass@host:port，
                       账号密码可以直接写在地址里
  --user NAME          Basic auth 用户名（地址里已带账号密码时可省略）
  --pass PASS          Basic auth 密码

示例:
  pkgmirror delete pypi requests -s http://user:pass@host:19999
  pkgmirror delete npm lodash --deps -s http://host:19999 --user user --pass pass
  pkgmirror delete gomod github.com/spf13/cobra --deps -s http://host:19999
  pkgmirror delete python 3.12 -y -s http://host:19999
  pkgmirror delete node node-v22.14.0-linux-x64.tar.xz --deps -s http://host:19999
`)
}

// runServer 启动服务端子命令。
//
//	@param args []string 子命令参数（已去掉子命令名）。
//	@return error 配置加载或服务运行失败的原因。
func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.Usage = usageServer
	configPath := fs.StringP("config", "c", "", "服务端配置文件路径，默认探测当前目录与二进制目录下的 server.toml")
	fs.Parse(args)
	if *configPath == "" {
		*configPath = paths.FindConfig("server.toml")
	}
	if _, err := os.Stat(*configPath); err != nil {
		log.Printf("提示: 未找到配置文件 %s，使用默认配置（监听 :19999，数据目录为二进制目录下的 data/，未启用认证）", *configPath)
	}
	cfg, err := server.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	return server.New(cfg).ListenAndServe()
}

// runDownload 按镜像类型分发到对应下载命令。
//
//	@param args []string 子命令参数，首项为 python、pypi、node、npm、golang 或 gomod。
//	@return error 选项解析或下载失败的原因。
func runDownload(args []string) error {
	if len(args) == 0 {
		usageDownload()
		return fmt.Errorf("请指定下载类型：python、pypi、npm、node、golang 或 gomod")
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
	case "npm":
		return runDownloadNpm(args[1:])
	case "node":
		return runDownloadNode(args[1:])
	case "golang":
		return runDownloadGolang(args[1:])
	case "gomod":
		return runDownloadGomod(args[1:])
	default:
		return fmt.Errorf("未知的下载类型 %q（应为 python、pypi、npm、node、golang 或 gomod）", args[0])
	}
}

// dlFlags 汇总 download 各子命令共享的选项定义。
type dlFlags struct {
	fs           *flag.FlagSet
	platforms    *string
	archs        *string
	indexURL     *string
	registry     *string
	distURL      *string
	goproxy      *string
	githubPrefix *string
	proxy        *string
	configFile   *string
	outDir       *string
}

// newDownloadFlags 注册各子命令共有的选项。
//
//	@param name string 子命令名（python、pypi 或 npm）。
//	@return dlFlags 已注册公共选项的选项集。
func newDownloadFlags(name string) dlFlags {
	fs := flag.NewFlagSet("download "+name, flag.ExitOnError)
	// 该层 -h 只展示本层用法；由入口再指定各资源类型的专属帮助；
	// StringP 绑定单字母短参数（与长参数同一变量），单字母的 -o 也
	// 须以 shorthand 注册才会被 pflag 解析
	return dlFlags{
		fs:           fs,
		platforms:    fs.StringP("platform", "p", "", "按操作系统过滤（linux/windows/darwin），逗号分隔，默认全部"),
		archs:        fs.String("arch", "", "按架构过滤（x86_64/aarch64，别名 amd64/arm64），逗号分隔，默认全部"),
		indexURL:     fs.StringP("index-url", "i", "", "PyPI 索引地址"),
		registry:     fs.StringP("registry", "R", "", "npm registry 地址（download npm 使用）"),
		distURL:      fs.String("dist-url", "", "发行归档镜像源根地址（node / golang 子命令使用）"),
		goproxy:      fs.String("goproxy", "", "上游 Go module proxy 地址（download gomod 使用）"),
		githubPrefix: fs.StringP("github-prefix", "g", "", "GitHub 反代前缀"),
		proxy:        fs.StringP("proxy", "x", "", "HTTP 代理地址，如 http://127.0.0.1:7890"),
		configFile:   fs.StringP("config", "c", paths.FindConfig("client.toml"), "客户端配置文件路径，不存在则跳过"),
		outDir:       fs.StringP("o", "o", "", "下载产出目录，默认固定 downloads/"),
	}
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
		Registry:     client.ResolveRegistry(*f.registry, cfg.Registry),
		DistURL:      client.ResolveNodeDistURL(*f.distURL, cfg.NodeDistURL),
		GoDistURL:    client.ResolveGolangDistURL(*f.distURL, cfg.GolangDistURL),
		GoProxyURL:   client.ResolveGoProxy(*f.goproxy, cfg.GoProxyURL),
		GitHubPrefix: firstNonEmpty(*f.githubPrefix, cfg.GitHubPrefix, os.Getenv("GITHUB_PREFIX")),
		Proxy:        firstNonEmpty(*f.proxy, cfg.Proxy),
		OutDir:       *f.outDir,
		Version:      Version,
	}, nil
}

// runDownloadNpm 执行 download npm 子命令（位置参数为 包名[@range] 列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadNpm(args []string) error {
	f := newDownloadFlags("npm")
	reqFile := f.fs.StringP("r", "r", "", "package.json 或 package-lock.json 路径（lock 免 npm，与命令行包名互斥）")
	nodes := f.fs.String("node", "", "目标 Node.js 版本，逗号分隔（如 18 或 18.20.1），校验包的 engines.node")
	f.fs.Usage = usageDownloadNpm
	f.fs.Parse(args)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadNpmOptions{DownloadCommon: common, LockFile: *reqFile, Packages: f.fs.Args()}
	for _, v := range splitList(*nodes) {
		nv, err := client.ParseNodeVersion(v)
		if err != nil {
			return err
		}
		opts.Nodes = append(opts.Nodes, nv)
	}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PkgMirrorUAVersion = Version
	nodeDesc := "不限"
	if len(opts.Nodes) > 0 {
		nodeDesc = strings.Join(opts.Nodes, ",")
	}
	log.Printf("npm registry: %s，目标 node: %s，代理: %s", redact.URL(opts.Registry), nodeDesc, proxyDesc(opts.Proxy))
	return client.RunDownloadNpm(opts)
}

// runDownloadNode 执行 download node 子命令（位置参数为版本列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadNode(args []string) error {
	f := newDownloadFlags("node")
	f.fs.Usage = usageDownloadNode
	lts := f.fs.Bool("lts", false, "追加下载当前最新 LTS 版本")
	f.fs.Parse(args)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadNodeOptions{DownloadCommon: common, LTS: *lts}
	for _, v := range f.fs.Args() {
		for _, item := range splitList(v) {
			spec, err := node.ParseSpec(item)
			if err != nil {
				return err
			}
			opts.Versions = append(opts.Versions, spec)
		}
	}
	// 显式版本与最新 LTS 是两种下载目标，命令行要求二选一。
	if len(opts.Versions) > 0 && *lts {
		return fmt.Errorf("--lts 与版本参数互斥：要最新 LTS 单独用 --lts，要指定版本只写版本")
	}
	if len(opts.Versions) == 0 && !*lts {
		return fmt.Errorf("请指定 Node 版本（如 22 或 22.11.0）或 --lts")
	}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PkgMirrorUAVersion = Version
	log.Printf("Node dist 源: %s，目标: %s%s，代理: %s", redact.URL(opts.DistURL),
		strings.Join(specStrings(opts.Versions), ","),
		map[bool]string{true: " +lts", false: ""}[*lts], proxyDesc(opts.Proxy))
	return client.RunDownloadNode(opts)
}

// specStrings 把版本目标列表转为描述串（日志用）。
func specStrings(specs []node.Spec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.String()
	}
	return out
}

// runDownloadGolang 执行 download golang 子命令（位置参数为版本列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadGolang(args []string) error {
	f := newDownloadFlags("golang")
	f.fs.Usage = usageDownloadGolang
	src := f.fs.Bool("src", false, "每个目标版本追加源码归档（goX.Y.Z.src.tar.gz）")
	allPlat := f.fs.Bool("all-platforms", false, "平台白名单放开为全部官方平台/架构产物")
	f.fs.Parse(args)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadGolangOptions{DownloadCommon: common, Src: *src, AllPlatforms: *allPlat}
	// 版本作为位置参数：每个参数支持逗号分隔多个版本
	for _, v := range f.fs.Args() {
		for _, item := range splitList(v) {
			spec, err := golang.ParseSpec(item)
			if err != nil {
				return err
			}
			opts.Versions = append(opts.Versions, spec)
		}
	}
	if len(opts.Versions) == 0 {
		return fmt.Errorf("请指定 Go 版本（如 1.23 或 1.23.4）")
	}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PkgMirrorUAVersion = Version
	var specDesc []string
	for _, spec := range opts.Versions {
		specDesc = append(specDesc, spec.String())
	}
	log.Printf("Go 归档源: %s，目标: %s，代理: %s", redact.URL(opts.GoDistURL),
		strings.Join(specDesc, ","), proxyDesc(opts.Proxy))
	return client.RunDownloadGolang(opts)
}

// runDownloadGomod 执行 download gomod 子命令（位置参数为 模块@版本 列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadGomod(args []string) error {
	f := newDownloadFlags("gomod")
	f.fs.Usage = usageDownloadGomod
	reqFile := f.fs.StringP("r", "r", "", "go.sum 文件路径（模块闭包清单）")
	f.fs.Parse(args)
	common, err := f.resolveCommon()
	if err != nil {
		return err
	}
	opts := client.DownloadGomodOptions{DownloadCommon: common, GoSumFile: *reqFile, Modules: f.fs.Args()}
	if opts.OutDir == "" {
		opts.OutDir = "downloads"
	}
	client.PkgMirrorUAVersion = Version
	log.Printf("module proxy 源: %s，go.sum: %s，命令行模块: %d 个，代理: %s",
		redact.URL(opts.GoProxyURL), reqDesc(*reqFile), len(opts.Modules), proxyDesc(opts.Proxy))
	return client.RunDownloadGomod(opts)
}

// reqDesc 把 -r 文件路径转为日志描述（空为"未指定"）。
//
//	@param path string -r 参数值。
//	@return string 日志描述。
func reqDesc(path string) string {
	if path == "" {
		return "未指定"
	}
	return path
}

// runDownloadPython 执行 download python 子命令（位置参数为版本列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadPython(args []string) error {
	f := newDownloadFlags("python")
	f.fs.Usage = usageDownloadPython
	all := f.fs.Bool("all", false, "对未指定补丁号的版本下载全部历史补丁")
	f.fs.Parse(args)
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
	client.PkgMirrorUAVersion = Version
	log.Printf("Python 安装包下载，代理: %s", proxyDesc(opts.Proxy))
	return client.RunDownloadPython(opts)
}

// runDownloadPypi 执行 download pypi 子命令（位置参数为 包名==版本 列表）。
//
//	@param args []string 子命令参数（已去掉资源类型）。
//	@return error 选项解析或下载失败的原因。
func runDownloadPypi(args []string) error {
	f := newDownloadFlags("pypi")
	reqFile := f.fs.StringP("r", "r", "", "需求文件路径（每行一个 包名==版本）")
	f.fs.Usage = usageDownloadPypi
	pythons := f.fs.StringP("python", "P", "", "目标 Python 版本，逗号分隔，必填（依赖解析与 wheel 过滤）")
	f.fs.Parse(args)
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
	client.PkgMirrorUAVersion = Version
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
	serverURL := fs.StringP("server", "s", "", "服务端地址")
	user := fs.String("user", "", "Basic auth 用户名")
	pass := fs.String("pass", "", "Basic auth 密码")
	fs.Parse(args)
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

// runDelete 执行删除子命令：解析类型与目标后交由客户端执行。
//
//	@param args []string 子命令参数（首项为镜像类型）。
//	@return error 选项解析或删除失败的原因。
func runDelete(args []string) error {
	if len(args) == 0 {
		usageDelete()
		return fmt.Errorf("请指定删除类型：pypi、npm、gomod、python、node 或 golang")
	}
	typ := args[0]
	if typ == "-h" || typ == "--help" || typ == "help" {
		usageDelete()
		return nil
	}
	switch typ {
	case "pypi", "npm", "gomod", "python", "node", "golang":
	default:
		return fmt.Errorf("未知的删除类型 %q（应为 pypi、npm、gomod、python、node 或 golang）", typ)
	}
	fs := flag.NewFlagSet("delete "+typ, flag.ExitOnError)
	fs.Usage = usageDelete
	serverURL := fs.StringP("server", "s", "", "服务端地址，如 http://user:pass@host:port")
	user := fs.String("user", "", "Basic auth 用户名")
	pass := fs.String("pass", "", "Basic auth 密码")
	deps := fs.Bool("deps", false, "级联删除依赖项（仅 pypi/npm/gomod）")
	yes := fs.BoolP("yes", "y", false, "跳过确认直接删除")
	fs.Parse(args[1:])
	if fs.NArg() != 1 {
		return fmt.Errorf("请指定一个删除目标（包名 / 模块路径 / 版本）；或加 -h 查看完整说明")
	}
	if *serverURL == "" {
		return fmt.Errorf("必须用 --server 指定服务端地址，如 http://user:pass@host:port")
	}
	// 服务端校验 --deps：安装包按文件名删除时可用，按版本删除时拒绝。
	return client.RunDelete(client.DeleteOptions{
		Type:   typ,
		Name:   fs.Arg(0),
		Deps:   *deps,
		Yes:    *yes,
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
