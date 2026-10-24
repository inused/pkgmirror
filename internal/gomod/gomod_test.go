package gomod

import (
	"reflect"
	"strings"
	"testing"
)

func TestEscapeUnescapeModulePath(t *testing.T) {
	cases := map[string]string{
		"github.com/spf13/cobra":       "github.com/spf13/cobra",
		"github.com/Azure/go-ansiterm": "github.com/!azure/go-ansiterm",
		"github.com/Azure/Azure-sdk":   "github.com/!azure/!azure-sdk",
		"gopkg.in/yaml.v3":             "gopkg.in/yaml.v3",
		"golang.org/x/tools":           "golang.org/x/tools",
		"mvdan.cc/xurl/v2":             "mvdan.cc/xurl/v2",
	}
	for path, esc := range cases {
		if got := EscapeModulePath(path); got != esc {
			t.Errorf("EscapeModulePath(%q) = %q，应为 %q", path, got, esc)
		}
		back, err := UnescapeModulePath(esc)
		if err != nil || back != path {
			t.Errorf("UnescapeModulePath(%q) = %q, %v，应为 %q", esc, back, err, path)
		}
	}
	// 非法转义：! 后非小写字母 / 结尾裸 !
	for _, bad := range []string{"github.com/!Azure/x", "a!!b", "x!"} {
		if _, err := UnescapeModulePath(bad); err == nil {
			t.Errorf("UnescapeModulePath(%q) 应报错", bad)
		}
	}
}

func TestValidModulePath(t *testing.T) {
	for _, ok := range []string{
		"github.com/spf13/cobra",
		"github.com/Azure/go-ansiterm",
		"gopkg.in/yaml.v3",
		"golang.org/x/tools",
		"mvdan.cc/xurl/v2",
		"example.com",
	} {
		if !ValidModulePath(ok) {
			t.Errorf("ValidModulePath(%q) 应为 true", ok)
		}
	}
	for _, bad := range []string{
		"",            // 空
		"/a/b",        // 首段空
		"a//b",        // 中间空段
		"a/../b",      // .. 段（首字符 . 不匹配白名单）
		"../a",        // 起始穿越
		"a/b/../../c", // 穿越
		"a/b c",       // 空格
		"a/b%2e%2e/c", // 编码点（未解码前不该出现，解码后是 .. 段被拒）
		"-a/b",        // 段以 - 开头（白名单要求字母数字开头）
		"a/b/",        // 尾空段
	} {
		if ValidModulePath(bad) {
			t.Errorf("ValidModulePath(%q) 应为 false", bad)
		}
	}
}

func TestParseGoSum(t *testing.T) {
	sum := `github.com/spf13/cobra v1.8.1 h1:e5+vHJSNAcdmvA/MXQL2MYxGCI8+FP6hVdZbsCX/TSs=
github.com/spf13/cobra v1.8.1/go.mod h1:NA+2g//5ezNozY1+aGN8pdTI7zr/R+DZrGBjMwTo0rM=
github.com/inconshreveable/mousetrap v1.1.0 h1:wN7xRRV6qV3sSdQF9gABymFb7LbTmHes2X/CF5xvF0=
github.com/inconshreveable/mousetrap v1.1.0/go.mod h1:Z8tu5nH9+vw7aP8CghLzAdxHJmS6E4UdzEwB2zGhCcg=
# 注释行（容错）
golang.org/x/sys v0.28.0 h1: FsNQBY0
gopkg.in/yaml.v3 v3.0.1 h1:euVs= 
`
	mvs, err := ParseGoSum(strings.NewReader(sum))
	if err != nil {
		t.Fatal(err)
	}
	want := []ModuleVersion{
		{"github.com/inconshreveable/mousetrap", "v1.1.0"},
		{"github.com/spf13/cobra", "v1.8.1"},
		{"golang.org/x/sys", "v0.28.0"},
		{"gopkg.in/yaml.v3", "v3.0.1"},
	}
	if !reflect.DeepEqual(mvs, want) {
		t.Fatalf("解析结果 %+v，应为 %+v", mvs, want)
	}
	// 多版本共存：同一模块不同 patch 均保留
	mvs, _ = ParseGoSum(strings.NewReader("m.example/a v1.0.0 h1:x\nm.example/a v1.0.1 h1:y\nm.example/a v1.0.0/go.mod h1:z\n"))
	if len(mvs) != 2 {
		t.Fatalf("同模块两版本应保留，得到 %+v", mvs)
	}
}

