package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postDeleteJSON 以 JSON 请求体调用删除接口并返回解析后的响应。
func postDeleteJSON(t *testing.T, tsURL string, req deleteRequest) (deleteResult, *http.Response) {
	t.Helper()
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, tsURL+"/api/delete", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result deleteResult
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("解析响应失败: %v: %s", err, raw)
		}
	}
	return result, resp
}

// writeWheel 生成含 METADATA（Requires-Dist 逐行）的 wheel 文件。
func writeWheel(t *testing.T, path string, requires ...string) {
	t.Helper()
	meta := "Metadata-Version: 2.1\nName: x\n"
	for _, r := range requires {
		meta += "Requires-Dist: " + r + "\n"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("x-1.0.dist-info/METADATA")
	fw.Write([]byte(meta))
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSdist 生成含 PKG-INFO（Requires-Dist 逐行）的 sdist tar.gz 文件。
func writeSdist(t *testing.T, path string, requires ...string) {
	t.Helper()
	meta := "Metadata-Version: 2.1\nName: x\n"
	for _, r := range requires {
		meta += "Requires-Dist: " + r + "\n"
	}
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	tw.WriteHeader(&tar.Header{Name: "x-1.0/PKG-INFO", Mode: 0o644, Size: int64(len(meta))})
	tw.Write([]byte(meta))
	tw.Close()
	gzw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeNpmPkg 直接在数据目录写入一个 npm 包（packument + tarball）。
func writeNpmPkg(t *testing.T, root, name string, versions map[string][]string) {
	t.Helper()
	dir := filepath.Join(root, "npm", filepath.FromSlash(name))
	os.MkdirAll(dir, 0o755)
	vers := map[string]any{}
	for ver, deps := range versions {
		file := name[strings.LastIndex(name, "/")+1:] + "-" + ver + ".tgz"
		entry := map[string]any{"dist": map[string]string{"tarball": "https://registry.npmjs.org/" + name + "/-/" + file}}
		if len(deps) > 0 {
			depMap := map[string]string{}
			for _, d := range deps {
				depMap[d] = "^1.0.0"
			}
			entry["dependencies"] = depMap
		}
		vers[ver] = entry
		os.WriteFile(filepath.Join(dir, file), []byte("tgz-"+name+"-"+ver), 0o644)
	}
	pack, _ := json.Marshal(map[string]any{"versions": vers})
	os.WriteFile(filepath.Join(dir, "packument.json"), pack, 0o644)
}

// writeGoModule 直接在数据目录写入一个 Go 模块的 .mod 三件套。
func writeGoModule(t *testing.T, root, mod string, requires ...string) {
	t.Helper()
	dir := filepath.Join(root, "gomod", filepath.FromSlash(mod), "@v")
	os.MkdirAll(dir, 0o755)
	content := "module " + mod + "\n\ngo 1.21\n"
	if len(requires) > 0 {
		content += "\nrequire (\n"
		for _, r := range requires {
			content += "\t" + r + "\n"
		}
		content += ")\n"
	}
	os.WriteFile(filepath.Join(dir, "v1.0.0.mod"), []byte(content), 0o644)
	os.WriteFile(filepath.Join(dir, "v1.0.0.zip"), []byte("zip-"+mod), 0o644)
	os.WriteFile(filepath.Join(dir, "v1.0.0.info"), []byte(`{"Version":"v1.0.0"}`), 0o644)
}

// TestDeletePypiDryRunAndExecute 覆盖 dry-run 不落盘、执行删除与伴随
// .sha256 清理。
func TestDeletePypiDryRunAndExecute(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	dir := filepath.Join(root, "pypi", "requests")
	os.MkdirAll(dir, 0o755)
	writeWheel(t, filepath.Join(dir, "requests-2.0-py3-none-any.whl"), "idna>=2.5")
	os.WriteFile(filepath.Join(dir, "requests-2.0-py3-none-any.whl.sha256"), []byte("abc\n"), 0o644)

	// dry-run：返回清单但不删
	result, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "Requests", DryRun: true})
	if resp.StatusCode != http.StatusOK || result.Status != "preview" {
		t.Fatalf("dry-run 应 200/preview, 得到 %d/%s", resp.StatusCode, result.Status)
	}
	if len(result.Targets) != 1 || result.Targets[0].Name != "requests" {
		t.Fatalf("目标应为 requests: %+v", result.Targets)
	}
	if result.Count != 2 || result.Total == 0 { // wheel + .sha256
		t.Errorf("清单应含 2 个文件与非零大小: %+v", result)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dry-run 不应删除: %v", err)
	}

	// 执行：目录整删
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "requests"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200, 得到 %d", resp.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("包目录应已删除: %v", err)
	}
	// 重复删除 → 404
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "requests"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("重复删除应 404, 得到 %d", resp.StatusCode)
	}
}

