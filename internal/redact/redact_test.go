package redact

import (
	"strings"
	"testing"
)

// TestURL 覆盖用户信息、重复与编码查询键、签名地址和非法输入的脱敏。
func TestURL(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"普通地址", "https://mirror.example/simple?python=3.12&arch=arm64", "https://mirror.example/simple?python=3.12&arch=arm64"},
		{"用户信息", "https://alice:p%40ss@mirror.example/simple", "https://mirror.example/simple"},
		{"查询凭据", "https://mirror.example/simple?%74oken=one&token=two&X-Amz-Signature=three&python=3.12", "https://mirror.example/simple?X-Amz-Signature=REDACTED&python=3.12&token=REDACTED"},
		{"签名短键", "https://mirror.example/file?sig=one&jwt=two&se=2030-01-01", "https://mirror.example/file?jwt=REDACTED&se=2030-01-01&sig=REDACTED"},
		{"仅用户名", "socks5://alice@proxy.example:1080", "socks5://proxy.example:1080"},
		{"片段", "https://mirror.example/file#access_token=one", "https://mirror.example/file"},
		{"非法查询", "https://mirror.example/file?token=%zz", "https://mirror.example/file?REDACTED"},
		{"非法地址", "https://alice:secret@mirror.example/%zz", "[无效 URL]"},
		{"空地址", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := URL(c.raw); got != c.want {
				t.Errorf("脱敏结果 = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestText 验证嵌套请求错误中的多个 URL 均被脱敏，状态与标点保留。
func TestText(t *testing.T) {
	input := `下载 https://alice:password@mirror.example/a?token=first 失败: Get "https://mirror.example/a?api_key=second": 401（代理 socks5://bob:proxy-pass@proxy.example:1080）。`
	got := Text(input)
	for _, secret := range []string{"alice", "password", "first", "second", "bob", "proxy-pass"} {
		if strings.Contains(got, secret) {
			t.Errorf("诊断文字仍包含凭据 %q", secret)
		}
	}
	for _, want := range []string{"mirror.example/a", "proxy.example:1080", "401", "）。"} {
		if !strings.Contains(got, want) {
			t.Errorf("诊断文字缺少 %q: %s", want, got)
		}
	}
	if got := Text("连接超时"); got != "连接超时" {
		t.Errorf("普通错误应原样保留，得到 %q", got)
	}
}
