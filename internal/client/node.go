package client

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"pkgmirror/internal/node"
)

// DownloadNodeOptions 是 download node 子命令的输入。
type DownloadNodeOptions struct {
	DownloadCommon
	// Versions 是命令行位置参数解析出的目标版本列表，与 LTS 至少给一个。
	Versions []node.Spec
	// LTS 为 true 时追加当前最新 LTS 版本（可与 Versions 并存）。
	LTS bool
}

// ResolveNodeDistURL 按三级来源合成 dist 根地址：
// 命令行 --dist-url > client.toml node-dist-url > NODE_DIST_URL 环境变量 > 官方源。
//
// 国内网络可配 npmmirror（https://cdn.npmmirror.com/binaries/node），与官方 dist 同布局。
//
//	@param explicit string 命令行参数值，可为空。
//	@param fromConfig string 配置文件值，可为空。
//	@return string 实际使用的 dist 根地址。
func ResolveNodeDistURL(explicit, fromConfig string) string {
	if explicit != "" {
		return explicit
	}
	if fromConfig != "" {
		return fromConfig
	}
	if v := os.Getenv("NODE_DIST_URL"); v != "" {
		return v
	}
	return node.DefaultDistURL
}

// RunDownloadNode 执行 download node：拉取 dist index.json 选版本，逐版本
// 下载 SHASUMS256.txt 与各平台可移植归档（归档按清单哈希校验）。任一文件
// 下载失败即中止，已下载的文件保留，便于重跑续传。
//
//	@param opts DownloadNodeOptions 已解析的选项。
//	@return error 任何环节失败的原因。
func RunDownloadNode(opts DownloadNodeOptions) error {
	if len(opts.Versions) == 0 && !opts.LTS {
		return fmt.Errorf("请指定 Node 版本（如 22 或 22.11.0）或 --lts（当前最新 LTS）")
	}
	ctx := context.Background()
	client, err := newHTTPClient(opts.Proxy, httpTimeout)
	if err != nil {
		return err
	}
	dist := strings.TrimSuffix(opts.DistURL, "/")
	if dist == "" {
		dist = node.DefaultDistURL
	}
	mf := newManifest(opts.Version)

	idx, err := fetchNodeIndex(ctx, client, dist)
	if err != nil {
		return err
	}
	files, err := node.Select(idx, opts.Versions, opts.LTS, opts.Platforms, opts.Archs)
	if err != nil {
		return err
	}
	log.Printf("筛选得到 %d 个 Node 归档文件（%d 个版本）", len(files), len(uniqVersions(files)))

	// 按版本分组：先取该版本 SHASUMS256.txt（归档校验哈希的来源），再逐归档下载
	byVer := map[string][]node.File{}
	for _, f := range files {
		byVer[f.Version] = append(byVer[f.Version], f)
	}
	vers := uniqVersions(files)
	total := len(files) + len(vers)
	done := 0
	for _, v := range vers {
		done++
		sumDest := filepath.Join(opts.OutDir, "node", v, node.ShasumsName)
		log.Printf("[%d/%d] %s/%s", done, total, v, node.ShasumsName)
		if err := fetchAndRecord(ctx, client, dist+"/"+v+"/"+node.ShasumsName, sumDest, "", opts.OutDir, &mf); err != nil {
			return err
		}
		sums, err := readLocalShasums(sumDest)
		if err != nil {
			return err
		}
		for _, f := range byVer[v] {
			done++
			sha := sums[f.Filename]
			if sha == "" {
				return fmt.Errorf("%s 的 %s 中没有 %s 的哈希行", v, node.ShasumsName, f.Filename)
			}
			log.Printf("[%d/%d] %s", done, total, f.Filename)
			dest := filepath.Join(opts.OutDir, "node", v, f.Filename)
			if err := fetchAndRecord(ctx, client, dist+"/"+v+"/"+f.Filename, dest, sha, opts.OutDir, &mf); err != nil {
				return err
			}
		}
	}
	return saveManifest(opts.OutDir, mf)
}

// fetchNodeIndex 拉取并解析 dist 的 index.json 发布索引。
//
//	@param ctx context.Context 上下文。
//	@param client *http.Client HTTP 客户端。
//	@param dist string dist 根地址。
//	@return []node.Release 发布列表。
//	@return error 下载或解析失败的原因。
func fetchNodeIndex(ctx context.Context, client *http.Client, dist string) ([]node.Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dist+"/index.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 Node dist 索引失败（可用 --dist-url 指定镜像源）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 Node dist 索引返回 %s", resp.Status)
	}
	return node.ParseIndex(resp.Body)
}

// readLocalShasums 读取已落盘的 SHASUMS256.txt。
//
//	@param path string 文件路径。
//	@return map[string]string 文件名到 sha256 的映射。
//	@return error 文件读取或解析失败时的错误。
func readLocalShasums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", node.ShasumsName, err)
	}
	defer f.Close()
	return node.ParseShasums(f)
}

// uniqVersions 提取文件列表中的版本序（保持原序，版本降序输入即降序输出）。
func uniqVersions(files []node.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if !seen[f.Version] {
			seen[f.Version] = true
			out = append(out, f.Version)
		}
	}
	return out
}