func TestCompareVersion(t *testing.T) {
	// 期望 a < b
	ordered := []string{
		"v1.2.3-alpha",
		"v1.2.3",
		"v1.2.10",
		"v1.10.0",
		"v2.0.0",
		"v2.1.0",
		"v10.0.0",
	}
	for i := 0; i < len(ordered)-1; i++ {
		if CompareVersion(ordered[i], ordered[i+1]) >= 0 {
			t.Errorf("%s 应小于 %s", ordered[i], ordered[i+1])
		}
		if CompareVersion(ordered[i+1], ordered[i]) <= 0 {
			t.Errorf("%s 应大于 %s", ordered[i+1], ordered[i])
		}
	}
	// +incompatible 与基础版本视为相等序（latest 二者取一即可）
	if CompareVersion("v2.0.0", "v2.0.0+incompatible") != 0 {
		t.Error("meta 应忽略")
	}
	// pseudo-version 按时间戳段字符串序
	if CompareVersion("v0.0.0-20200101000000-abc", "v0.0.0-20210101000000-def") >= 0 {
		t.Error("时间戳早的 pseudo 应排前")
	}
	// 无法解析排最后
	if CompareVersion("latest", "v1.0.0") <= 0 {
		t.Error("非法串应排可解析串之后")
	}
	if CompareVersion("v1.0.0", "v1.0.0") != 0 {
		t.Error("同版本应相等")
	}
}

func TestLatest(t *testing.T) {
	if got := Latest([]string{"v1.0.0", "v1.2.0", "v1.10.0", "v1.2.3-rc1"}); got != "v1.10.0" {
		t.Errorf("Latest = %q，应为 v1.10.0", got)
	}
	if got := Latest(nil); got != "" {
		t.Errorf("空列表应返回空串，得到 %q", got)
	}
}

func TestFileRe(t *testing.T) {
	for _, ok := range []string{"v1.9.1.mod", "v1.9.1.zip", "v1.9.1.info", "v2.0.0+incompatible.zip", "v0.0.0-20210101000000-abcdef123456.mod"} {
		if !FileRe.MatchString(ok) {
			t.Errorf("FileRe(%q) 应匹配", ok)
		}
	}
	for _, bad := range []string{"1.9.1.zip", "v1.9.1.txt", "v../../x.zip", "v1.9.1.zip.sha256"} {
		if FileRe.MatchString(bad) {
			t.Errorf("FileRe(%q) 不应匹配", bad)
		}
	}
}

func TestLatestStable(t *testing.T) {
	vers := []string{"v1.0.0", "v1.1.0-rc1", "v1.2.0", "v0.0.0-20210101000000-abc", "v1.1.0"}
	if got := LatestStable(vers); got != "v1.2.0" {
		t.Errorf("LatestStable = %q，应为 v1.2.0", got)
	}
	// 全为预发布 → 空串
	if got := LatestStable([]string{"v1.0.0-rc1", "v0.0.0-20210101000000-abc"}); got != "" {
		t.Errorf("全预发布应返回空串，得到 %q", got)
	}
	if !IsStablePrefix("v1.2.0") || IsStablePrefix("v1.2.0-rc1") || IsStablePrefix("v0.0.0-20210101000000-abc") {
		t.Error("IsStablePrefix 判定错误")
	}
	// +incompatible 的 meta 不影响稳定判定
	if !IsStablePrefix("v2.0.0+incompatible") {
		t.Error("+incompatible 应视为稳定版")
	}
}

func TestParseRequires(t *testing.T) {
	content := `module github.com/spf13/cobra

go 1.15

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5
)

require golang.org/x/text v0.3.0 // indirect

toolchain go1.23.4
replace github.com/x/y => ../local
`
	got := ParseRequires(content)
	want := []ModuleVersion{
		{"github.com/inconshreveable/mousetrap", "v1.1.0"},
		{"github.com/spf13/pflag", "v1.0.5"},
		{"golang.org/x/text", "v0.3.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRequires = %+v，应为 %+v", got, want)
	}
	// 伪版本依赖照常提取；非法路径/版本行跳过；空内容安全
	got = ParseRequires("require example.com/odd v0.0.0-20240101000000-abcdef123456\nrequire ../bad v1.0.0\nrequire good.com/x notaver\n")
	want = []ModuleVersion{{"example.com/odd", "v0.0.0-20240101000000-abcdef123456"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("边界行处理 = %+v，应为 %+v", got, want)
	}
	if ParseRequires("") != nil {
		t.Error("空内容应返回空")
	}
}
