// Package gomod 实现 Go module proxy 协议的模块路径编解码、go.sum 精确
// 清单解析与模块版本比较，供下载客户端（download gomod）与服务端
// （/mirror/goproxy/ 路由）共用。
//
// 代理协议规定模块路径中的大写字母转义为 "!" + 小写（如
// github.com/Azure → github.com/!azure），以规避大小写不敏感文件系统上的
// 缓存路径冲突；存储与浏览页使用解码后的原始路径，仅 URL 层转义。
package gomod

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// EscapeModulePath 把模块路径编码为代理请求路径段：大写字母转义为
// "!" + 小写，其余字符不变（模块路径字符集限于 ASCII 字母数字与 -._~，
// 无需其他转义）。
//
//	@param path string 原始模块路径（如 github.com/Azure/go-ansiterm）。
//	@return string 转义后的路径段（如 github.com/!azure/go-ansiterm）。
func EscapeModulePath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('!')
			b.WriteByte(c + ('a' - 'A'))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// UnescapeModulePath 把代理请求路径段还原为模块路径："!" 后跟小写字母
// 还原为大写；"!" 后不是小写字母（含结尾裸 "!"）视为非法。
//
//	@param s string 转义后的路径段。
//	@return string 原始模块路径。
//	@return error 含非法转义序列时的错误。
func UnescapeModulePath(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '!' {
			if i+1 >= len(s) || s[i+1] < 'a' || s[i+1] > 'z' {
				// 取 "!" 及其后一个字符展示非法序列；结尾裸 "!" 只展示自身
				bad := s[i:]
				if i+2 <= len(s) {
					bad = s[i : i+2]
				}
				return "", fmt.Errorf("模块路径含非法转义序列 %q", bad)
			}
			b.WriteByte(s[i+1] - ('a' - 'A'))
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

// modulePathRe 是解码后模块路径的白名单：斜杠分隔的段，每段字母数字
// 开头、字符限 [A-Za-z0-9._~-]；"." / ".." 无法匹配（首字符须为字母数字），
// 路径穿越在白名单层即被拒绝。
var modulePathRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(/[A-Za-z0-9][A-Za-z0-9._~-]*)*$`)

// ValidModulePath 报告解码后的模块路径是否合法（服务端落盘前的校验）。
//
//	@param path string 解码后的原始模块路径。
//	@return bool 合法时为 true。
func ValidModulePath(path string) bool {
	return len(path) <= 512 && modulePathRe.MatchString(path)
}

// FileRe 是 @v 目录下版本文件名的白名单（v1.9.1.mod、v0.0.0-20210101...
// .zip、v2.0.0+incompatible.info 等）。
var FileRe = regexp.MustCompile(`^v[A-Za-z0-9][A-Za-z0-9.+-]*\.(mod|zip|info)$`)

// ModuleVersion 是一个待镜像的模块版本。
type ModuleVersion struct {
	Module  string // 原始模块路径，保留大小写（如 github.com/Azure/azure-sdk）
	Version string // 带 v 前缀（如 v1.2.3、v0.0.0-20210101000000-abcdef123456）
}

// ParseGoSum 解析 go.sum 为模块版本集合（按模块、版本排序）。
//
// go.sum 每行格式为 "模块 版本[/go.mod] h1:哈希"：裸版本行是 zip 哈希，
// 带 /go.mod 等斜杠后缀的行是 go.mod（含 extra go.mod）哈希；同一模块
// 版本的多行合并为一个条目。空行与 # 注释行忽略；字段不足、模块路径
// 非法或版本不以 v 开头的行跳过。
//
//	@param r io.Reader go.sum 内容。
//	@return []ModuleVersion 去重排序后的模块版本集合。
//	@return error 读取失败时的错误。
func ParseGoSum(r io.Reader) ([]ModuleVersion, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	seen := map[ModuleVersion]bool{}
	var out []ModuleVersion
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ver := fields[1]
		// 斜杠后缀（/go.mod 与 extra 变体）去掉，只留版本本体
		if i := strings.Index(ver, "/"); i >= 0 {
			ver = ver[:i]
		}
		if !strings.HasPrefix(ver, "v") || !ValidModulePath(fields[0]) {
			continue
		}
		mv := ModuleVersion{Module: fields[0], Version: ver}
		if !seen[mv] {
			seen[mv] = true
			out = append(out, mv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return CompareVersion(out[i].Version, out[j].Version) < 0
	})
	return out, nil
}

// CompareVersion 比较两个模块版本串（"vX.Y.Z[-pre][+meta]"），返回 -1/0/1。
//
// 语义为 @latest 选择的近似实现：三段数字按数值比较；无预发布段大于有
// 预发布段；预发布段按字符串比较（pseudo-version 的时间戳段定长，
// 字符串序即时间序；"v1.2.3-2" 与 "v1.2.3-10" 这类变长数字段可能错序）；
// +incompatible 等构建元数据忽略；
// 无法解析的串排在可解析串之后。
func CompareVersion(a, b string) int {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return 1
	case !okb:
		return -1
	}
	for i := 0; i < 3; i++ {
		if pa.nums[i] != pb.nums[i] {
			if pa.nums[i] < pb.nums[i] {
				return -1
			}
			return 1
		}
	}
	// 无 pre > 有 pre；同为 pre 时按字符串比较（比较规则见函数注释）
	switch {
	case pa.pre == "" && pb.pre == "":
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	}
	return strings.Compare(pa.pre, pb.pre)
}

// modVersion 是解析后的模块版本。
type modVersion struct {
	nums [3]int
	pre  string
}

// parseVersion 解析 "vX.Y.Z[-pre][+meta]"（meta 忽略）。
//
//	@param v string 版本串。
//	@return modVersion 解析结果。
//	@return bool 格式合法时为 true。
func parseVersion(v string) (modVersion, bool) {
	var mv modVersion
	v = strings.TrimPrefix(v, "v")
	// 构建元数据（+incompatible 等）不参与比较
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "-"); i >= 0 {
		mv.pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return mv, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return mv, false
		}
		mv.nums[i] = n
	}
	return mv, true
}

// Latest 返回版本列表中的最大版本。
//
//	@param versions []string 候选版本串。
//	@return string 最大版本；列表为空时返回空串。
func Latest(versions []string) string {
	var best string
	for _, v := range versions {
		if best == "" || CompareVersion(v, best) > 0 {
			best = v
		}
	}
	return best
}

// LatestStable 返回版本列表中排除预发布段（-rc、-beta、pseudo 时间戳等）
// 后的最大版本，即 @latest 查询在 @v/list 上的本地等价物（minor 前缀
// 匹配用它选该线最新 patch）。
//
// 全部为预发布时返回空串（模块只有 rc/预发布 tag 的罕见情形）。
//
//	@param versions []string 候选版本串。
//	@return string 最大稳定版本；无稳定版本时为空串。
func LatestStable(versions []string) string {
	var stable []string
	for _, v := range versions {
		mv, ok := parseVersion(v)
		if ok && mv.pre == "" {
			stable = append(stable, v)
		}
	}
	return Latest(stable)
}

// IsStablePrefix 报告版本串是否为无预发布段的 "vX.Y.Z" / "vX.Y" 形式，
// 供 minor 前缀匹配跳过预发布候选。
//
//	@param v string 版本串。
//	@return bool 稳定形式时为 true。
func IsStablePrefix(v string) bool {
	mv, ok := parseVersion(v)
	return ok && mv.pre == ""
}

// ParseRequires 从模块的 go.mod 内容（.mod 文件即该文件原文）中提取全部
// require 声明的模块版本，供下载端递归展开依赖闭包。
//
// 覆盖 require 的两种语法：单行（require x v1）与块（require ( ... )）；
// 行内 // 注释（含 // indirect 标记）忽略；module/go/toolchain/exclude/
// replace 等其他指令与无法识别的行跳过。
// replace 是主模块概念，依赖模块 .mod 中的 replace 不参与（与 go 命令
// 行为一致）。
//
//	@param content string go.mod 文件内容。
//	@return []ModuleVersion require 的模块版本列表（保持声明顺序）。
func ParseRequires(content string) []ModuleVersion {
	var out []ModuleVersion
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		// 去行内注释后按空白分字段
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case inBlock:
			if fields[0] == ")" {
				inBlock = false
				continue
			}
			if mv, ok := requirePair(fields); ok {
				out = append(out, mv)
			}
		case fields[0] == "require" && len(fields) >= 3:
			if fields[1] == "(" {
				// require ( 后同行可能跟条目（罕见写法），按块处理
				inBlock = true
				continue
			}
			if mv, ok := requirePair(fields[1:]); ok {
				out = append(out, mv)
			}
		case fields[0] == "require" && len(fields) == 2 && fields[1] == "(":
			inBlock = true
		}
	}
	return out
}

// requirePair 校验 require 条目的 模块 版本 两字段形式。
//
//	@param fields []string require 行去掉指令后的字段。
//	@return ModuleVersion 模块版本。
//	@return bool 字段形式合法（模块路径合法且版本 v 开头）时为 true。
func requirePair(fields []string) (ModuleVersion, bool) {
	if len(fields) < 2 {
		return ModuleVersion{}, false
	}
	mod, ver := fields[0], fields[1]
	if !ValidModulePath(mod) || !strings.HasPrefix(ver, "v") {
		return ModuleVersion{}, false
	}
	return ModuleVersion{Module: mod, Version: ver}, true
}