// TestDeletePypiCascade 覆盖级联语义：仅被目标引用的传递依赖删除，
// 被幸存包引用的保留；元数据取 wheel METADATA 与 sdist PKG-INFO 并集。
func TestDeletePypiCascade(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	for _, name := range []string{"requests", "idna", "urllib3", "chardet", "other"} {
		os.MkdirAll(filepath.Join(root, "pypi", name), 0o755)
	}
	writeWheel(t, filepath.Join(root, "pypi", "requests", "requests-2.0-py3-none-any.whl"), "idna>=2.5", "urllib3<3")
	writeSdist(t, filepath.Join(root, "pypi", "requests", "requests-2.0.tar.gz"), "chardet>=3")
	writeWheel(t, filepath.Join(root, "pypi", "idna", "idna-3.0-py3-none-any.whl"))
	writeWheel(t, filepath.Join(root, "pypi", "urllib3", "urllib3-2.0-py3-none-any.whl"))
	writeWheel(t, filepath.Join(root, "pypi", "chardet", "chardet-5.0-py3-none-any.whl"), "idna>=2")
	writeWheel(t, filepath.Join(root, "pypi", "other", "other-1.0-py3-none-any.whl"), "urllib3<3")

	result, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "requests", Deps: true, DryRun: true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览失败: %d", resp.StatusCode)
	}
	var got []string
	for _, c := range result.Cascades {
		got = append(got, c.Name)
	}
	// chardet 仅 requests 引用 → 删；idna 虽被 chardet 引用但 chardet 同批
	// 删除，引用者消失 → 一并级联；urllib3 被幸存的 other 引用 → 保留
	want := []string{"chardet", "idna"}
	if !reflectEqual(got, want) {
		t.Fatalf("级联清单 = %v, want %v", got, want)
	}

	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "requests", Deps: true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除失败: %d", resp.StatusCode)
	}
	for _, gone := range []string{"requests", "chardet", "idna"} {
		if _, err := os.Stat(filepath.Join(root, "pypi", gone)); !os.IsNotExist(err) {
			t.Errorf("%s 应已删除", gone)
		}
	}
	for _, keep := range []string{"urllib3", "other"} {
		if _, err := os.Stat(filepath.Join(root, "pypi", keep)); err != nil {
			t.Errorf("%s 应保留: %v", keep, err)
		}
	}
}

// TestDeleteNpmCascade 覆盖 packument 依赖（含 optional）与 scoped 包
// 删除后的 @scope 目录清理。
func TestDeleteNpmCascade(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	// a 依赖 b 与 optional c；d 也依赖 b；c 只被 a 引用
	writeNpmPkg(t, root, "a", map[string][]string{"1.0.0": {"b"}})
	{
		// 给 a 的 packument 追加 optional 依赖 c
		dir := filepath.Join(root, "npm", "a")
		body, _ := os.ReadFile(filepath.Join(dir, "packument.json"))
		var doc map[string]any
		json.Unmarshal(body, &doc)
		doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["optionalDependencies"] = map[string]string{"c": "^1.0.0"}
		out, _ := json.Marshal(doc)
		os.WriteFile(filepath.Join(dir, "packument.json"), out, 0o644)
	}
	writeNpmPkg(t, root, "b", map[string][]string{"1.0.0": nil})
	writeNpmPkg(t, root, "c", map[string][]string{"1.0.0": nil})
	writeNpmPkg(t, root, "d", map[string][]string{"1.0.0": {"b"}})
	writeNpmPkg(t, root, "@scope/e", map[string][]string{"1.0.0": nil})

	result, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "npm", Name: "a", Deps: true, DryRun: true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览失败: %d", resp.StatusCode)
	}
	var got []string
	for _, c := range result.Cascades {
		got = append(got, c.Name)
	}
	if !reflectEqual(got, []string{"c"}) {
		t.Fatalf("级联清单 = %v, want [c]", got)
	}
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "npm", Name: "a", Deps: true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除失败: %d", resp.StatusCode)
	}

	// scoped 包删除后 @scope 目录一并清理
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "npm", Name: "@scope/e"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("scoped 删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "npm", "@scope")); !os.IsNotExist(err) {
		t.Errorf("@scope 空目录应清理: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "npm", "b")); err != nil {
		t.Errorf("b 应保留: %v", err)
	}
}

