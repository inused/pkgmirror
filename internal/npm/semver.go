// semver.go 实现用于校验 engines.node 的语义化版本 range 求值，
// 语法对齐 node-semver 的 ranges（比较符、x-range、~、^、连字符范围、
// 空格 AND、|| OR）。无法解析的 range 按满足处理。
package npm

import (
	"strconv"
	"strings"
)

// semVer 是完整的三段语义化版本，含可选的预发布段。
type semVer struct {
	major, minor, patch int
	pre                 []string // 预发布段（点分）；空切片表示正式版
}

// parseSemVer 解析严格的三段版本号（允许 v 前缀与预发布段，忽略 build 段）。
//
//	@param s string 原始串（如 v18.20.1、1.2.3-beta.1）。
//	@return semVer 解析结果。
//	@return bool 格式合法时为 true。
func parseSemVer(s string) (semVer, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semVer
	main, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return v, false
	}
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return v, false
	}
	if v.minor, err = strconv.Atoi(parts[1]); err != nil {
		return v, false
	}
	if v.patch, err = strconv.Atoi(parts[2]); err != nil {
		return v, false
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, seg := range v.pre {
			if seg == "" {
				return v, false
			}
		}
	}
	return v, true
}

// sameBase 判断两个版本的主三段是否相同（预发布匹配规则用）。
func sameBase(a, b semVer) bool {
	return a.major == b.major && a.minor == b.minor && a.patch == b.patch
}

// compare 按语义化版本规范排序：主三段数值比较，正式版大于预发布版，
// 预发布段逐点比较（数字段按数值、数字段小于字母段，段多者大）。
//
//	@param other semVer 另一版本。
//	@return int 小于 0 表示本版本更小，0 相等，大于 0 更大。
func (v semVer) compare(other semVer) int {
	if v.major != other.major {
		return cmpInt(v.major, other.major)
	}
	if v.minor != other.minor {
		return cmpInt(v.minor, other.minor)
	}
	if v.patch != other.patch {
		return cmpInt(v.patch, other.patch)
	}
	if len(v.pre) == 0 && len(other.pre) == 0 {
		return 0
	}
	if len(v.pre) == 0 {
		return 1 // 正式版 > 预发布版
	}
	if len(other.pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		a, b := v.pre[i], other.pre[i]
		an, aerr := strconv.Atoi(a)
		bn, berr := strconv.Atoi(b)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return cmpInt(an, bn)
			}
		case aerr == nil: // 数字段 < 字母段
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a, b); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(v.pre), len(other.pre))
}

// cmpInt 比较两个 int。
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparator 是单个比较约束（如 >=1.2.3）。hasPre 记录原始串是否带
// 预发布段，用于预发布匹配规则。
type comparator struct {
	op     string // ">="、"<="、">"、"<"、"=" 之一
	ver    semVer
	hasPre bool
}

// NodeSatisfies 判断 node 版本 v 是否满足 engines 的 range。
//
// range 语法（node-semver ranges 子集）：`>=18`、`^18 || ^20`、
// `14.x - 16.x`、`~4.1`、空格 AND、`||` OR、`*` 任意。range 为空或
// 任一 OR 分支无法解析时，该分支按满足处理。
//
//	@param v string 目标 node 版本（形如 18.20.0）。
//	@param rng string engines.node 的 range 原文。
//	@return bool 满足时为 true；v 本身格式非法时恒为 true（视为不限）。
func NodeSatisfies(v, rng string) bool {
	ver, ok := parseSemVer(v)
	if !ok {
		return true
	}
	rng = strings.TrimSpace(rng)
	if rng == "" {
		return true
	}
	// || 分隔的任一分支满足即可
	for _, alt := range strings.Split(rng, "||") {
		if satisfiesSet(ver, strings.TrimSpace(alt)) {
			return true
		}
	}
	return false
}

