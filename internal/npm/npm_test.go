package npm

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{"lodash", "@babel/core", "a", "pkg.name", "pkg_name", "x-y.z_1~2"}
	for _, n := range valid {
		if !ValidName(n) {
			t.Errorf("%q 应为合法包名", n)
		}
	}
	invalid := []string{"", "/lodash", "lodash/", "@", "@scope", "@/pkg", "@scope/sub/pkg", "UPPER", "@scope/Pkg", "pkg name", strings.Repeat("a", 215), "pkg@1"}
	for _, n := range invalid {
		if ValidName(n) {
			t.Errorf("%q 应为非法包名", n)
		}
	}
}

// lockV3 是覆盖各条目形态的 package-lock 样例（v3，packages 节点）。
const lockV3 = `{
  "name": "proj",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "proj", "version": "1.0.0", "dependencies": {"lodash": "^4.0.0"}},
    "node_modules/lodash": {
      "version": "4.17.21",
      "resolved": "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz",
      "integrity": "sha512-v2bX5aGkL1..."
    },
    "node_modules/@babel/core": {
      "version": "7.24.0",
      "resolved": "https://registry.npmjs.org/@babel/core/-/core-7.24.0.tgz",
      "integrity": "sha512-aaa..."
    },
    "node_modules/esbuild/node_modules/@esbuild/linux-x64": {
      "version": "0.21.0",
      "resolved": "https://registry.npmjs.org/@esbuild/linux-x64/-/linux-x64-0.21.0.tgz",
      "integrity": "sha512-bbb...",
      "os": ["linux", "!win32"],
      "cpu": ["x64"]
    },
    "node_modules/dup": {"version": "1.0.0", "resolved": "https://registry.npmjs.org/dup/-/dup-1.0.0.tgz"},
    "node_modules/nested/node_modules/lodash": {
      "version": "4.17.21",
      "resolved": "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz"
    },
    "node_modules/gitdep": {"version": "1.0.0", "resolved": "git+ssh://git@github.com/x/y.git#abc"},
    "node_modules/localdep": {"version": "0.0.1", "resolved": "file:../local"},
    "node_modules/bundled": {"version": "2.0.0"},
    "node_modules/link": {"version": "1.0.0", "resolved": "file:../ws", "link": true}
  }
}`

func TestParseLock(t *testing.T) {
	entries, skipped, err := ParseLock(strings.NewReader(lockV3))
	if err != nil {
		t.Fatal(err)
	}
	// 根条目、git/file/link/bundled 均不产出条目
	if len(entries) != 4 {
		t.Fatalf("应解析出 4 个条目, 得到 %d: %+v", len(entries), entries)
	}
	// 排序：@babel/core < @esbuild/linux-x64 < dup < lodash
	wantNames := []string{"@babel/core", "@esbuild/linux-x64", "dup", "lodash"}
	for i, e := range entries {
		if e.Name != wantNames[i] {
			t.Errorf("第 %d 个条目应为 %s, 得到 %s", i, wantNames[i], e.Name)
		}
	}
	// os/cpu 的否定值剔除，正向值保留
	for _, e := range entries {
		if e.Name == "@esbuild/linux-x64" {
			if len(e.OS) != 1 || e.OS[0] != "linux" {
				t.Errorf("os 应剔除否定值得到 [linux], 得到 %v", e.OS)
			}
			if len(e.CPU) != 1 || e.CPU[0] != "x64" {
				t.Errorf("cpu 应为 [x64], 得到 %v", e.CPU)
			}
		}
	}
	// git/file 条目进入 skipped；bundled/link 不进入
	if len(skipped) != 2 {
		t.Fatalf("应跳过 2 个非 registry 条目, 得到 %d: %v", len(skipped), skipped)
	}
	for _, s := range skipped {
		if !strings.Contains(s, "gitdep") && !strings.Contains(s, "localdep") {
			t.Errorf("意外的跳过条目: %s", s)
		}
	}
}

