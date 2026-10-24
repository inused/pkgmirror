package node

import (
	"strings"
	"testing"
)

// testIndex 是覆盖选择逻辑的模拟索引：22 线含 LTS 与非 LTS 混排，
// 21 线无 arm64 产物（验证缺失报错），20 线已停止维护。
var testIndex = []Release{
	{Version: "v22.14.0", LTS: "Jod", Files: []string{"linux-x64", "linux-arm64", "osx-x64-tar", "osx-arm64-tar", "win-x64-zip", "win-arm64-zip", "linux-x64-musl", "win-x64-exe", "src"}},
	{Version: "v22.13.1", LTS: "Jod", Files: []string{"linux-x64", "linux-arm64", "osx-x64-tar", "osx-arm64-tar", "win-x64-zip", "win-arm64-zip"}},
	{Version: "v22.12.0", LTS: false, Files: []string{"linux-x64", "osx-arm64-tar", "win-x64-zip"}},
	{Version: "v21.7.3", LTS: false, Files: []string{"linux-x64", "osx-x64-tar", "win-x64-zip"}},
	{Version: "v20.18.2", LTS: "Iron", Files: []string{"linux-x64", "linux-arm64", "osx-x64-tar", "osx-arm64-tar", "win-x64-zip", "win-arm64-zip"}},
}

func TestParseIndex(t *testing.T) {
	rls, err := ParseIndex(strings.NewReader(`[
{"version":"v22.14.0","date":"2025-02-11","files":["linux-x64","src"],"npm":"10.9.2","lts":"Jod"},
{"version":"v23.6.1","date":"2025-01-08","files":["linux-x64","src"],"npm":"10.9.2","lts":false}
]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rls) != 2 {
		t.Fatalf("条目数 = %d，应为 2", len(rls))
	}
	if !rls[0].IsLTS() || rls[1].IsLTS() {
		t.Fatalf("LTS 判定错误: %v %v", rls[0].LTS, rls[1].LTS)
	}
	if _, err := ParseIndex(strings.NewReader("{")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in   string
		want Spec
	}{
		{"22", Spec{22, -1, -1}},
		{"22.11", Spec{22, 11, -1}},
		{"22.11.0", Spec{22, 11, 0}},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.in)
		if err != nil || got != c.want {
			t.Fatalf("ParseSpec(%q) = %v, %v，应为 %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "v22", "22.11.0.1", "22.x"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Fatalf("ParseSpec(%q) 应报错", bad)
		}
	}
}

func TestSelectLatestOfMajor(t *testing.T) {
	// 22 线最新是 v22.14.0（跨 LTS 与非 LTS 混排取 semver 最大）
	files, err := Select(testIndex, []Spec{{22, -1, -1}}, false, []string{"linux"}, []string{"x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	want := File{"v22.14.0", "node-v22.14.0-linux-x64.tar.xz"}
	if len(files) != 1 || files[0] != want {
		t.Fatalf("结果 = %v，应为 [%v]", files, want)
	}
}

func TestSelectMinorAndExact(t *testing.T) {
	// 22.13 线最新是 v22.13.1
	files, err := Select(testIndex, []Spec{{22, 13, -1}}, false, []string{"linux"}, []string{"x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Version != "v22.13.1" {
		t.Fatalf("minor 选择结果 = %v", files)
	}
	// 精确匹配旧版本
	files, err = Select(testIndex, []Spec{{22, 12, 0}}, false, []string{"linux"}, []string{"x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Version != "v22.12.0" {
		t.Fatalf("精确选择结果 = %v", files)
	}
}

func TestSelectLTS(t *testing.T) {
	files, err := Select(testIndex, nil, true, []string{"linux"}, []string{"x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Version != "v22.14.0" {
		t.Fatalf("--lts 选择结果 = %v，应为 v22.14.0（当前最新 LTS）", files)
	}
}

func TestSelectAllPlatformsDefault(t *testing.T) {
	// 默认全平台全架构 = 3 os × 2 arch = 6 个归档，且不含 musl/exe/src
	files, err := Select(testIndex, []Spec{{20, -1, -1}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 6 {
		t.Fatalf("默认应展开 6 个归档，实际 %d: %v", len(files), files)
	}
	for _, f := range files {
		if strings.Contains(f.Filename, "musl") || strings.Contains(f.Filename, "exe") {
			t.Fatalf("musl/安装器变体未被排除: %v", f)
		}
	}
	// macOS 文件名 os 段是 darwin 而非 osx
	want := File{"v20.18.2", "node-v20.18.2-darwin-arm64.tar.xz"}
	found := false
	for _, f := range files {
		if f == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("应包含 %v，实际 %v", want, files)
	}
}

func TestSelectPlatformAliasAndFilter(t *testing.T) {
	// darwin/macos 与 amd64/arm64 别名均应归一
	for _, os := range []string{"darwin", "macos"} {
		files, err := Select(testIndex, []Spec{{22, -1, -1}}, false, []string{os}, []string{"amd64"})
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || files[0].Filename != "node-v22.14.0-darwin-x64.tar.xz" {
			t.Fatalf("平台 %s 别名归一失败: %v", os, files)
		}
	}
	// Windows 走 zip
	files, err := Select(testIndex, []Spec{{22, -1, -1}}, false, []string{"windows"}, []string{"aarch64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Filename != "node-v22.14.0-win-arm64.zip" {
		t.Fatalf("windows zip 映射失败: %v", files)
	}
}

func TestSelectMissing(t *testing.T) {
	// 版本不存在
	if _, err := Select(testIndex, []Spec{{23, -1, -1}}, false, nil, nil); err == nil {
		t.Fatal("不存在的版本应报错")
	}
	// 21.7.3 无 linux-arm64 归档，应报标识缺失
	if _, err := Select(testIndex, []Spec{{21, -1, -1}}, false, []string{"linux"}, []string{"aarch64"}); err == nil {
		t.Fatal("缺失平台归档应报错")
	}
	// 无 LTS 时报错
	if _, err := Select(testIndex[:0], nil, true, nil, nil); err == nil {
		t.Fatal("空索引取 LTS 应报错")
	}
}

func TestParseShasums(t *testing.T) {
	sums, err := ParseShasums(strings.NewReader(`
9f8ea7a1b6e0ff44774ea0d4f7e56e90dbea8dfa02b1e2ba2e2f9c539030d2f0  node-v22.14.0-linux-x64.tar.xz
ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789  node-v22.14.0-win-x64.zip
not a valid line
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 {
		t.Fatalf("应解析 2 行，实际 %d: %v", len(sums), sums)
	}
	if sums["node-v22.14.0-win-x64.zip"] != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("大写哈希应归一为小写: %v", sums)
	}
}