// satisfiesSet 判断版本是否满足一个 AND 分支（空格分隔的比较器组，
// 含连字符范围）。
//
//	@param v semVer 目标版本。
//	@param set string 分支原文。
//	@return bool 满足时为 true；分支为空（"" 或 "*"）恒满足；含无法解析
//	 的比较器时按满足处理（宽松）。
func satisfiesSet(v semVer, set string) bool {
	if set == "" || set == "*" || set == "x" || set == "X" {
		return true
	}
	tokens := strings.Fields(set)
	// 先把 "lo - hi" 三元组合成单个条目，避免下界先按普通比较器入列
	// 造成重复约束（如 "1.2.3 - 2" 中的 1.2.3 展开为精确匹配）
	var merged []string
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == "-" && i > 0 && len(merged) > 0 && i+1 < len(tokens) {
			merged[len(merged)-1] = merged[len(merged)-1] + " - " + tokens[i+1]
			i++ // 跳过 hi（下一轮 i++ 跳过当前 "-"）
			continue
		}
		merged = append(merged, tokens[i])
	}
	var comps []comparator
	anyValid := false
	for _, tok := range merged {
		if lo, hi, isHyphen := strings.Cut(tok, " - "); isHyphen {
			cs, ok := hyphenComparators(lo, hi)
			if !ok {
				return true
			}
			comps = append(comps, cs...)
			anyValid = true
			continue
		}
		cs, ok := parseComparator(tok)
		if !ok {
			return true
		}
		comps = append(comps, cs...)
		anyValid = true
	}
	if !anyValid {
		return true
	}
	for _, c := range comps {
		if !v.matches(c) {
			return false
		}
	}
	// 预发布匹配规则：目标版本带预发布段时，分支中必须存在带预发布段
	// 且主三段相同的比较器，否则（如 1.2.3-beta 对 >=1.2.3）不满足
	if len(v.pre) > 0 {
		for _, c := range comps {
			if c.hasPre && sameBase(v, c.ver) {
				return true
			}
		}
		return false
	}
	return true
}

// matches 判断版本是否满足单个比较器。
func (v semVer) matches(c comparator) bool {
	r := v.compare(c.ver)
	switch c.op {
	case ">=":
		return r >= 0
	case "<=":
		return r <= 0
	case ">":
		return r > 0
	case "<":
		return r < 0
	default: // "="
		return r == 0
	}
}

// hyphenComparators 把连字符范围 lo - hi 展开为两个比较器。
//
// partial 规则：hi 缺段时按"包含到该段上限"展开（2.3 → <2.4.0，
// 2 → <3.0.0）。
//
//	@param lo string 下界版本（可为 partial）。
//	@param hi string 上界版本（可为 partial）。
//	@return []comparator 展开的比较器（>=lo 与 <=hi / <hi 上限）。
//	@return bool 任一侧无法解析时为 false。
func hyphenComparators(lo, hi string) ([]comparator, bool) {
	loV, loParts, ok := parsePartial(lo)
	if !ok {
		return nil, false
	}
	loC := comparator{op: ">=", ver: fillPartial(loV, loParts)}
	loC.hasPre = len(loV.pre) > 0

	hiV, hiParts, ok := parsePartial(hi)
	if !ok {
		return nil, false
	}
	if hiParts >= 3 {
		return []comparator{loC, {op: "<=", ver: hiV, hasPre: len(hiV.pre) > 0}}, true
	}
	// partial 上界：升一位并严格小于（2.3 → <2.4.0；2 → <3.0.0）
	if hiParts == 2 {
		hiV.minor++
		hiV.patch = 0
	} else {
		hiV.major++
		hiV.minor, hiV.patch = 0, 0
	}
	hiV.pre = nil
	return []comparator{loC, {op: "<", ver: hiV}}, true
}

// parseComparator 解析单个比较器 token 并按需展开（~、^、x-range 产出
// 两个比较器）。
//
//	@param tok string 比较器原文（如 >=18、^18、~4.1、18.2、18.2.x）。
//	@return []comparator 展开后的比较器。
//	@return bool 无法解析时为 false（调用方按宽松满足处理）。
func parseComparator(tok string) ([]comparator, bool) {
	for _, op := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(tok, op) {
			return expand(op, strings.TrimSpace(strings.TrimPrefix(tok, op)))
		}
	}
	// 无操作符：x-range 或精确版本
	return expand("=", tok)
}

