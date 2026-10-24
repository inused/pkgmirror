package golang

import (
	"strings"
	"testing"
)

// testIndex 是覆盖选择逻辑的模拟清单：1.23 线含 rc 与补丁混排，1.22 线
// 只有部分平台产物，1.24 线单独验证主版本最新。
var testIndex = []Release{
	{Version: "go1.24.1", Stable: true, Files: []File{
		{Filename: "go1.24.1.linux-amd64.tar.gz", Os: "linux", Arch: "amd64", Version: "go1.24.1", SHA256: "a1", Size: 70, Kind: "archive"},
		{Filename: "go1.24.1.darwin-arm64.tar.gz", Os: "darwin", Arch: "arm64", Version: "go1.24.1", SHA256: "a2", Size: 67, Kind: "archive"},
		{Filename: "go1.24.1.windows-amd64.zip", Os: "windows", Arch: "amd64", Version: "go1.24.1", SHA256: "a3", Size: 71, Kind: "archive"},
		{Filename: "go1.24.1.windows-amd64.msi", Os: "windows", Arch: "amd64", Version: "go1.24.1", SHA256: "i1", Size: 80, Kind: "installer"},
		{Filename: "go1.24.1.src.tar.gz", Version: "go1.24.1", SHA256: "s1", Size: 13, Kind: "source"},
	}},
	{Version: "go1.23.4", Stable: true, Files: []File{
		{Filename: "go1.23.4.linux-amd64.tar.gz", Os: "linux", Arch: "amd64", Version: "go1.23.4", SHA256: "b1", Size: 70, Kind: "archive"},
		{Filename: "go1.23.4.linux-arm64.tar.gz", Os: "linux", Arch: "arm64", Version: "go1.23.4", SHA256: "b2", Size: 66, Kind: "archive"},
		{Filename: "go1.23.4.darwin-amd64.tar.gz", Os: "darwin", Arch: "amd64", Version: "go1.23.4", SHA256: "b3", Size: 68, Kind: "archive"},
		{Filename: "go1.23.4.darwin-arm64.tar.gz", Os: "darwin", Arch: "arm64", Version: "go1.23.4", SHA256: "b4", Size: 67, Kind: "archive"},
		{Filename: "go1.23.4.windows-amd64.zip", Os: "windows", Arch: "amd64", Version: "go1.23.4", SHA256: "b5", Size: 71, Kind: "archive"},
		{Filename: "go1.23.4.windows-arm64.zip", Os: "windows", Arch: "arm64", Version: "go1.23.4", SHA256: "b6", Size: 69, Kind: "archive"},
		{Filename: "go1.23.4.linux-386.tar.gz", Os: "linux", Arch: "386", Version: "go1.23.4", SHA256: "b7", Size: 64, Kind: "archive"},
		{Filename: "go1.23.4.src.tar.gz", Version: "go1.23.4", SHA256: "b8", Size: 13, Kind: "source"},
	}},
	{Version: "go1.23.3", Stable: true, Files: []File{
		{Filename: "go1.23.3.linux-amd64.tar.gz", Os: "linux", Arch: "amd64", Version: "go1.23.3", SHA256: "c1", Size: 70, Kind: "archive"},
	}},
	{Version: "go1.23rc2", Stable: false, Files: []File{
		{Filename: "go1.23rc2.linux-amd64.tar.gz", Os: "linux", Arch: "amd64", Version: "go1.23rc2", Kind: "archive"},
	}},
	{Version: "go1.22.10", Stable: true, Files: []File{
		{Filename: "go1.22.10.linux-amd64.tar.gz", Os: "linux", Arch: "amd64", Version: "go1.22.10", SHA256: "d1", Size: 68, Kind: "archive"},
		{Filename: "go1.22.10.windows-386.zip", Os: "windows", Arch: "386", Version: "go1.22.10", SHA256: "d2", Size: 66, Kind: "archive"},
	}},
}

