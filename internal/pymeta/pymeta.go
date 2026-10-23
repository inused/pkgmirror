// Package pymeta 解析 uv 仓库的 download-metadata.json，
// 并按目标 Python 版本与平台筛选 CPython 安装包条目。
//
// 元数据包含各版本、平台的文件名、下载地址与 sha256。
// uv 通过 UV_PYTHON_INSTALL_MIRROR 按其中的文件名请求安装包。
package pymeta

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// MetaURL 是元数据文件的原始地址；经 GitHubPrefix 拼前缀后可走反代。
const MetaURL = "https://raw.githubusercontent.com/astral-sh/uv/main/crates/uv-python-managed/download-metadata.json"

// archInfo 是条目中的 CPU 架构信息。Family 是架构族（x86_64/aarch64 等），
// Variant 是微架构级别（v2/v3/v4），表示同一架构的不同 CPU 指令集档位，
// 与构建变体（Entry.Variant）无关，不参与过滤。
type archInfo struct {
	Family  string  `json:"family"`
	Variant *string `json:"variant"`
}

// Entry 是一个平台的 CPython/PyPy 安装包条目。
// Variant 非 nil 表示 freethreaded/debug 等构建变体，默认下载时排除。
type Entry struct {
	Name       string   `json:"name"`
	Arch       archInfo `json:"arch"`
	Os         string   `json:"os"`
	Libc       string   `json:"libc"`
	Major      int      `json:"major"`
	Minor      int      `json:"minor"`
	Patch      int      `json:"patch"`
	Prerelease string   `json:"prerelease"`
	URL        string   `json:"url"`
	SHA256     string   `json:"sha256"`
	Variant    *string  `json:"variant"`
	Build      string   `json:"build"`
}

// Version 返回标准三段版本号字符串，如 "3.12.7"。
func (e Entry) Version() string {
	return fmt.Sprintf("%d.%d.%d", e.Major, e.Minor, e.Patch)
}

// Metadata 是全部下载条目，key 形如 "cpython-3.12.7-windows-x86_64-msvc"。
type Metadata map[string]Entry

// ParseMetadata 解析元数据 JSON。
//
//	@param r io.Reader 元数据文件内容。
//	@return Metadata 解析结果。
//	@return error JSON 解码失败时的错误；缺失字段取对应类型的零值。
func ParseMetadata(r io.Reader) (Metadata, error) {
	var m Metadata
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("解析 uv 下载元数据失败: %w", err)
	}
	return m, nil
}

// Spec 是一个目标 Python 版本。Patch 为 0 表示取该 minor 的最新 patch，
// 大于 0 表示精确匹配该 patch。
type Spec struct {
	Major int
	Minor int
	Patch int
}

// String 返回 "3.12" 或 "3.12.4" 形式的描述。
func (s Spec) String() string {
	if s.Patch == 0 {
		return fmt.Sprintf("%d.%d", s.Major, s.Minor)
	}
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

// archAliases 把常见架构写法归一到元数据 Arch.Family 用的名称。
var archAliases = map[string]string{
	"amd64": "x86_64",
	"arm64": "aarch64",
}

// Select 按版本、平台与架构筛选要下载的 CPython 标准构建条目。
//
// 过滤条件：Name 为 cpython、非 prerelease、无构建变体（排除 freethreaded/debug）；
// platforms 非空时按 Os 字段匹配（linux/windows/darwin），archs 非空时按
// Arch.Family 匹配（x86_64/aarch64/i686 等，amd64/arm64 为别名）。
// Spec.Patch 为 0 时取该 minor 可用的最大 patch（AllPatches 为 true 时取全部 patch），
// 大于 0 时精确匹配；匹配不到任何条目时报错并列出该 minor 的可用 patch。
//
//	@param specs []Spec 目标版本列表。
//	@param platforms []string 平台过滤列表（os 名）；空表示全平台。
//	@param archs []string 架构过滤列表；空表示全架构。
//	@param allPatches bool Spec.Patch 为 0 时是否下载该 minor 全部历史 patch。
//	@return []Entry 筛选结果，按版本降序、文件名升序。
//	@return error 任意一个 Spec 匹配不到条目时的错误。
func (m Metadata) Select(specs []Spec, platforms, archs []string, allPatches bool) ([]Entry, error) {
	platformSet := map[string]bool{}
	for _, p := range platforms {
		platformSet[p] = true
	}
	archSet := map[string]bool{}
	for _, a := range archs {
		if canonical, ok := archAliases[strings.ToLower(a)]; ok {
			archSet[canonical] = true
		} else {
			archSet[strings.ToLower(a)] = true
		}
	}
	var out []Entry
	for _, spec := range specs {
		// cands 收集该 minor 全部 patch 的候选条目，patches 记录出现过哪些 patch
		cands := map[int][]Entry{}
		for _, e := range m {
			if e.Name != "cpython" || e.Major != spec.Major || e.Minor != spec.Minor {
				continue
			}
			if e.Prerelease != "" || e.Variant != nil {
				continue
			}
			if len(platformSet) > 0 && !platformSet[e.Os] {
				continue
			}
			if len(archSet) > 0 && !archSet[e.Arch.Family] {
				continue
			}
			cands[e.Patch] = append(cands[e.Patch], e)
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("元数据中没有 %s 的 CPython 标准构建（平台过滤 %v）", spec, platforms)
		}
		var patches []int
		for p := range cands {
			patches = append(patches, p)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(patches)))
		want := []int{}
		switch {
		case spec.Patch > 0:
			if _, ok := cands[spec.Patch]; !ok {
				return nil, fmt.Errorf("元数据中没有 %s 的 CPython 构建可用 patch: %v", spec, patches)
			}
			want = append(want, spec.Patch)
		case allPatches:
			want = patches
		default:
			want = append(want, patches[0]) // 已降序，取最新
		}
		for _, p := range want {
			out = append(out, cands[p]...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Major != out[j].Major {
			return out[i].Major > out[j].Major
		}
		if out[i].Minor != out[j].Minor {
			return out[i].Minor > out[j].Minor
		}
		if out[i].Patch != out[j].Patch {
			return out[i].Patch > out[j].Patch
		}
		return out[i].URL < out[j].URL
	})
	return out, nil
}
