package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeUserConfigEnv 设置用户级配置目录的环境变量并返回目录路径。
func fakeUserConfigEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	return dir
}

func userConfigFilePath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "netgw", "config.yaml")
	}
	return filepath.Join(dir, "netgw", "config.yaml")
}

func TestResolvePriority(t *testing.T) {
	// 显式路径优先。
	if p, src := Resolve("/tmp/foo.yaml"); p != "/tmp/foo.yaml" || src != "command line" {
		t.Errorf("explicit: %q %q", p, src)
	}
}

func TestUserConfigPath(t *testing.T) {
	dir := fakeUserConfigEnv(t)
	want := filepath.Join(dir, ConfigDirName, ConfigFileName)
	if got := UserConfigPath(); got != want {
		t.Errorf("UserConfigPath = %q, want %q", got, want)
	}
}

func TestResolveUserOverSystem(t *testing.T) {
	dir := fakeUserConfigEnv(t)
	user := userConfigFilePath(dir)
	if err := os.MkdirAll(filepath.Dir(user), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, src := Resolve("")
	if src != "user config" || p != user {
		t.Errorf("got %q %q, want user config", src, p)
	}
}

func TestResolveSkipsMissingUser(t *testing.T) {
	// 用户级不存在时不应误判。
	fakeUserConfigEnv(t)
	// Windows 无系统级配置，会落到 local 或默认；Unix 会落到 system。
	_, src := Resolve("")
	if src == "user config" {
		t.Errorf("should not report user config when file missing")
	}
}

func TestResolveLocal(t *testing.T) {
	dir := t.TempDir()
	fakeUserConfigEnv(t)
	wd, _ := os.Getwd()
	wdPath, _ := filepath.Abs(wd)
	defer os.Chdir(wdPath)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.yaml", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "config.yaml")
	p, src := Resolve("")
	if src != "local config.yaml" {
		t.Errorf("source = %q", src)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	if abs != want {
		t.Errorf("got %q %q", src, p)
	}
}

func TestEnsureUserConfig(t *testing.T) {
	dir := fakeUserConfigEnv(t)
	cfgPath := userConfigFilePath(dir)
	created, err := EnsureUserConfig([]byte("server:\n  listen: \":9000\"\n"))
	if err != nil || !created {
		t.Fatalf("first run: created=%v err=%v", created, err)
	}
	// 第二次运行不覆盖。
	if err := os.WriteFile(cfgPath, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureUserConfig([]byte("should-not-overwrite"))
	if err != nil || created {
		t.Fatalf("second run: created=%v err=%v", created, err)
	}
	data, _ := os.ReadFile(cfgPath)
	if string(data) != "custom" {
		t.Errorf("config overwritten: %q", data)
	}
}

func TestLoadAutoParseError(t *testing.T) {
	// 显式路径不存在必须报错，不得静默回退。
	if _, _, err := LoadAuto(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing explicit config")
	}
	// 配置存在但语法非法必须报错。
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("server: [1,2,\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAuto(bad); err == nil {
		t.Fatal("expected parse error")
	}
}
