package main

import (
	"bytes"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDownloadFlagAliases 验证短参数与长参数共享同一变量（pflag 一体注册）：
// 任一写法设置的值即为该选项最终值，长短混用时后出现的生效。
func TestDownloadFlagAliases(t *testing.T) {
	f := newDownloadFlags("pypi")
	// --python 及其短参数 -P 在 pypi 子命令注册，此处按同样方式注册以验证
	pythons := f.fs.StringP("python", "P", "", "")
	f.fs.Parse([]string{
		"-i", "http://idx", "--index-url", "http://idx2",
		"-R", "http://reg", "-p", "windows",
		"-g", "http://gh", "-x", "http://px", "-P", "3.12",
	})
	if *f.indexURL != "http://idx2" {
		t.Errorf("-i 与 --index-url 混用时应后写生效，实际 %s", *f.indexURL)
	}
	if *f.registry != "http://reg" {
		t.Errorf("-R 应设置 --registry 的值，实际 %s", *f.registry)
	}
	if *f.platforms != "windows" {
		t.Errorf("-p 应设置 --platform 的值，实际 %s", *f.platforms)
	}
	if *f.githubPrefix != "http://gh" {
		t.Errorf("-g 应设置 --github-prefix 的值，实际 %s", *f.githubPrefix)
	}
	if *f.proxy != "http://px" {
		t.Errorf("-x 应设置 --proxy 的值，实际 %s", *f.proxy)
	}
	if *pythons != "3.12" {
		t.Errorf("-P 应设置 --python 的值，实际 %s", *pythons)
	}
}

// TestDownloadFlagsInterspersed 验证选项写在位置参数前后均被解析（pflag
// interspersed 默认开启，替代原先的自写参数重排），位置参数顺序保留。
func TestDownloadFlagsInterspersed(t *testing.T) {
	f := newDownloadFlags("python")
	all := f.fs.Bool("all", false, "")
	f.fs.Parse([]string{"3.12", "-p", "linux", "--all", "3.13.1"})
	if *f.platforms != "linux" || !*all {
		t.Errorf("位置参数后的选项未被解析：platform=%s all=%v", *f.platforms, *all)
	}
	if args := f.fs.Args(); !reflect.DeepEqual(args, []string{"3.12", "3.13.1"}) {
		t.Errorf("位置参数应保持原顺序，实际 %v", args)
	}
}

// TestDownloadLogsRedactCredentials 验证下载启动日志隐藏源地址和代理凭据。
// 空包列表使下载在请求网络前退出，日志仍应保留主机和普通查询条件。
func TestDownloadLogsRedactCredentials(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	err := runDownloadNpm([]string{
		"--config", filepath.Join(t.TempDir(), "missing.toml"),
		"--registry", "https://alice:registry-pass@registry.example/?token=registry-token&os=linux",
		"--proxy", "http://bob:proxy-pass@proxy.example:8080?api_key=proxy-key",
	})
	if err == nil {
		t.Fatal("未指定包名应在请求网络前报错")
	}
	for _, secret := range []string{"alice", "registry-pass", "registry-token", "bob", "proxy-pass", "proxy-key"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("启动日志包含未隐藏的凭据 %q", secret)
		}
	}
	for _, want := range []string{"registry.example", "proxy.example:8080", "os=linux"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("启动日志缺少排障信息 %q", want)
		}
	}
}
