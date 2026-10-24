// Package golang 解析 go.dev/dl 的发布清单（?mode=json&include=all），
// 按目标版本与平台筛选要下载的 Go 发行归档。
//
// 归档平铺在 dl.google.com/go 根下，sha256 取发布清单的 files 字段。
// mise 和 asdf-golang 可通过各自的镜像环境变量接入。
package golang

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultDistURL 是官方归档根地址，客户端未配置镜像源时的兜底值
// （国内可配 https://mirrors.aliyun.com/golang，flat 同布局）。
const DefaultDistURL = "https://dl.google.com/go"

// MetaURL 是包含历史版本的发布清单端点，供版本范围与精确版本查询使用。
const MetaURL = "https://go.dev/dl/?mode=json&include=all"

// Release 是发布清单中的一个发布条目。
type Release struct {
	// Version 是带 go 前缀的版本号（如 "go1.23.4"），与归档文件名前缀一致。
	Version string `json:"version"`
	// Stable 为 false 时是 rc 等预发布（如 "go1.27rc3"），Select 排除。
	Stable bool `json:"stable"`
	// Files 是该版本的平台产物列表。
	Files []File `json:"files"`
}

// File 是发布条目中的一个平台产物。
type File struct {
	// Filename 是归档文件名（如 "go1.23.4.linux-amd64.tar.gz"）。
	Filename string `json:"filename"`
	// Os 是产物目标系统（如 "linux"），与文件名 os 段一致；source 产物为空。
	Os string `json:"os"`
	// Arch 是产物目标架构（如 "amd64"），与文件名 arch 段一致；source 产物为空。
	Arch string `json:"arch"`
	// Version 是产物所属版本号（如 "go1.23.4"）。
	Version string `json:"version"`
	// SHA256 是官方给出的归档哈希（十六进制小写），下载校验用。
	SHA256 string `json:"sha256"`
	// Size 是归档字节数。
	Size int64 `json:"size"`
	// Kind 是产物类型：archive（可移植归档）、installer（msi/pkg）、source（源码）。
	Kind string `json:"kind"`
}

// ParseIndex 解析发布清单 JSON。
//
//	@param r io.Reader 清单 JSON 内容。
//	@return []Release 按原文顺序的发布列表（新到旧，Select 不依赖顺序）。
//	@return error JSON 解码失败时的错误；缺失字段取对应类型的零值。
func ParseIndex(r io.Reader) ([]Release, error) {
	var idx []Release
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, fmt.Errorf("解析 Go 发布清单失败: %w", err)
	}
	return idx, nil
}

// Spec 是一个目标 Go 版本。Minor/Patch 为 -1 表示不限（取该范围内最新），
// 否则精确匹配。Major 必填（>= 0）。
type Spec struct {
	Major int
	Minor int
	Patch int
}

