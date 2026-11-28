package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestProxy(t *testing.T, defaultBackend string, routes []Route) *Proxy {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	router, err := NewRouter(routes)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	p, err := New(defaultBackend, router, 2*time.Second, false, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestProxyRouteWebRoot(t *testing.T) {
	// 路由级 web_root：命中前缀且文件存在时伺服静态文件（含
	// strip_prefix），文件不存在时反代到路由 Backend。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>admin-site</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var backendPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendPath = r.URL.Path
		_, _ = io.WriteString(w, "backend-ok")
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	p := newTestProxy(t, backendAddr, []Route{
		{Prefix: "/admin", Backend: backendAddr, StripPrefix: true, WebRoot: dir},
	})
	srv := httptest.NewServer(p)
	defer srv.Close()
	client := srv.Client()

	// 1) 静态文件存在：伺服 web_root 下文件（strip_prefix 后的路径）。
	resp, err := client.Get(srv.URL + "/admin/app.css")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "body{}" {
		t.Errorf("static /admin/app.css: status=%d body=%q", resp.StatusCode, body)
	}
	// 目录请求 → index.html。
	resp2, err := client.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 || !strings.Contains(string(body2), "admin-site") {
		t.Errorf("static /admin/: status=%d body=%q", resp2.StatusCode, body2)
	}
	if backendPath == "/app.css" || backendPath == "/" {
		t.Errorf("static request should not reach backend, backendPath=%q", backendPath)
	}

	// 2) 文件不存在：反代到路由 Backend（剥前缀）。
	resp3, err := client.Get(srv.URL + "/admin/api/users")
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || string(body3) != "backend-ok" || backendPath != "/api/users" {
		t.Errorf("fallback proxy: status=%d body=%q backendPath=%q", resp3.StatusCode, body3, backendPath)
	}
}

func TestRouterLongestPrefixFirst(t *testing.T) {
	r, err := NewRouter([]Route{
		{Prefix: "/api", Backend: "1.1.1.1:80"},
		{Prefix: "/api/v1", Backend: "2.2.2.2:80"},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if rt, ok := r.Match("/api/v1/users"); !ok || rt.Backend != "2.2.2.2:80" {
		t.Errorf("match /api/v1/users = %+v, ok=%v", rt, ok)
	}
	if rt, ok := r.Match("/api/users"); !ok || rt.Backend != "1.1.1.1:80" {
		t.Errorf("match /api/users = %+v, ok=%v", rt, ok)
	}
	if _, ok := r.Match("/other"); ok {
		t.Error("/other should not match")
	}
}

func TestRouterRejectsInvalid(t *testing.T) {
	if _, err := NewRouter([]Route{{Prefix: "api", Backend: "1.1.1.1:80"}}); err == nil {
		t.Error("prefix without slash should be rejected")
	}
	if _, err := NewRouter([]Route{{Prefix: "/api", Backend: ""}}); err == nil {
		t.Error("empty backend should be rejected")
	}
}

func TestProxyForwardsToDefaultBackend(t *testing.T) {
	// 未配置路由时，所有请求都转发到默认后端。
	var gotPath, gotHost, gotXFF string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		gotXFF = r.Header.Get("X-Forwarded-For")
		_, _ = io.WriteString(w, "hello from default")
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	p := newTestProxy(t, addr, nil)
	srv := httptest.NewServer(p)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/anything/here", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if string(body) != "hello from default" {
		t.Errorf("body = %q", body)
	}
	if gotPath != "/anything/here" {
		t.Errorf("backend path = %q, want /anything/here (prefix preserved)", gotPath)
	}
	if gotHost != addr {
		t.Errorf("backend Host = %q, want %q", gotHost, addr)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For should be set")
	}
}

func TestProxyRouteMatchAndStripPrefix(t *testing.T) {
	var defaultHits, apiHits int
	var apiPath, apiHost string
	defaultBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultHits++
		_, _ = io.WriteString(w, "default")
	}))
	defer defaultBackend.Close()
	apiBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits++
		apiPath = r.URL.Path
		apiHost = r.Host
		_, _ = io.WriteString(w, "api")
	}))
	defer apiBackend.Close()

	p := newTestProxy(t,
		strings.TrimPrefix(defaultBackend.URL, "http://"),
		[]Route{{Prefix: "/api", Backend: strings.TrimPrefix(apiBackend.URL, "http://"), StripPrefix: true}},
	)
	srv := httptest.NewServer(p)
	defer srv.Close()

	// /api/users → /users
	resp, err := http.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users: %v", err)
	}
	resp.Body.Close()
	if apiHits != 1 {
		t.Fatalf("api hits = %d", apiHits)
	}
	if apiPath != "/users" {
		t.Errorf("stripped path = %q, want /users", apiPath)
	}
	apiAddr := strings.TrimPrefix(apiBackend.URL, "http://")
	if apiHost != apiAddr {
		t.Errorf("backend Host = %q, want %q", apiHost, apiAddr)
	}

	// 未命中 → 默认后端
	if _, err := http.Get(srv.URL + "/web/index"); err != nil {
		t.Fatalf("GET /web/index: %v", err)
	}
	if defaultHits != 1 {
		t.Errorf("default hits = %d, want 1", defaultHits)
	}
}

func TestProxyStripPrefixRoot(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer backend.Close()

	p := newTestProxy(t,
		strings.TrimPrefix(backend.URL, "http://"),
		[]Route{{Prefix: "/api", Backend: strings.TrimPrefix(backend.URL, "http://"), StripPrefix: true}},
	)
	srv := httptest.NewServer(p)
	defer srv.Close()

	if _, err := http.Get(srv.URL + "/api"); err != nil {
		t.Fatalf("GET /api: %v", err)
	}
	if gotPath != "/" {
		t.Errorf("path = %q, want / (empty after strip)", gotPath)
	}
}

func TestProxyPostBodyForwarded(t *testing.T) {
	var gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer backend.Close()

	p := newTestProxy(t, strings.TrimPrefix(backend.URL, "http://"), nil)
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/submit", "text/plain", strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if gotBody != "hello world" {
		t.Errorf("backend body = %q", gotBody)
	}
}

func TestProxyBackendUnreachable502(t *testing.T) {
	p := newTestProxy(t, "127.0.0.1:1", nil) // 端口 1 几乎必然不可达
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestProxyHTTPSBackend(t *testing.T) {
	// https:// 后端：网关以 TLS 连接后端（自签名证书 + 跳过校验）。
	var gotPath string
	var gotTLS bool
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTLS = r.TLS != nil
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "https://")

	router, err := NewRouter(nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p, err := New("https://"+addr, router, 2*time.Second, true, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tls-check")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if !gotTLS {
		t.Error("backend did not receive a TLS connection")
	}
	if gotPath != "/tls-check" {
		t.Errorf("backend path = %q", gotPath)
	}
}

func TestParseBackend(t *testing.T) {
	cases := []struct {
		in, scheme, host string
	}{
		{"127.0.0.1:8080", "http", "127.0.0.1:8080"},
		{"https://frp.example.com:63299", "https", "frp.example.com:63299"},
		{"http://1.2.3.4", "http", "1.2.3.4"},
		{"ftp://1.2.3.4", "http", "1.2.3.4"},
	}
	for _, c := range cases {
		u := parseBackend(c.in)
		if u.Scheme != c.scheme || u.Host != c.host {
			t.Errorf("parseBackend(%q) = %s://%s, want %s://%s", c.in, u.Scheme, u.Host, c.scheme, c.host)
		}
	}
}
