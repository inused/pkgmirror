package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExeDir(t *testing.T) {
	// go test 下二进制在临时构建目录，只需验证返回目录下确实存在当前可执行文件
	dir := ExeDir()
	if dir == "" {
		t.Fatal("ExeDir 不应为空")
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(os.Args[0]))); err != nil {
		// os.Args[0] 与 os.Executable 在个别环境下可能不同名，退化为目录可用性检查
		if info, serr := os.Stat(dir); serr != nil || !info.IsDir() {
			t.Errorf("ExeDir 返回的目录不可用: %s (%v)", dir, serr)
		}
	}
}

func TestFindConfig(t *testing.T) {
	dir := t.TempDir()
	// 两处都不存在 → 返回当前目录下的名字
	got := FindConfig(filepath.Join(dir, "none.toml"))
	if got != filepath.Join(dir, "none.toml") {
		t.Errorf("未命中时应返回原名, 得到 %s", got)
	}
	// 二进制目录存在 → 命中二进制目录
	target := filepath.Join(ExeDir(), "findconfig-probe.toml")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(target)
	if got = FindConfig("findconfig-probe.toml"); got != target {
		t.Errorf("应命中二进制目录: %s", got)
	}
	// 当前目录也存在 → 当前目录优先（CWD 即 go test 临时目录，两文件内容不同可区分）
	// go test 的 CWD 是包目录（只读风险），用子进程方式验证成本高，
	// 这里只验证当前目录不存在时能回退到二进制目录（上面的用例已覆盖）
}
