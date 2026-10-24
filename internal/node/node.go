// Package node 解析 nodejs.org/dist 的 index.json 发布索引，
// 按目标版本与平台筛选要下载的 Node.js 发行文件。
//
// 版本管理器按官方 dist 布局下载文件：{dist}/vX.Y.Z/node-vX.Y.Z-
// {os}-{arch}.{ext} 寻址，校验清单为同目录的 SHASUMS256.txt。
package node

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultDistURL 是官方 dist 根地址，客户端未配置镜像源时的兜底值。
const DefaultDistURL = "https://nodejs.org/dist"

// ShasumsName 是每个版本目录下的官方 sha256 校验清单文件名。
const ShasumsName = "SHASUMS256.txt"

// Release 是 index.json 中的一个发布条目。
type Release struct {
	// Version 是带 v 前缀的版本号，与 dist 目录名一致（如 "v22.11.0"）。
	Version string `json:"version"`
	// Date 是发布日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Files 是该版本发布的平台产物标识列表（如 "linux-x64"、
	// "osx-arm64-tar"、"win-x64-zip"、"src"）。标识的 os 段用 osx/win
	// 命名，与实际文件名 os 段（darwin/win）不同，见 Select。
	Files []string `json:"files"`
	// Npm 是捆绑的 npm 版本。
	Npm string `json:"npm"`
	// LTS 为 false（非 LTS）或 LTS 代号字符串（如 "Jod"）。
	LTS any `json:"lts"`
}

// IsLTS 报告该发布是否为 LTS（lts 字段为代号字符串时成立）。
func (r Release) IsLTS() bool {
	_, ok := r.LTS.(string)
	return ok
}

// ParseIndex 解析 index.json 发布索引。
//
//	@param r io.Reader index.json 内容。
//	@return []Release 按原文顺序的发布列表（新到旧，Select 不依赖顺序）。
//	@return error JSON 解码失败时的错误；缺失字段取对应类型的零值。
func ParseIndex(r io.Reader) ([]Release, error) {
	var idx []Release
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, fmt.Errorf("解析 Node dist 索引失败: %w", err)
	}
	return idx, nil
}

// Spec 是一个目标 Node 版本。Minor/Patch 为 -1 表示不限（取该范围内最新），
// 否则精确匹配。Major 必填（>= 0）。
type Spec struct {
	Major int
	Minor int
	Patch int
}