// TestDeleteGomodCascade 覆盖 .mod require 建图、级联与模块路径父目录清理。
func TestDeleteGomodCascade(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	writeGoModule(t, root, "example.com/app", "example.com/only v1.0.0", "example.com/shared v1.0.0")
	writeGoModule(t, root, "example.com/only")
	writeGoModule(t, root, "example.com/shared")
	writeGoModule(t, root, "example.com/other/pkg", "example.com/shared v1.0.0")

	result, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "gomod", Name: "example.com/app", Deps: true, DryRun: true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览失败: %d", resp.StatusCode)
	}
	var got []string
	for _, c := range result.Cascades {
		got = append(got, c.Name)
	}
	if !reflectEqual(got, []string{"example.com/only"}) {
		t.Fatalf("级联清单 = %v, want [example.com/only]", got)
	}
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "gomod", Name: "example.com/app", Deps: true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "gomod", "example.com", "only")); !os.IsNotExist(err) {
		t.Error("example.com/only 应已删除")
	}
	if _, err := os.Stat(filepath.Join(root, "gomod", "example.com", "shared")); err != nil {
		t.Errorf("shared 应保留: %v", err)
	}

	// 删除嵌套模块后父链清理：删掉 example.com/other/pkg 后 example.com/other
	// 变空，应被清理；example.com 下仍有 shared，保留
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "gomod", Name: "example.com/other/pkg"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除 other/pkg 失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "gomod", "example.com", "other")); !os.IsNotExist(err) {
		t.Error("空的父目录 example.com/other 应清理")
	}
	if _, err := os.Stat(filepath.Join(root, "gomod", "example.com", "shared", "@v")); err != nil {
		t.Errorf("example.com/shared 应保留: %v", err)
	}
}

// TestDeleteInstallers 覆盖三类安装包的版本组删除：python 跨 tag 匹配
// 与空目录清理、node 版本目录、golang 版本文件组（含 go 前缀兼容与
// X.Y 前缀匹配）。
func TestDeleteInstallers(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	// python：20241002 只含 3.12（删后整目录清理），20250317 含 3.12 与
	// 3.13（删 3.12 后保留 3.13 与目录）
	tagFiles := map[string][]string{
		"20241002": {"3.12.1"},
		"20250317": {"3.12.1", "3.13.0"},
	}
	for tag, vers := range tagFiles {
		dir := filepath.Join(root, "python", tag)
		os.MkdirAll(dir, 0o755)
		for _, ver := range vers {
			name := fmt.Sprintf("cpython-%s+%s-x86_64-unknown-linux-gnu-install_only.tar.gz", ver, tag)
			os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644)
			os.WriteFile(filepath.Join(dir, name+".sha256"), []byte("sum"), 0o644)
		}
	}
	// node：版本目录
	nodeDir := filepath.Join(root, "node", "v22.14.0")
	os.MkdirAll(nodeDir, 0o755)
	os.WriteFile(filepath.Join(nodeDir, "node-v22.14.0-linux-x64.tar.xz"), []byte("xz"), 0o644)
	os.WriteFile(filepath.Join(nodeDir, "SHASUMS256.txt"), []byte("sha"), 0o644)
	// golang：flat 文件 + 伴随 .sha256
	for _, f := range []string{"go1.23.4.linux-amd64.tar.gz", "go1.23.4.linux-amd64.tar.gz.sha256", "go1.22.0.linux-amd64.tar.gz"} {
		os.WriteFile(filepath.Join(root, "golang", f), []byte("gz"), 0o644)
	}

	// python 3.12 前缀：跨 tag 删除该系列全部文件与 .sha256，3.13 保留，
	// 20241002 目录因此变空被清理（20250317 仍含 3.13 文件保留）
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: "3.12"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("python 删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "python", "20241002")); !os.IsNotExist(err) {
		t.Error("变空的 tag 目录应清理")
	}
	left, _ := os.ReadDir(filepath.Join(root, "python", "20250317"))
	if len(left) != 2 { // 3.13 主文件 + .sha256
		t.Errorf("3.13 文件应保留（2 个），剩 %d 个", len(left))
	}

	// node v22 精确目录删除
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "node", Name: "v22.14.0"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("node 删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(nodeDir); !os.IsNotExist(err) {
		t.Error("node 版本目录应删除")
	}

	// golang go 前缀兼容（页面按钮传 go1.23.4 形式）
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "golang", Name: "go1.23.4"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("golang 删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "golang", "go1.23.4.linux-amd64.tar.gz")); !os.IsNotExist(err) {
		t.Error("go1.23.4 归档应删除")
	}
	// golang X.Y 前缀匹配
	if _, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "golang", Name: "1.22"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("golang 前缀删除失败: %d", resp.StatusCode)
	}
	leftGo, _ := os.ReadDir(filepath.Join(root, "golang"))
	if len(leftGo) != 0 {
		t.Errorf("golang 目录应已清空, 剩 %d 项", len(leftGo))
	}
}

