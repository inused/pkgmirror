// Package redact 隐藏日志和错误文字中 URL 携带的凭据，保留主机、路径及普通查询条件。
package redact

import (
	"net/url"
	"regexp"
	"strings"
)

// urlPattern 匹配诊断文字中的 URL，双引号和空白作为地址边界。
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>` + "`" + `]+`)

// URL 隐藏 URL 中的用户信息、敏感查询值和片段，供日志展示。
//
// 查询键包含 token、key、secret、pass、auth、signature、credential，或为
// sig、jwt 时隐藏对应值；普通查询条件保留。非法 URL 或查询串整体隐藏。
//
//	@param raw string 原始 URL，仅用于生成展示文字。
//	@return string 脱敏后的 URL；空输入返回空串，无法解析时返回提示文字。
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[无效 URL]"
	}
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		u.RawQuery = "REDACTED"
	} else {
		changed := false
		for key := range query {
			lower := strings.ToLower(key)
			if lower == "sig" || lower == "jwt" {
				query.Set(key, "REDACTED")
				changed = true
				continue
			}
			for _, word := range []string{"token", "key", "secret", "pass", "auth", "signature", "credential"} {
				if strings.Contains(lower, word) {
					query.Set(key, "REDACTED")
					changed = true
					break
				}
			}
		}
		if changed {
			u.RawQuery = query.Encode()
		}
	}
	return u.String()
}

// Text 隐藏诊断文字中各 URL 的凭据，保留周围的错误原因与标点。
//
//	@param text string 日志、命令输出或错误说明。
//	@return string URL 按 URL 函数规则脱敏后的文字。
func Text(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		trimmed := strings.TrimRight(raw, "),.;，。；）")
		return URL(trimmed) + raw[len(trimmed):]
	})
}
