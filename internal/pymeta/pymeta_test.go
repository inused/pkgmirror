package pymeta

import (
	"strings"
	"testing"
)

// testMeta 构造覆盖关键分支的最小元数据：3.12 两个 patch、3.13 一个 patch，
// 含 freethreaded 变体、prerelease、pypy 与不同平台条目。
func testMeta() Metadata {
	return Metadata{
		"cpython-3.13.1-linux-x86_64-gnu":    {Name: "cpython", Major: 3, Minor: 13, Patch: 1, Os: "linux", Libc: "gnu", URL: "u://20261001/cpython-3.13.1-linux.tar.gz", Build: "20261001"},
		"cpython-3.13.1-windows-x86_64-msvc": {Name: "cpython", Major: 3, Minor: 13, Patch: 1, Os: "windows", URL: "u://20261001/cpython-3.13.1-windows.tar.gz", Build: "20261001"},
		"cpython-3.12.9-linux-x86_64-gnu":    {Name: "cpython", Major: 3, Minor: 12, Patch: 9, Os: "linux", URL: "u://20260101/cpython-3.12.9-linux.tar.gz", Build: "20260101"},
		"cpython-3.12.9-windows-x86_64-msvc": {Name: "cpython", Major: 3, Minor: 12, Patch: 9, Os: "windows", URL: "u://20260101/cpython-3.12.9-windows.tar.gz", Build: "20260101"},
		"cpython-3.12.4-linux-x86_64-gnu":    {Name: "cpython", Major: 3, Minor: 12, Patch: 4, Os: "linux", URL: "u://20240615/cpython-3.12.4-linux.tar.gz", Build: "20240615"},
		"cpython-3.12.4+freethreaded-linux":  {Name: "cpython", Major: 3, Minor: 12, Patch: 4, Os: "linux", URL: "u://20240615/cpython-3.12.4+t-linux.tar.gz", Build: "20240615", Variant: strPtr("freethreaded")},
		"cpython-3.14.0a1-linux-x86_64-gnu":  {Name: "cpython", Major: 3, Minor: 14, Patch: 0, Os: "linux", Prerelease: "a1", URL: "u://a/cpython-3.14.0a1.tar.gz"},
		"pypy-3.11.9-linux-x86_64-gnu":       {Name: "pypy", Major: 3, Minor: 11, Patch: 9, Os: "linux", URL: "u://p/pypy.tar.gz"},
	}
}

func strPtr(s string) *string { return &s }

func TestSelectLatest(t *testing.T) {
	got, err := testMeta().Select([]Spec{{3, 12, 0}}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("3.12 最新 patch 应有 2 个平台文件, 得到 %d", len(got))
	}
	for _, e := range got {
		if e.Patch != 9 {
			t.Errorf("应选最新 patch 9, 得到 %d", e.Patch)
		}
	}
}

func TestSelectExactPatch(t *testing.T) {
	got, err := testMeta().Select([]Spec{{3, 12, 4}}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Patch != 4 {
		t.Errorf("精确 3.12.4 应只有 1 个文件（变体已排除）, 得到 %+v", got)
	}
}

func TestSelectAllPatches(t *testing.T) {
	got, err := testMeta().Select([]Spec{{3, 12, 0}}, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 { // 3.12.9 两个平台 + 3.12.4 一个（freethreaded 排除）
		t.Errorf("--all-patches 应得到 3 个文件, 得到 %d", len(got))
	}
}

func TestSelectPlatformFilter(t *testing.T) {
	got, err := testMeta().Select([]Spec{{3, 13, 0}}, []string{"windows"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Os != "windows" {
		t.Errorf("平台过滤 windows 失败: %+v", got)
	}
}

func TestSelectMissing(t *testing.T) {
	if _, err := testMeta().Select([]Spec{{3, 11, 0}}, nil, nil, false); err == nil {
		t.Error("不存在的 minor 应报错")
	}
	if _, err := testMeta().Select([]Spec{{3, 12, 5}}, nil, nil, false); err == nil {
		t.Error("不存在的 patch 应报错")
	}
}

func TestParseMetadata(t *testing.T) {
	m, err := ParseMetadata(strings.NewReader(`{"cpython-3.12.7-windows-x86_64-msvc":{"name":"cpython","os":"windows","major":3,"minor":12,"patch":7,"url":"https://example/cpython.tar.gz","sha256":"abc","build":"20241016"}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := m["cpython-3.12.7-windows-x86_64-msvc"]
	if e.Version() != "3.12.7" || e.Build != "20241016" || e.SHA256 != "abc" {
		t.Errorf("条目解析错误: %+v", e)
	}
}

func TestSelectArchFilter(t *testing.T) {
	// 补充带架构差异的条目后验证 arch 过滤与别名归一
	meta := testMeta()
	meta["cpython-3.13.1-linux-aarch64-gnu"] = Entry{
		Name: "cpython", Major: 3, Minor: 13, Patch: 1, Os: "linux",
		Arch: archInfo{Family: "aarch64"}, URL: "u://20261001/cpython-3.13.1-aarch64.tar.gz", Build: "20261001",
	}
	meta["cpython-3.13.1-linux-x86_64-gnu"] = Entry{
		Name: "cpython", Major: 3, Minor: 13, Patch: 1, Os: "linux",
		Arch: archInfo{Family: "x86_64"}, URL: "u://20261001/cpython-3.13.1-x86_64.tar.gz", Build: "20261001",
	}
	got, err := meta.Select([]Spec{{3, 13, 0}}, []string{"linux"}, []string{"arm64"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Arch.Family != "aarch64" {
		t.Errorf("arm64 别名应归一为 aarch64 且只匹配 1 条: %+v", got)
	}
}