// TestDeleteValidation 覆盖方法、类型、--deps 适用范围与非法参数。
func TestDeleteValidation(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	os.MkdirAll(filepath.Join(root, "pypi", "six"), 0o755)
	os.WriteFile(filepath.Join(root, "pypi", "six", "six-1.0-py3-none-any.whl"), []byte("w"), 0o644)

	resp, err := http.Get(ts.URL + "/api/delete")
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405, 得到 %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 安装包不支持 --deps
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: "3.12", Deps: true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("python + deps 应 400, 得到 %d", resp.StatusCode)
	}
	// 未知类型
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "cargo", Name: "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("未知类型应 400, 得到 %d", resp.StatusCode)
	}
	// 非法 pypi 包名（路径穿越形式）
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "../etc"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法包名应 400, 得到 %d", resp.StatusCode)
	}
	// 非法版本参数
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "golang", Name: "abc"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法版本应 400, 得到 %d", resp.StatusCode)
	}
	// 目标不存在
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "pypi", Name: "nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("缺失目标应 404, 得到 %d", resp.StatusCode)
	}
	// 非法 JSON
	hr, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/delete", strings.NewReader("{"))
	hr.Header.Set("Content-Type", "application/json")
	r2, _ := http.DefaultClient.Do(hr)
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("非法 JSON 应 400, 得到 %d", r2.StatusCode)
	}
}

// TestDeleteFormRedirect 验证删除后 303 返回对应类型列表并携带 msg。
func TestDeleteFormRedirect(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	dir := filepath.Join(root, "pypi", "six")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "six-1.0-py3-none-any.whl"), []byte("w"), 0o644)

	form := url.Values{"type": {"pypi"}, "name": {"six"}}
	hr, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/delete", strings.NewReader(form.Encode()))
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 模拟从预览页提交：浏览器地址栏停在 /api/delete，Referer 即本接口
	hr.Header.Set("Referer", ts.URL+"/api/delete")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("表单删除应 303, 得到 %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	// 回跳固定为该类型列表页（不信任 Referer），http.Redirect 可能补全
	// 为绝对 URL，按包含关系断言
	if !strings.Contains(loc, "/browse/?msg=") {
		t.Errorf("应重定向回 /browse/ 带 msg, 得到 %q", loc)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("表单删除应已落盘删除")
	}
	// 列表页显示结果提示（请求重定向目标，msg 经 Query 解码回中文）；
	// POST 303 的 Location 是相对路径（浏览器合法处理），测试侧拼基准地址
	target := loc
	if !strings.HasPrefix(target, "http") {
		target = ts.URL + target
	}
	page, _ := http.Get(target)
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), "已删除") {
		t.Error("列表页应显示删除结果提示")
	}
}