// String 返回 "1.23" 或 "1.23.4" 形式的描述。
func (s Spec) String() string {
	if s.Minor < 0 {
		return strconv.Itoa(s.Major)
	}
	if s.Patch < 0 {
		return fmt.Sprintf("%d.%d", s.Major, s.Minor)
	}
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

// ParseSpec 解析 "1"（主版本内最新）、"1.23"（该 minor 内最新）或
// "1.23.4"（精确）形式的版本串。
//
//	@param s string 版本串。
//	@return Spec 解析结果，未指定段为 -1。
//	@return error 格式非法时的错误。
func ParseSpec(s string) (Spec, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return Spec{}, fmt.Errorf("Go 版本 %q 应形如 1.23 或 1.23.4", s)
	}
	nums := []int{-1, -1, -1}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Spec{}, fmt.Errorf("Go 版本 %q 含非法数字段 %q", s, p)
		}
		nums[i] = n
	}
	return Spec{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// versionRe 匹配纯数字三段版本（rc 等预发布不匹配，由 Stable 字段兜底排除）。
var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// parseVersion 解析 "go1.23.4" 为三段数字；go 前缀可有可无。
//
//	@param v string 版本串。
//	@return [3]int major/minor/patch。
//	@return bool 格式合法且无 rc 等附加段时为 true。
func parseVersion(v string) ([3]int, bool) {
	var nums [3]int
	m := versionRe.FindStringSubmatch(strings.TrimPrefix(v, "go"))
	if m == nil {
		return nums, false
	}
	for i := 0; i < 3; i++ {
		nums[i], _ = strconv.Atoi(m[i+1])
	}
	return nums, true
}

// CompareVersion 按三段 semver 比较 "go1.23.4" 版本串，返回 -1/0/1；
// 无法解析的串排在可解析串之后（服务端浏览页用它对版本降序排序）。
func CompareVersion(a, b string) int {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return 1
	case !okb:
		return -1
	}
	for i := 0; i < 3; i++ {
		if va[i] != vb[i] {
			if va[i] < vb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// platformAliases 把平台写法归一到官方 JSON 的 os 字段命名。
// macos 与 darwin 均映射为 darwin。
var platformAliases = map[string]string{
	"macos":  "darwin",
	"darwin": "darwin",
}

// archAliases 把架构写法归一到官方 JSON 的 arch 字段命名
// （官方用 amd64/arm64，与 x86_64/aarch64 互为别名）。
var archAliases = map[string]string{
	"x86_64":  "amd64",
	"aarch64": "arm64",
}

// DefaultPlatforms 和 DefaultArchs 是未显式过滤时采用的系统与架构列表。
var (
	DefaultPlatforms = []string{"linux", "darwin", "windows"}
	DefaultArchs     = []string{"amd64", "arm64"}
)

// Select 按版本、平台与架构筛选要下载的 Go 发行文件。
//
// 每个 Spec 解析为该范围内 semver 最大的 stable 发布（rc 等预发布排除），
// 匹配不到报错。kind 只取 archive（src 为 true 时追加每版本的 source 条目，
// source 不参与平台过滤）。平台过滤两维度独立：显式给出的维度按官方 JSON
// 的 os/arch 字段精确匹配（可指定 aix、mips 等任意官方平台）；未给出的
// 维度套主流白名单；allPlatforms 为 true 时白名单放开为全部。组合缺失
// （如 darwin 无 386 产物）安静跳过，整体结果为空时报错。
//
//	@param rls []Release 发布清单。
//	@param specs []Spec 目标版本列表。
//	@param platforms []string 平台过滤列表；空为白名单。
//	@param archs []string 架构过滤列表；空为白名单。
//	@param src bool 是否追加 kind=source 的源码归档。
//	@param allPlatforms bool 是否放开白名单取全部平台/架构。
//	@return []File 筛选结果，版本降序、文件名升序。
//	@return error 版本匹配不到或整体过滤结果为空时的错误。
func Select(rls []Release, specs []Spec, platforms, archs []string, src, allPlatforms bool) ([]File, error) {
	var osList, archList []string
	for _, p := range platforms {
		if canonical, ok := platformAliases[strings.ToLower(p)]; ok {
			osList = append(osList, canonical)
		} else {
			osList = append(osList, strings.ToLower(p))
		}
	}
	for _, a := range archs {
		if canonical, ok := archAliases[strings.ToLower(a)]; ok {
			archList = append(archList, canonical)
		} else {
			archList = append(archList, strings.ToLower(a))
		}
	}
	// 显式给出精确匹配；未给出时默认白名单，allPlatforms 放开为全部
	if len(osList) == 0 && !allPlatforms {
		osList = DefaultPlatforms
	}
	if len(archList) == 0 && !allPlatforms {
		archList = DefaultArchs
	}

	var versions []string
	seen := map[string]bool{}
	for _, spec := range specs {
		var best string
		for _, r := range rls {
			v, ok := parseVersion(r.Version)
			if !ok || !r.Stable {
				continue
			}
			if v[0] != spec.Major {
				continue
			}
			if spec.Minor >= 0 && v[1] != spec.Minor {
				continue
			}
			if spec.Patch >= 0 && v[2] != spec.Patch {
				continue
			}
			if best == "" || CompareVersion(r.Version, best) > 0 {
				best = r.Version
			}
		}
		if best == "" {
			return nil, fmt.Errorf("发布清单中没有 %s 的 stable 发布", spec)
		}
		if !seen[best] {
			versions = append(versions, best)
			seen[best] = true
		}
	}

	var out []File
	for _, v := range versions {
		rel := releaseOf(rls, v)
		for _, f := range rel.Files {
			if f.Kind == "source" {
				if src {
					out = append(out, f)
				}
				continue
			}
			if f.Kind != "archive" {
				continue // installer（msi/pkg）不在镜像范围
			}
			if len(osList) > 0 && !contains(osList, f.Os) {
				continue
			}
			if len(archList) > 0 && !contains(archList, f.Arch) {
				continue
			}
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("筛选结果为空：平台/架构组合在这些版本中没有 archive 产物")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareVersion(out[i].Version, out[j].Version); c != 0 {
			return c > 0
		}
		return out[i].Filename < out[j].Filename
	})
	return out, nil
}

// contains 报告列表是否包含该值。
func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// releaseOf 返回指定版本号的发布条目；Select 的 versions 均来自 rls，必然命中。
func releaseOf(rls []Release, version string) *Release {
	for i := range rls {
		if rls[i].Version == version {
			return &rls[i]
		}
	}
	return &Release{Version: version}
}

// VersionOf 从归档文件名提取版本号（"go1.23.4.linux-amd64.tar.gz" →
// "go1.23.4"），供服务端浏览页分组；无法识别时返回空串。
//
//	@param filename string 归档文件名。
//	@return string 带 go 前缀的版本号；无法解析时为空串。
func VersionOf(filename string) string {
	m := fileVerRe.FindStringSubmatch(filename)
	if m == nil {
		return ""
	}
	return "go" + m[1]
}

// fileVerRe 匹配官方归档文件名的版本前缀段（正式版三段数字；rc 版少一段
// 点段，如 go1.27rc3）。
var fileVerRe = regexp.MustCompile(`^go(\d+(?:\.\d+)*(?:rc\d+)?)\.`)