// String 返回 "22"、"22.11" 或 "22.11.0" 形式的描述。
func (s Spec) String() string {
	if s.Minor < 0 {
		return strconv.Itoa(s.Major)
	}
	if s.Patch < 0 {
		return fmt.Sprintf("%d.%d", s.Major, s.Minor)
	}
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

// ParseSpec 解析 "22"（主版本内最新）、"22.11"（该 minor 内最新）或
// "22.11.0"（精确）形式的版本串。
//
//	@param s string 版本串。
//	@return Spec 解析结果，未指定段为 -1。
//	@return error 格式非法时的错误。
func ParseSpec(s string) (Spec, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return Spec{}, fmt.Errorf("node 版本 %q 应形如 22、22.11 或 22.11.0", s)
	}
	nums := []int{-1, -1, -1}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Spec{}, fmt.Errorf("node 版本 %q 含非法数字段 %q", s, p)
		}
		nums[i] = n
	}
	return Spec{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// versionRe 匹配纯数字三段版本（index.json 均为正式发布，异常条目跳过）。
var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// parseVersion 解析 "vX.Y.Z" 为三段数字；v 前缀可有可无。
//
//	@param v string 版本串。
//	@return [3]int major/minor/patch。
//	@return bool 格式合法且无预发布等附加段时为 true。
func parseVersion(v string) ([3]int, bool) {
	var nums [3]int
	m := versionRe.FindStringSubmatch(strings.TrimPrefix(v, "v"))
	if m == nil {
		return nums, false
	}
	for i := 0; i < 3; i++ {
		nums[i], _ = strconv.Atoi(m[i+1])
	}
	return nums, true
}

// CompareVersion 按三段 semver 比较 "vX.Y.Z" 版本串，返回 -1/0/1；
// 无法解析的串排在可解析串之后（服务端浏览页用它对版本目录降序排序）。
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

// File 是一个要下载的 Node 发行文件。
type File struct {
	// Version 是带 v 前缀的版本目录名（如 "v22.11.0"）。
	Version string
	// Filename 是 dist 文件名（如 "node-v22.11.0-linux-x64.tar.xz"）。
	Filename string
}

// platformAliases 把常见平台写法归一到内部 os 名（linux/macos/windows）。
// macos 与 darwin 均映射为 darwin。
var platformAliases = map[string]string{
	"darwin": "macos",
	"macos":  "macos",
}

// archAliases 把常见架构写法归一到内部架构名（x86_64/aarch64）。
var archAliases = map[string]string{
	"amd64": "x86_64",
	"arm64": "aarch64",
}

// osInfo 描述一个 os 的 files 标识与文件名规则。
type osInfo struct {
	// idSuffix 是 files 标识在架构段后的固定后缀：macOS 的 tar 归档带
	// -tar（与 -pkg 安装器区分），Windows 的 zip 带 -zip（与 exe/msi/7z
	// 区分），Linux 只有裸 tar.xz 一种归档、无后缀。
	idSuffix string
	// fileOS 是实际文件名的 os 段：标识用 osx/win，文件名用 darwin/win。
	fileOS string
	// ext 是可移植归档扩展名。
	ext string
}

// osInfos 是三个平台的标识与文件名映射。
var osInfos = map[string]osInfo{
	"linux":   {idSuffix: "", fileOS: "linux", ext: ".tar.xz"},
	"macos":   {idSuffix: "-tar", fileOS: "darwin", ext: ".tar.xz"},
	"windows": {idSuffix: "-zip", fileOS: "win", ext: ".zip"},
}

// osIDPrefix 是 files 标识的 os 段前缀。
var osIDPrefix = map[string]string{
	"linux":   "linux-",
	"macos":   "osx-",
	"windows": "win-",
}

// archTokens 是内部架构名到 dist 标识/文件名架构段的映射。
var archTokens = map[string]string{
	"x86_64":  "x64",
	"aarch64": "arm64",
}

// fileID 返回 platform/arch 在 index.json files 数组中的标识
// （linux-x64、osx-arm64-tar、win-x64-zip）。
func fileID(platform, arch string) (string, bool) {
	prefix, ok := osIDPrefix[platform]
	if !ok {
		return "", false
	}
	token, ok := archTokens[arch]
	if !ok {
		return "", false
	}
	return prefix + token + osInfos[platform].idSuffix, true
}

// fileName 返回 platform/arch 的实际下载文件名
// （node-v22.11.0-darwin-x64.tar.xz、node-v22.11.0-win-x64.zip 等）。
func fileName(version, platform, arch string) (string, bool) {
	osi, ok := osInfos[platform]
	if !ok {
		return "", false
	}
	token, ok := archTokens[arch]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("node-v%s-%s-%s%s", strings.TrimPrefix(version, "v"), osi.fileOS, token, osi.ext), true
}

// Select 按版本、平台与架构筛选要下载的 Node 发行文件。
//
// 每个 Spec 解析为该范围内 semver 最大的发布：Minor/Patch 未指定时取该
// 范围最新（"22" 取 22 线最新，"22.11" 取 22.11 线最新 patch），指定时
// 精确匹配，匹配不到报错。lts 为 true 时追加当前最新 LTS 发布。
// platforms/archs 非空时按内部名过滤（darwin/macos、amd64/arm64 为别名），
// 空为全平台全架构。目标只取可移植归档（tar.xz/zip），按整段 files 标识
// 精确匹配，musl、安装器、src 等变体自然排除；标识缺失时报错。
//
//	@param rls []Release index.json 的发布列表。
//	@param specs []Spec 目标版本列表。
//	@param lts bool 是否追加当前最新 LTS。
//	@param platforms []string 平台过滤列表（linux/macos/windows）；空为全部。
//	@param archs []string 架构过滤列表（x86_64/aarch64）；空为全部。
//	@return []File 筛选结果，版本降序、文件名升序。
//	@return error 版本或平台产物匹配不到时的错误。
func Select(rls []Release, specs []Spec, lts bool, platforms, archs []string) ([]File, error) {
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
	if len(osList) == 0 {
		osList = []string{"linux", "macos", "windows"}
	}
	if len(archList) == 0 {
		archList = []string{"x86_64", "aarch64"}
	}

	var versions []string
	seen := map[string]bool{}
	for _, spec := range specs {
		// cands 收集该 Spec 范围内的候选版本，降序排列后取首个
		var cands []string
		for _, r := range rls {
			v, ok := parseVersion(r.Version)
			if !ok || v[0] != spec.Major {
				continue
			}
			if spec.Minor >= 0 && v[1] != spec.Minor {
				continue
			}
			if spec.Patch >= 0 && v[2] != spec.Patch {
				continue
			}
			cands = append(cands, r.Version)
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("dist 索引中没有 %s 的发布", spec)
		}
		sort.Slice(cands, func(i, j int) bool { return CompareVersion(cands[i], cands[j]) > 0 })
		pick := cands[0]
		if spec.Minor >= 0 && spec.Patch < 0 {
			// 已按 semver 降序，首个与 pick 同 minor 的即该 minor 最新 patch
			top, _ := parseVersion(pick)
			for _, c := range cands {
				if cv, _ := parseVersion(c); cv[1] == top[1] {
					pick = c
					break
				}
			}
		}
		if !seen[pick] {
			versions = append(versions, pick)
			seen[pick] = true
		}
	}
	if lts {
		var best string
		for _, r := range rls {
			if !r.IsLTS() {
				continue
			}
			if best == "" || CompareVersion(r.Version, best) > 0 {
				best = r.Version
			}
		}
		if best == "" {
			return nil, fmt.Errorf("dist 索引中没有 LTS 发布")
		}
		if !seen[best] {
			versions = append(versions, best)
			seen[best] = true
		}
	}

	var out []File
	for _, v := range versions {
		rel := releaseOf(rls, v)
		for _, os := range osList {
			for _, arch := range archList {
				id, _ := fileID(os, arch)
				found := false
				for _, f := range rel.Files {
					if f == id {
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("%s 没有可移植归档标识 %s（该版本产物: %v）", v, id, rel.Files)
				}
				name, _ := fileName(v, os, arch)
				out = append(out, File{Version: v, Filename: name})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := CompareVersion(out[i].Version, out[j].Version); c != 0 {
			return c > 0
		}
		return out[i].Filename < out[j].Filename
	})
	return out, nil
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

// shasumRe 匹配 SHASUMS256.txt 的行格式：64 位十六进制 + 两个空格 + 文件名。
var shasumRe = regexp.MustCompile(`^([0-9a-fA-F]{64}) {2}(\S.*)$`)

// ParseShasums 解析 SHASUMS256.txt 为 文件名 → sha256（小写）映射。
//
// 空行与不匹配格式的行直接跳过，不视为错误：镜像只取其中归档文件的
// 对应行，其余行不影响校验。
//
//	@param r io.Reader SHASUMS256.txt 内容。
//	@return map[string]string 文件名到 sha256 十六进制小写的映射。
//	@return error 读取失败时的错误。
func ParseShasums(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	sums := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if m := shasumRe.FindStringSubmatch(line); m != nil {
			sums[m[2]] = strings.ToLower(m[1])
		}
	}
	return sums, nil
}