// TestDeletePageButtons 验证六类列表页均渲染删除按钮（空页面无表格，
// 因此每类都造一条数据）。
func TestDeletePageButtons(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	os.MkdirAll(filepath.Join(root, "pypi", "six"), 0o755)
	os.WriteFile(filepath.Join(root, "pypi", "six", "six-1.0-py3-none-any.whl"), []byte("w"), 0o644)
	pyDir := filepath.Join(root, "python", "20250317")
	os.MkdirAll(pyDir, 0o755)
	os.WriteFile(filepath.Join(pyDir, "cpython-3.12.1+20250317-x86_64-unknown-linux-gnu-install_only.tar.gz"), []byte("d"), 0o644)
	writeNpmPkg(t, root, "lodash", map[string][]string{"4.17.21": nil})
	os.MkdirAll(filepath.Join(root, "node", "v22.14.0"), 0o755)
	os.WriteFile(filepath.Join(root, "node", "v22.14.0", "node-v22.14.0-linux-x64.tar.xz"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "node", "v22.14.0", "SHASUMS256.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(root, "golang", "go1.23.4.linux-amd64.tar.gz"), []byte("g"), 0o644)
	writeGoModule(t, root, "github.com/spf13/cobra")

	for _, page := range []string{"/browse/", "/python/", "/npm/", "/node/", "/golang/", "/browse/gomod/"} {
		resp, err := http.Get(ts.URL + page)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "/api/delete") {
			t.Errorf("%s 应含删除按钮", page)
		}
		if !strings.Contains(string(body), "delbtn") {
			t.Errorf("%s 应含删除按钮样式类", page)
		}
	}
}

// reflectEqual 简化 nil 与空切片的对比。
func reflectEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// postDeleteForm 以 urlencoded 表单调用删除接口（网页路径），不跟随重定向。
func postDeleteForm(t *testing.T, tsURL string, vals url.Values) *http.Response {
	t.Helper()
	hr, _ := http.NewRequest(http.MethodPost, tsURL+"/api/delete", strings.NewReader(vals.Encode()))
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestDeletePreviewPage 覆盖表单 dryRun 路径：渲染预览页（含目标、级联
// 清单与两个执行按钮），不删除文件。
func TestDeletePreviewPage(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	for _, name := range []string{"requests", "idna"} {
		os.MkdirAll(filepath.Join(root, "pypi", name), 0o755)
	}
	writeWheel(t, filepath.Join(root, "pypi", "requests", "requests-2.0-py3-none-any.whl"), "idna>=2.5")
	writeWheel(t, filepath.Join(root, "pypi", "idna", "idna-3.0-py3-none-any.whl"))

	resp := postDeleteForm(t, ts.URL, url.Values{"type": {"pypi"}, "name": {"requests"}, "dryRun": {"1"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预览应 200, 得到 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// 级联含 idna 的 wheel 与建图时生成的 .sha256 缓存（fileSHA256 惰性写回）；
	// 取消为标题旁按钮，执行按钮在两个清单区块的标题行内
	for _, want := range []string{
		"删除预览", "requests", "idna",
		`<h1>删除预览 <a class="cancelbtn"`,
		"直接删除（1 个文件）", "级联删除（3 个文件）",
		`<h2>删除目标 <button type="submit" class="delmain">直接删除（1 个文件）</button></h2>`,
		`name="deps" value="1"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("预览页缺 %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pypi", "requests")); err != nil {
		t.Fatalf("预览不应删除: %v", err)
	}

	// 目标不存在 → 错误页（非预览页）
	resp = postDeleteForm(t, ts.URL, url.Values{"type": {"pypi"}, "name": {"nope"}, "dryRun": {"1"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("缺失目标应 404, 得到 %d", resp.StatusCode)
	}
}

// TestDeletePreviewExecute 覆盖预览页两个执行按钮：deps=1 级联删除，
// 不带 deps 只删自己。
func TestDeletePreviewExecute(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	for _, name := range []string{"requests", "idna", "urllib3"} {
		os.MkdirAll(filepath.Join(root, "pypi", name), 0o755)
	}
	writeWheel(t, filepath.Join(root, "pypi", "requests", "requests-2.0-py3-none-any.whl"), "idna>=2.5", "urllib3<3")
	writeWheel(t, filepath.Join(root, "pypi", "idna", "idna-3.0-py3-none-any.whl"))
	writeWheel(t, filepath.Join(root, "pypi", "urllib3", "urllib3-2.0-py3-none-any.whl"))

	// 只删自己（不带 deps）：requests 删，idna/urllib3 保留
	resp := postDeleteForm(t, ts.URL, url.Values{"type": {"pypi"}, "name": {"requests"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("只删自己应 303, 得到 %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "pypi", "idna")); err != nil {
		t.Fatalf("只删自己不应删依赖: %v", err)
	}

	// 重新造目标后走级联按钮（deps=1）：目标与独占依赖一并删
	os.MkdirAll(filepath.Join(root, "pypi", "requests"), 0o755)
	writeWheel(t, filepath.Join(root, "pypi", "requests", "requests-2.0-py3-none-any.whl"), "idna>=2.5")
	resp = postDeleteForm(t, ts.URL, url.Values{"type": {"pypi"}, "name": {"requests"}, "deps": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("级联删除应 303, 得到 %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "pypi", "idna")); !os.IsNotExist(err) {
		t.Error("级联按钮应删除独占依赖 idna")
	}
	if _, err := os.Stat(filepath.Join(root, "pypi", "urllib3")); err != nil {
		t.Errorf("urllib3 不应受影响: %v", err)
	}
}

// TestDeleteListEntryField 验证六类列表页按钮均走 dryRun 预览入口，
// 依赖型传包名、安装包传行级归档文件名。
func TestDeleteListEntryField(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	os.MkdirAll(filepath.Join(root, "pypi", "six"), 0o755)
	os.WriteFile(filepath.Join(root, "pypi", "six", "six-1.0-py3-none-any.whl"), []byte("w"), 0o644)
	writeNpmPkg(t, root, "lodash", map[string][]string{"4.17.21": nil})
	writeGoModule(t, root, "github.com/spf13/cobra")
	pyDir := filepath.Join(root, "python", "20250317")
	os.MkdirAll(pyDir, 0o755)
	os.WriteFile(filepath.Join(pyDir, "cpython-3.12.1+20250317-x86_64-unknown-linux-gnu-install_only.tar.gz"), []byte("d"), 0o644)
	nodeDir := filepath.Join(root, "node", "v22.14.0")
	os.MkdirAll(nodeDir, 0o755)
	os.WriteFile(filepath.Join(nodeDir, "node-v22.14.0-linux-x64.tar.xz"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(nodeDir, "SHASUMS256.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(root, "golang", "go1.23.4.linux-amd64.tar.gz"), []byte("g"), 0o644)

	for _, page := range []string{"/browse/", "/npm/", "/browse/gomod/"} {
		resp, _ := http.Get(ts.URL + page)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), `name="dryRun" value="1"`) {
			t.Errorf("%s 删除按钮应走 dryRun 预览入口", page)
		}
	}
	// 安装包三类也走 dryRun 预览入口，目标为行级归档文件名（value 为
	// 实际文件名而非版本）
	for _, page := range []string{"/python/", "/node/", "/golang/"} {
		resp, _ := http.Get(ts.URL + page)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), `name="dryRun" value="1"`) {
			t.Errorf("%s 删除按钮应走 dryRun 预览入口", page)
		}
	}
	if page, _ := http.Get(ts.URL + "/python/"); true {
		body, _ := io.ReadAll(page.Body)
		page.Body.Close()
		// html/template 把文件名中的 + 转义为 &#43;（浏览器提交时自动还原）
		if !strings.Contains(string(body), `value="cpython-3.12.1&#43;20250317-x86_64-unknown-linux-gnu-install_only.tar.gz"`) {
			t.Error("python 页预览目标应为行级文件名")
		}
	}
	// node 页 SHASUMS256.txt 行不渲染按钮：dryRun 表单只出现一次
	if page, _ := http.Get(ts.URL + "/node/"); true {
		body, _ := io.ReadAll(page.Body)
		page.Body.Close()
		if n := strings.Count(string(body), `name="dryRun"`); n != 1 {
			t.Errorf("node 页删除按钮应为 1 个（SHASUMS256.txt 行排除）, 得到 %d", n)
		}
	}
}

// TestDeleteInstallerFileModes 覆盖安装包文件名形态：单文件删除（含伴随
// .sha256 与 node SHASUMS 共享规则）与 --deps 扩展为整版本组。
func TestDeleteInstallerFileModes(t *testing.T) {
	_, ts, root := newTestServer(t, nil)
	// python：同版本两个平台文件 + 伴随 .sha256
	pyDir := filepath.Join(root, "python", "20250317")
	os.MkdirAll(pyDir, 0o755)
	pyMain := "cpython-3.12.1+20250317-x86_64-unknown-linux-gnu-install_only.tar.gz"
	pyOther := "cpython-3.12.1+20250317-aarch64-apple-darwin-install_only.tar.gz"
	os.WriteFile(filepath.Join(pyDir, pyMain), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(pyDir, pyMain+".sha256"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(pyDir, pyOther), []byte("b"), 0o644)

	// 单删主文件：主文件 + .sha256 删，另一平台保留
	result, resp := postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: pyMain, DryRun: true})
	if resp.StatusCode != http.StatusOK || len(result.Targets) != 1 || len(result.Targets[0].Files) != 2 {
		t.Fatalf("python 单文件预览失败: %d %+v", resp.StatusCode, result.Targets)
	}
	// --deps 扩展为整组：差集含另一平台
	result, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: pyMain, Deps: true, DryRun: true})
	if resp.StatusCode != http.StatusOK || len(result.Cascades) != 1 || len(result.Cascades[0].Files) != 1 {
		t.Fatalf("python 整组预览失败: %d cascades=%+v", resp.StatusCode, result.Cascades)
	}
	if _, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: pyMain}); resp.StatusCode != http.StatusOK {
		t.Fatalf("python 单删失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(pyDir, pyMain)); !os.IsNotExist(err) {
		t.Error("主文件应删除")
	}
	if _, err := os.Stat(filepath.Join(pyDir, pyOther)); err != nil {
		t.Errorf("另一平台文件应保留: %v", err)
	}

	// node：两归档 + SHASUMS；删一个归档时 SHASUMS 保留
	nodeDir := filepath.Join(root, "node", "v22.14.0")
	os.MkdirAll(nodeDir, 0o755)
	nMain := "node-v22.14.0-linux-x64.tar.xz"
	nOther := "node-v22.14.0-darwin-arm64.tar.xz"
	os.WriteFile(filepath.Join(nodeDir, nMain), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(nodeDir, nOther), []byte("y"), 0o644)
	os.WriteFile(filepath.Join(nodeDir, "SHASUMS256.txt"), []byte("s"), 0o644)
	if _, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "node", Name: nMain}); resp.StatusCode != http.StatusOK {
		t.Fatalf("node 单删失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, "SHASUMS256.txt")); err != nil {
		t.Errorf("还有其他归档时 SHASUMS256.txt 应保留: %v", err)
	}
	// 删最后一个归档：SHASUMS256.txt 一并删除，空目录清理
	if _, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "node", Name: nOther}); resp.StatusCode != http.StatusOK {
		t.Fatalf("node 末归档删除失败: %d", resp.StatusCode)
	}
	if _, err := os.Stat(nodeDir); !os.IsNotExist(err) {
		t.Error("归档清零后 SHASUMS256.txt 与空目录应一并删除")
	}

	// golang：单归档 + .sha256，--deps 扩展整组
	os.WriteFile(filepath.Join(root, "golang", "go1.23.4.linux-amd64.tar.gz"), []byte("g1"), 0o644)
	os.WriteFile(filepath.Join(root, "golang", "go1.23.4.linux-amd64.tar.gz.sha256"), []byte("s1"), 0o644)
	os.WriteFile(filepath.Join(root, "golang", "go1.23.4.src.tar.gz"), []byte("g2"), 0o644)
	result, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "golang", Name: "go1.23.4.linux-amd64.tar.gz", Deps: true, DryRun: true})
	if resp.StatusCode != http.StatusOK || len(result.Targets) != 1 || len(result.Targets[0].Files) != 2 || len(result.Cascades[0].Files) != 1 {
		t.Fatalf("golang 整组预览失败: %d %+v", resp.StatusCode, result)
	}
	if _, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "golang", Name: "go1.23.4.linux-amd64.tar.gz", Deps: true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("golang 整组删除失败: %d", resp.StatusCode)
	}
	left, _ := os.ReadDir(filepath.Join(root, "golang"))
	if len(left) != 0 {
		t.Errorf("golang 整组应全删, 剩 %d 项", len(left))
	}

	// SHASUMS256.txt 不可作为删除目标；按版本删除时携带 deps 报错
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "node", Name: "SHASUMS256.txt"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("SHASUMS256.txt 应拒绝删除, 得到 %d", resp.StatusCode)
	}
	_, resp = postDeleteJSON(t, ts.URL, deleteRequest{Type: "python", Name: "3.12", Deps: true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("按版本删除时携带 deps 应 400, 得到 %d", resp.StatusCode)
	}
}