// expand 按操作符与 partial 版本展开比较器。
//
//	@param op string 操作符（>= <= > < = ^ ~）。
//	@param rest string 版本原文（可为 partial：18、18.2、18.2.x）。
//	@return []comparator 展开结果；"^"/"~" 与 x-range 各产出两个比较器。
//	@return bool 解析失败时为 false。
func expand(op, rest string) ([]comparator, bool) {
	if rest == "*" || rest == "x" || rest == "X" || rest == "" {
		if op == "^" || op == "~" || op == "=" {
			return nil, true // 任意版本：无约束
		}
		return nil, false // "> *" 等非法
	}
	v, parts, ok := parsePartial(rest)
	if !ok {
		return nil, false
	}
	hasPre := len(v.pre) > 0
	switch op {
	case ">=", "<=", ">", "<":
		return []comparator{{op: op, ver: fillPartial(v, parts), hasPre: hasPre}}, true
	case "=":
		switch parts {
		case 3:
			return []comparator{{op: "=", ver: v, hasPre: hasPre}}, true
		case 2: // 18.2 → >=18.2.0 <18.3.0
			upper := v
			upper.minor++
			upper.patch = 0
			upper.pre = nil
			return []comparator{{op: ">=", ver: v}, {op: "<", ver: upper}}, true
		default: // 18 → >=18.0.0 <19.0.0
			upper := v
			upper.major++
			upper.minor, upper.patch = 0, 0
			upper.pre = nil
			return []comparator{{op: ">=", ver: fillPartial(v, parts)}, {op: "<", ver: upper}}, true
		}
	case "~":
		base := fillPartial(v, parts)
		upper := base
		upper.pre = nil
		if parts >= 2 { // ~1.2 → <1.3.0
			upper.minor++
			upper.patch = 0
		} else { // ~1 → <2.0.0
			upper.major++
			upper.minor, upper.patch = 0, 0
		}
		return []comparator{{op: ">=", ver: base, hasPre: hasPre}, {op: "<", ver: upper}}, true
	default: // ^
		base := fillPartial(v, parts)
		upper := base
		upper.pre = nil
		switch {
		case upper.major > 0 || parts <= 1: // ^18、^18.2 → <19.0.0
			upper.major++
			upper.minor, upper.patch = 0, 0
		case upper.minor > 0 || parts == 2: // ^0.2、^0.2.3 → <0.3.0
			upper.minor++
			upper.patch = 0
		default: // ^0.0.3 → <0.0.4
			upper.patch++
		}
		return []comparator{{op: ">=", ver: base, hasPre: hasPre}, {op: "<", ver: upper}}, true
	}
}

// fillPartial 把 partial 版本补齐为完整版本（缺段补 0，x/* 段视为缺）。
//
//	@param v semVer 已解析的版本（缺段值无意义）。
//	@param parts int 给出的段数（1~3）。
//	@return semVer 补零后的完整版本。
func fillPartial(v semVer, parts int) semVer {
	if parts < 3 {
		v.patch = 0
	}
	if parts < 2 {
		v.minor = 0
	}
	return v
}

// parsePartial 解析 partial 版本（18、18.2、18.20.1、18.x、18.2.*）。
//
//	@param s string 版本原文。
//	@return semVer 版本值（缺段为 0，需配合 parts 使用）。
//	@return int 实际给出的段数（x/X/*/空 之后的段不计）。
//	@return bool 格式非法时为 false。
func parsePartial(s string) (semVer, int, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	main, pre, hasPre := strings.Cut(s, "-")
	var v semVer
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, seg := range v.pre {
			if seg == "" {
				return v, 0, false
			}
		}
	}
	var nums []int
	for _, seg := range strings.Split(main, ".") {
		if seg == "" || seg == "x" || seg == "X" || seg == "*" {
			break // 通配段及其后视为未给出
		}
		n, err := strconv.Atoi(seg)
		if err != nil || n < 0 {
			return v, 0, false
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 || len(nums) > 3 {
		return v, 0, false
	}
	v.major, v.minor, v.patch = nums[0], 0, 0
	if len(nums) >= 2 {
		v.minor = nums[1]
	}
	if len(nums) == 3 {
		v.patch = nums[2]
	}
	// 预发布段必须挂在完整三段版本上（"1.2-beta" 非标，按非法处理）
	if hasPre && len(nums) != 3 {
		return v, 0, false
	}
	return v, len(nums), true
}
