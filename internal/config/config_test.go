package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testListener(addr string) ListenerConfig {
	return ListenerConfig{Addr: addr, Backend: "127.0.0.1:8080"}
}

func TestValidateListeners(t *testing.T) {
	// 合法：多端口，各自独立 backend / TLS / 路由。
	cfg := Defaults()
	cfg.Server.Listeners = []ListenerConfig{
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:8080"},
		{Addr: "0.0.0.0:8443", Backend: "127.0.0.1:9090",
			TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
			Routes: map[string]HTTPRoute{"/api": {Backend: "127.0.0.1:9091"}}},
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("valid listeners rejected: %v", err)
	}
	if !cfg.Server.Listeners[1].TLSEnabled() || cfg.Server.Listeners[0].TLSEnabled() {
		t.Error("TLS should be per-port")
	}

	// 非法：重复地址。
	dup := Defaults()
	dup.Server.Listeners = []ListenerConfig{
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:1"},
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:2"},
	}
	if err := dup.validate(); err == nil {
		t.Error("duplicate listener address should be rejected")
	}

	// 非法：缺 backend。
	nb := Defaults()
	nb.Server.Listeners = []ListenerConfig{{Addr: "0.0.0.0:8000"}}
	if err := nb.validate(); err == nil {
		t.Error("listener without backend should be rejected")
	}

	// 非法：TLS 证书与私钥必须成对。
	badTLS := Defaults()
	badTLS.Server.Listeners = []ListenerConfig{
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:1", TLSCertFile: "cert.pem"},
	}
	if err := badTLS.validate(); err == nil {
		t.Error("tls_cert without tls_key should be rejected")
	}

	// 非法：路由前缀不以 / 开头。
	badPrefix := Defaults()
	badPrefix.Server.Listeners = []ListenerConfig{
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:1",
			Routes: map[string]HTTPRoute{"api": {Backend: "127.0.0.1:2"}}},
	}
	if err := badPrefix.validate(); err == nil {
		t.Error("route prefix without '/' should be rejected")
	}

	// 非法：路由缺 backend。
	badRoute := Defaults()
	badRoute.Server.Listeners = []ListenerConfig{
		{Addr: "0.0.0.0:8000", Backend: "127.0.0.1:1",
			Routes: map[string]HTTPRoute{"/api": {}}},
	}
	if err := badRoute.validate(); err == nil {
		t.Error("route with empty backend should be rejected")
	}

	// 非法：空 listeners。
	empty := Defaults()
	empty.Server.Listeners = nil
	if err := empty.validate(); err == nil {
		t.Error("empty listeners should be rejected")
	}
}