func TestParseLockV1Rejected(t *testing.T) {
	v1 := `{"lockfileVersion": 1, "dependencies": {"lodash": {"version": "4.17.21", "integrity": "sha512-x"}}}`
	if _, _, err := ParseLock(strings.NewReader(v1)); err == nil || !strings.Contains(err.Error(), "npm ≥ 7") {
		t.Errorf("v1 lock 应报错并提示重新生成, 得到: %v", err)
	}
}

func TestMatchOSCPU(t *testing.T) {
	e := Entry{Name: "@esbuild/linux-x64", OS: []string{"linux"}, CPU: []string{"x64"}}
	cases := []struct {
		platforms, archs []string
		want             bool
	}{
		{nil, nil, true}, // 未过滤：全收
		{[]string{"linux"}, []string{"x86_64"}, true},    // 别名 x86_64 → x64
		{[]string{"windows"}, []string{"x86_64"}, false}, // windows → win32 不匹配
		{[]string{"linux", "windows"}, []string{"aarch64"}, false},
	}
	for _, c := range cases {
		if got := MatchOSCPU(e, c.platforms, c.archs); got != c.want {
			t.Errorf("MatchOSCPU(%v,%v) 应为 %v", c.platforms, c.archs, c.want)
		}
	}
	// win32 映射：--platform windows 命中 npm 的 os=win32 条目
	w := Entry{Name: "x", OS: []string{"win32"}, CPU: []string{"x64"}}
	if !MatchOSCPU(w, []string{"windows"}, []string{"amd64"}) {
		t.Error("windows/amd64 应匹配 win32/x64")
	}
	// 未标注 os/cpu 的条目恒匹配
	plain := Entry{Name: "lodash"}
	if !MatchOSCPU(plain, []string{"linux"}, []string{"aarch64"}) {
		t.Error("未标注平台的条目不应被过滤")
	}
}

// packumentV2 是含两个版本的 packument 样例。
const packumentV2 = `{
  "_id": "lodash",
  "dist-tags": {"latest": "4.17.21"},
  "versions": {
    "4.17.20": {"dist": {"tarball": "https://registry.npmjs.org/lodash/-/lodash-4.17.20.tgz", "integrity": "sha512-old..."}},
    "4.17.21": {"dist": {"tarball": "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz", "integrity": "sha512-new..."}}
  },
  "time": {"modified": "2021-02-20T01:00:00.000Z"}
}`

func TestTarballInfo(t *testing.T) {
	url, sri, engines, err := TarballInfo([]byte(packumentV2), "4.17.21")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz" || sri != "sha512-new..." || engines != "" {
		t.Errorf("提取错误: %s %s %q", url, sri, engines)
	}
	if _, _, _, err := TarballInfo([]byte(packumentV2), "5.0.0"); err == nil {
		t.Error("不存在的版本应报错")
	}
	// engines.node 提取
	withEngines := `{"versions":{"22.0.0":{"dist":{"tarball":"https://r/x-22.0.0.tgz"},"engines":{"node":">=18"}}}}`
	_, _, engines, err = TarballInfo([]byte(withEngines), "22.0.0")
	if err != nil || engines != ">=18" {
		t.Errorf("engines 提取错误: %q %v", engines, err)
	}
}

func TestRewriteTarballs(t *testing.T) {
	out, err := RewriteTarballs([]byte(packumentV2), "http://h:8080", "lodash")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// 全部版本的 tarball 重写，其它字段保留
	if !strings.Contains(s, `"tarball":"http://h:8080/mirror/npm/lodash/-/lodash-4.17.20.tgz"`) {
		t.Errorf("4.17.20 未重写: %s", s)
	}
	if !strings.Contains(s, `"tarball":"http://h:8080/mirror/npm/lodash/-/lodash-4.17.21.tgz"`) {
		t.Errorf("4.17.21 未重写: %s", s)
	}
	if !strings.Contains(s, `"latest":"4.17.21"`) || !strings.Contains(s, `"modified"`) {
		t.Errorf("dist-tags/time 字段丢失: %s", s)
	}
	if strings.Contains(s, "registry.npmjs.org") {
		t.Errorf("仍残留官方地址: %s", s)
	}
	// scoped 包名保留在 URL 中
	scoped := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.npmjs.org/@s/p/-/p-1.0.0.tgz"}}}}`
	out, _ = RewriteTarballs([]byte(scoped), "http://h:1", "@s/p")
	if !strings.Contains(string(out), `"tarball":"http://h:1/mirror/npm/@s/p/-/p-1.0.0.tgz"`) {
		t.Errorf("scoped 包重写错误: %s", out)
	}
}