func TestParseIndex(t *testing.T) {
	rls, err := ParseIndex(strings.NewReader(`[
{"version":"go1.27.3","stable":true,"files":[{"filename":"go1.27.3.src.tar.gz","os":"","arch":"","version":"go1.27.3","sha256":"6924","size":13617171,"kind":"source"}]},
{"version":"go1.27rc3","stable":false,"files":[]}
]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rls) != 2 || rls[0].Version != "go1.27.3" || rls[0].Files[0].SHA256 != "6924" {
		t.Fatalf("解析结果错误: %+v", rls)
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
		{"1", Spec{1, -1, -1}},
		{"1.23", Spec{1, 23, -1}},
		{"1.23.4", Spec{1, 23, 4}},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.in)
		if err != nil || got != c.want {
			t.Fatalf("ParseSpec(%q) = %v, %v，应为 %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "go1.23", "1.23.4.5", "1.x"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Fatalf("ParseSpec(%q) 应报错", bad)
		}
	}
}

func TestSelectDefaultWhitelist(t *testing.T) {
	// 默认白名单：linux/darwin/windows × amd64/arm64，取 1.23 线最新补丁
	files, err := Select(testIndex, []Spec{{1, 23, -1}}, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 6 {
		t.Fatalf("默认白名单应筛出 6 个归档，得到 %d", len(files))
	}
	for _, f := range files {
		if f.Version != "go1.23.4" {
			t.Fatalf("1.23 应取最新补丁 go1.23.4，得到 %s", f.Version)
		}
		if f.Kind != "archive" {
			t.Fatalf("默认不应包含 %s（kind=%s）", f.Filename, f.Kind)
		}
	}
	// installer 与 src 默认排除、386 默认排除
	for _, f := range files {
		if strings.Contains(f.Filename, ".msi") || strings.HasSuffix(f.Filename, ".src.tar.gz") || strings.Contains(f.Filename, "386") {
			t.Fatalf("默认白名单不应包含 %s", f.Filename)
		}
	}
}

func TestSelectExplicitPlatform(t *testing.T) {
	// 显式给出精确匹配，不受白名单限制：386 可指定
	files, err := Select(testIndex, []Spec{{1, 23, -1}}, []string{"linux"}, []string{"386"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Filename != "go1.23.4.linux-386.tar.gz" {
		t.Fatalf("显式 linux/386 应得到 linux-386 归档: %+v", files)
	}
	// 只给 platform：arch 维度仍套白名单，1.22 线 windows 只有 386 产物，
	// 白名单内无匹配 → 整体为空按设计报错（需显式 --arch 386）
	if _, err := Select(testIndex, []Spec{{1, 22, -1}}, []string{"windows"}, nil, false, false); err == nil {
		t.Fatal("windows×amd64/arm64 白名单无产物应报错")
	}
	files, err = Select(testIndex, []Spec{{1, 22, -1}}, []string{"windows"}, []string{"386"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Filename != "go1.22.10.windows-386.zip" {
		t.Fatalf("windows/386 应得到一个归档: %+v", files)
	}
	// 组合缺失跳过不报错：darwin 无 386
	files, err = Select(testIndex, []Spec{{1, 23, -1}}, []string{"linux", "darwin"}, []string{"386"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Filename != "go1.23.4.linux-386.tar.gz" {
		t.Fatalf("darwin-386 缺失应跳过，只剩 linux-386: %+v", files)
	}
}

func TestSelectMajorAndExact(t *testing.T) {
	// 主版本最新：1 → go1.24.1（该线只有 3 个白名单产物）
	files, err := Select(testIndex, []Spec{{1, -1, -1}}, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("1 线最新 go1.24.1 白名单应 3 个归档，得到 %d", len(files))
	}
	// 精确版本
	files, err = Select(testIndex, []Spec{{1, 23, 3}}, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Version != "go1.23.3" {
		t.Fatalf("精确 1.23.3 应得单文件: %+v", files)
	}
	// 版本不存在
	if _, err = Select(testIndex, []Spec{{1, 20, -1}}, nil, nil, false, false); err == nil {
		t.Fatal("1.20 应报无发布")
	}
}

func TestSelectSrcAndAllPlatforms(t *testing.T) {
	// --src：每版本追加 source（不受平台过滤影响）
	files, err := Select(testIndex, []Spec{{1, 23, -1}}, nil, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var src int
	for _, f := range files {
		if f.Kind == "source" {
			src++
		}
	}
	if src != 1 {
		t.Fatalf("src 应追加 1 个源码归档，得到 %d", src)
	}
	// --all-platforms：白名单放开，1.23 线全部 archive（7 个）
	files, err = Select(testIndex, []Spec{{1, 23, -1}}, nil, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("all-platforms 应得 1.23 线全部 7 个归档，得到 %d", len(files))
	}
}

func TestSelectEmptyResult(t *testing.T) {
	// 整体过滤结果为空时报错（1.22 线无 darwin 产物）
	if _, err := Select(testIndex, []Spec{{1, 22, -1}}, []string{"darwin"}, nil, false, false); err == nil {
		t.Fatal("无匹配产物应报错")
	}
}

func TestSelectExcludesRC(t *testing.T) {
	// rc（stable=false）不参与版本选择
	files, err := Select(testIndex, []Spec{{1, 23, 4}}, nil, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f.Version, "rc") {
			t.Fatalf("rc 发布不应入选: %+v", f)
		}
	}
}

func TestVersionOf(t *testing.T) {
	cases := map[string]string{
		"go1.23.4.linux-amd64.tar.gz":  "go1.23.4",
		"go1.23.4.src.tar.gz":          "go1.23.4",
		"go1.27rc3.linux-amd64.tar.gz": "go1.27rc3",
		"node-v22.tar.xz":              "",
	}
	for in, want := range cases {
		if got := VersionOf(in); got != want {
			t.Errorf("VersionOf(%q) = %q，应为 %q", in, got, want)
		}
	}
}

func TestCompareVersion(t *testing.T) {
	if CompareVersion("go1.23.4", "go1.23.10") >= 0 {
		t.Error("go1.23.4 应小于 go1.23.10")
	}
	if CompareVersion("go1.24.0", "go1.23.99") <= 0 {
		t.Error("go1.24.0 应大于 go1.23.99")
	}
	if CompareVersion("go1.23.4", "go1.23.4") != 0 {
		t.Error("同版本应相等")
	}
}
