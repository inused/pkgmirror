package client

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"pkgmirror/internal/golang"
)

// ResolveGolangDistURL 按三级来源合成 Go 发行归档根地址：
// 命令行 --dist-url > client.toml golang-dist-url > GOLANG_DIST_URL 环境变量 > 官方源。
//
// 国内网络可配 mirrors.aliyun.com/golang，与官方 flat 同布局。
//
//	@param explicit string 命令行参数值，可为空。
//	@param fromConfig string 配置文件值，可为空。
//	@return string 实际使用的归档根地址。
func ResolveGolangDistURL(explicit, fromConfig string) string {
	if explicit != "" {
		return explicit
	}
	if fromConfig != "" {
		return fromConfig
	}
	if v := os.Getenv("GOLANG_DIST_URL"); v != "" {
		return v
	}
	return golang.DefaultDistURL
}

// DownloadGolangOptions 是 download golang 子命令的输入。
type DownloadGolangOptions struct {
	DownloadCommon
	// Versions 是命令行位置参数解析出的目标版本列表，必填。
	Versions []golang.Spec
	// Src 为 true 时每个目标版本追加源码归档（goX.Y.Z.src.tar.gz）。
	Src bool
	// AllPlatforms 为 true 时平台白名单放开为全部官方平台/架构产物。
	AllPlatforms bool
}

// RunDownloadGolang 执行 download golang：拉取官方发布清单选版本，逐文件
// 下载发行归档（按清单内嵌 sha256 校验）。任一文件下载失败即中止，已下载
// 的文件保留，便于重跑续传。
//
//	@param opts DownloadGolangOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadGolang(opts DownloadGolangOptions) error {
	if len(opts.Versions) == 0 {
		return fmt.Errorf("请指定 Go 版本（如 1.23 或 1.23.4）")
	}
	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	dist := strings.TrimSuffix(opts.GoDistURL, "/")
	if dist == "" {
		dist = golang.DefaultDistURL
	}
	mf := newManifest(opts.Version)

	rls, err := fetchGolangIndex(ctx, client)
	if err != nil {
		return err
	}
	files, err := golang.Select(rls, opts.Versions, opts.Platforms, opts.Archs, opts.Src, opts.AllPlatforms)
	if err != nil {
		return err
	}
	log.Printf("筛选得到 %d 个 Go 发行归档（%d 个版本）", len(files), uniqGolangVersions(files))

	for i, f := range files {
		log.Printf("[%d/%d] %s", i+1, len(files), f.Filename)
		dest := filepath.Join(opts.OutDir, "golang", f.Filename)
		if err := fetchAndRecord(ctx, client, dist+"/"+f.Filename, dest, f.SHA256, opts.OutDir, &mf); err != nil {
			return err
		}
	}
	return saveManifest(opts.OutDir, mf)
}

// fetchGolangIndex 拉取并解析 go.dev/dl 的发布清单 JSON。
//
// 清单端点固定（见 golang.MetaURL），不随归档镜像源变化：第三方镜像一般
// 只镜像归档 flat 布局，不含清单 JSON；网络不畅时用 --proxy 兜底。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@return []golang.Release 发布清单。
//	@return error 下载或解析失败的原因。
func fetchGolangIndex(ctx context.Context, client *http.Client) ([]golang.Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, golang.MetaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 Go 发布清单失败（可用 --proxy 指定代理）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 Go 发布清单返回 %s", resp.Status)
	}
	return golang.ParseIndex(resp.Body)
}

// uniqGolangVersions 统计文件列表覆盖的版本数（日志用）。
func uniqGolangVersions(files []golang.File) int {
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Version] = true
	}
	return len(seen)
}