func TestSRIDigestAndVerify(t *testing.T) {
	content := []byte("hello tarball\n")
	sum := sha512.Sum512(content)
	sri := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])

	dir := t.TempDir()
	path := filepath.Join(dir, "pkg-1.0.0.tgz")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySRIFile(path, sri); err != nil {
		t.Errorf("哈希一致应通过: %v", err)
	}
	bad := "sha512-" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 64)))
	if err := VerifySRIFile(path, bad); err == nil {
		t.Error("哈希不一致应报错")
	}
	// 不支持的算法跳过校验；不存在的文件报错
	if err := VerifySRIFile(path, "sha1-abc"); err != nil {
		t.Errorf("不支持算法应跳过校验: %v", err)
	}
	if err := VerifySRIFile(filepath.Join(dir, "none.tgz"), sri); err == nil {
		t.Error("文件不存在应报错")
	}
}

func TestFetchPackumentScoped(t *testing.T) {
	ctx := context.Background()
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`{"versions":{}}`))
	}))
	defer ts.Close()
	if _, err := FetchPackument(ctx, http.DefaultClient, ts.URL, "@scope/pkg"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/@scope%2Fpkg" {
		t.Errorf("scoped 包应请求 %%2F 编码路径, 得到 %s", gotPath)
	}
	// 404 给出明确提示
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts2.Close()
	if _, err := FetchPackument(ctx, http.DefaultClient, ts2.URL, "none"); err == nil || !strings.Contains(err.Error(), "不在 registry") {
		t.Errorf("404 应给出包不存在提示, 得到: %v", err)
	}
}

func TestEscapeName(t *testing.T) {
	if got := EscapeName("@scope/pkg"); got != "@scope%2Fpkg" {
		t.Errorf("scoped 编码错误: %s", got)
	}
	if got := EscapeName("lodash"); got != "lodash" {
		t.Errorf("普通包名不应变化: %s", got)
	}
}

func ExamplePackumentURL() {
	fmt.Println(PackumentURL("https://registry.npmjs.org/", "@scope/pkg"))
	// Output: https://registry.npmjs.org/@scope%2Fpkg
}

// TestDepNamesForTarballs 覆盖依赖并集（dependencies ∪ optionalDependencies）
// 及按已镜像 tarball 筛选版本。
func TestDepNamesForTarballs(t *testing.T) {
	body := []byte(`{"versions":{
		"1.0.0":{"dist":{"tarball":"https://registry.npmjs.org/a/-/a-1.0.0.tgz"},"dependencies":{"b":"^1.0.0"}},
		"2.0.0":{"dist":{"tarball":"https://registry.npmjs.org/a/-/a-2.0.0.tgz"},"dependencies":{"c":"^2.0.0"},"optionalDependencies":{"d":"^2.0.0"}},
		"3.0.0":{"dist":{"tarball":"https://registry.npmjs.org/a/-/a-3.0.0.tgz"},"dependencies":{"e":"^3.0.0"}}
	}}`)
	mirrored := map[string]bool{"a-1.0.0.tgz": true, "a-2.0.0.tgz": true}
	got, err := DepNamesForTarballs(body, func(f string) bool { return mirrored[f] })
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 3.0.0 未镜像：e 不计入；2.0.0 的 optional d 计入
	want := []string{"b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("依赖名 = %v, want %v", got, want)
	}
	// 全部版本未镜像 → 空
	if got, _ := DepNamesForTarballs(body, func(string) bool { return false }); len(got) != 0 {
		t.Errorf("无已镜像版本应返回空, 得到 %v", got)
	}
	// 非法 JSON → 错误
	if _, err := DepNamesForTarballs([]byte("{"), func(string) bool { return true }); err == nil {
		t.Error("非法 JSON 应报错")
	}
}