func TestLoadListeners(t *testing.T) {
	content := `
read_header_timeout: 3s
listeners:
  - addr: "0.0.0.0:8000"
    backend: "127.0.0.1:8080"
    routes:
      "/api": "127.0.0.1:9091"
      "/web":
        backend: "127.0.0.1:9092"
        strip_prefix: true
  - addr: "0.0.0.0:8443"
    backend: "127.0.0.1:9090"
    read_header_timeout: 5s
    tls_cert: "cert.pem"
    tls_key: "key.pem"

http:
  connect_timeout: 2s

logging:
  level: "debug"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Server.Listeners) != 2 {
		t.Fatalf("len(listeners) = %d, want 2", len(cfg.Server.Listeners))
	}
	l0 := cfg.Server.Listeners[0]
	if l0.Addr != "0.0.0.0:8000" || l0.Backend != "127.0.0.1:8080" {
		t.Errorf("listener[0] = %+v", l0)
	}
	// 端口未配置 read_header_timeout 时保持零值（运行时用全局默认）。
	if l0.ReadHeaderTimeout != 0 {
		t.Errorf("listener[0].read_header_timeout = %v, want 0", l0.ReadHeaderTimeout)
	}
	if len(l0.Routes) != 2 {
		t.Fatalf("listener[0].routes = %+v", l0.Routes)
	}
	if l0.Routes["/web"] != (HTTPRoute{Backend: "127.0.0.1:9092", StripPrefix: true}) {
		t.Errorf("listener[0].routes[/web] = %+v", l0.Routes["/web"])
	}
	l1 := cfg.Server.Listeners[1]
	if l1.Backend != "127.0.0.1:9090" || !l1.TLSEnabled() {
		t.Errorf("listener[1] = %+v", l1)
	}
	if l1.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("listener[1].read_header_timeout = %v", l1.ReadHeaderTimeout)
	}
	if cfg.Server.ReadHeaderTimeout != 3*time.Second {
		t.Errorf("global read_header_timeout = %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.HTTP.ConnectTimeout != 2*time.Second {
		t.Errorf("connect_timeout = %v", cfg.HTTP.ConnectTimeout)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("level = %q", cfg.Logging.Level)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if len(cfg.Server.Listeners) != 1 {
		t.Fatalf("default listeners = %d, want 1", len(cfg.Server.Listeners))
	}
	l := cfg.Server.Listeners[0]
	if l.Addr != "0.0.0.0:7822" || l.Backend != "127.0.0.1:8080" {
		t.Errorf("default listener = %+v", l)
	}
	if l.TLSEnabled() {
		t.Error("tls should be disabled by default")
	}
	if cfg.Server.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("read_header_timeout = %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.HTTP.ConnectTimeout != 5*time.Second {
		t.Errorf("connect_timeout = %v", cfg.HTTP.ConnectTimeout)
	}
}

func TestLoadFromFile(t *testing.T) {
	content := `
# 网关配置示例
listeners:
  - addr: "0.0.0.0:30000"
    backend: "127.0.0.1:9000"
    read_header_timeout: 3s
    tls_cert: "cert.pem"
    tls_key: "key.pem"
    routes:
      "/api": "127.0.0.1:9090"
      "/web": "127.0.0.1:9091"
      "/static":
        backend: "127.0.0.1:9092"
        strip_prefix: true

http:
  connect_timeout: 2s

logging:
  level: "debug"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Server.Listeners) != 1 {
		t.Fatalf("len(listeners) = %d", len(cfg.Server.Listeners))
	}
	l := cfg.Server.Listeners[0]
	if l.Addr != "0.0.0.0:30000" {
		t.Errorf("addr = %q", l.Addr)
	}
	if l.ReadHeaderTimeout != 3*time.Second {
		t.Errorf("read_header_timeout = %v", l.ReadHeaderTimeout)
	}
	if !l.TLSEnabled() {
		t.Error("tls should be enabled")
	}
	if l.TLSCertFile != "cert.pem" || l.TLSKeyFile != "key.pem" {
		t.Errorf("tls files = %q / %q", l.TLSCertFile, l.TLSKeyFile)
	}
	if cfg.HTTP.ConnectTimeout != 2*time.Second {
		t.Errorf("connect_timeout = %v", cfg.HTTP.ConnectTimeout)
	}
	if l.Backend != "127.0.0.1:9000" {
		t.Errorf("backend = %q", l.Backend)
	}
	if l.Routes["/api"] != (HTTPRoute{Backend: "127.0.0.1:9090"}) {
		t.Errorf("routes[/api] = %+v", l.Routes["/api"])
	}
	if l.Routes["/web"] != (HTTPRoute{Backend: "127.0.0.1:9091"}) {
		t.Errorf("routes[/web] = %+v", l.Routes["/web"])
	}
	if l.Routes["/static"] != (HTTPRoute{Backend: "127.0.0.1:9092", StripPrefix: true}) {
		t.Errorf("routes[/static] = %+v", l.Routes["/static"])
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("level = %q", cfg.Logging.Level)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	content := `
listeners:
  - addr: "127.0.0.1:12345"
    backend: "127.0.0.1:1"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Server.Listeners) != 1 || cfg.Server.Listeners[0].Addr != "127.0.0.1:12345" {
		t.Fatalf("listeners = %+v", cfg.Server.Listeners)
	}
	// 未覆盖的字段保持默认值。
	if cfg.Server.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("read_header_timeout = %v, want default", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.HTTP.ConnectTimeout != 5*time.Second {
		t.Errorf("connect_timeout = %v, want default", cfg.HTTP.ConnectTimeout)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"bad duration", "read_header_timeout: five\n"},
		{"bad level", "logging:\n  level: verbose\n"},
		{"empty listeners", "listeners: []\n"},
		{"listener missing addr", "listeners:\n  - backend: \"1.2.3.4:80\"\n"},
		{"listener missing backend", "listeners:\n  - addr: \"0.0.0.0:8000\"\n"},
		{"duplicate addr", "listeners:\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:80\"\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:81\"\n"},
		{"tls cert without key", "listeners:\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:80\"\n    tls_cert: \"cert.pem\"\n"},
		{"tls key without cert", "listeners:\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:80\"\n    tls_key: \"key.pem\"\n"},
		{"route prefix without slash", "listeners:\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:80\"\n    routes:\n      \"api\": \"1.2.3.4:81\"\n"},
		{"route empty backend", "listeners:\n  - addr: \"0.0.0.0:8000\"\n    backend: \"1.2.3.4:80\"\n    routes:\n      \"/api\": \"\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseYAMLStructure(t *testing.T) {
	root, err := parseYAML([]byte("a:\n  b: \"1\"\n  c: 2\nd: 3\n"))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	a, ok := root["a"].(map[string]any)
	if !ok {
		t.Fatalf("a should be a map, got %T", root["a"])
	}
	if a["b"] != "1" {
		t.Errorf("a.b = %v", a["b"])
	}
	if a["c"] != "2" {
		t.Errorf("a.c = %v (scalars are strings)", a["c"])
	}
	if root["d"] != "3" {
		t.Errorf("d = %v", root["d"])
	}
}

func TestParseYAMLList(t *testing.T) {
	// 列表项三种形态：标量项、首键跟破折号的映射项、破折号换行
	// 缩进续写的映射项。
	src := `items:
  - "one"
  - name: "a"
    value: "1"
  -
    name: "b"
    value: "2"
nested:
  - addr: "0.0.0.0:8000"
    backend: "127.0.0.1:8080"
    routes:
      "/api": "127.0.0.1:9091"
`
	root, err := parseYAML([]byte(src))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	items, ok := root["items"].([]any)
	if !ok {
		t.Fatalf("items should be a list, got %T", root["items"])
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	if items[0] != "one" {
		t.Errorf("items[0] = %v", items[0])
	}
	m1, ok := items[1].(map[string]any)
	if !ok || m1["name"] != "a" || m1["value"] != "1" {
		t.Errorf("items[1] = %+v", items[1])
	}
	m2, ok := items[2].(map[string]any)
	if !ok || m2["name"] != "b" || m2["value"] != "2" {
		t.Errorf("items[2] = %+v", items[2])
	}

	nested, ok := root["nested"].([]any)
	if !ok || len(nested) != 1 {
		t.Fatalf("nested = %T", root["nested"])
	}
	n1, ok := nested[0].(map[string]any)
	if !ok || n1["addr"] != "0.0.0.0:8000" || n1["backend"] != "127.0.0.1:8080" {
		t.Errorf("nested[0] = %+v", nested[0])
	}
	routes, ok := n1["routes"].(map[string]any)
	if !ok || routes["/api"] != "127.0.0.1:9091" {
		t.Errorf("nested[0].routes = %+v", n1["routes"])
	}
}
