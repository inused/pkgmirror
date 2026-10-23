package main

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

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
