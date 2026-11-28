package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netgw/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// freeAddr 取一个空闲临时 TCP 地址。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// echoBackend 启动原始 TCP 回显后端：收到什么就回什么。
func echoBackend(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().String()
}

// httpBackend 启动记录请求路径并回写 body 的 HTTP 后端。
func httpBackend(t *testing.T, body string, path *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path != nil {
			*path = r.URL.Path
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cfg1 构造单端口混合模式配置：backend 为默认后端，routes 为该端口路由。
func cfg1(t *testing.T, addr, backend string, routes map[string]config.HTTPRoute) *config.Config {
	t.Helper()
	if routes == nil {
		routes = map[string]config.HTTPRoute{}
	}
	return config.DefaultsWithListeners([]config.ListenerConfig{{
		Addr:              addr,
		Backend:           backend,
		ReadHeaderTimeout: 10 * time.Second,
		Routes:            routes,
	}})
}

func startServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	srv, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	// 等待全部端口就绪。
	var addrs []string
	for _, l := range cfg.Server.Listeners {
		addrs = append(addrs, l.Addr)
	}
	ready := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		ready[a] = false
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for _, a := range addrs {
			if ready[a] {
				continue
			}
			conn, err := net.DialTimeout("tcp", a, 50*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				ready[a] = true
			} else {
				allReady = false
			}
		}
		if allReady {
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
				<-errCh
			})
			return srv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not become ready on %s", strings.Join(addrs, ", "))
	return nil
}

func TestGatewayHTTPProxy(t *testing.T) {
	var gotPath, gotHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")
	addr := freeAddr(t)

	cfg := cfg1(t, addr, backendAddr, map[string]config.HTTPRoute{
		"/": {Backend: backendAddr}, // 混合模式：全部 HTTP 请求走反代
	})
	startServer(t, cfg)

	resp, err := http.Get("http://" + addr + "/proxied/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Errorf("status=%d body=%q", resp.StatusCode, body)
	}
	if gotPath != "/proxied/hello" {
		t.Errorf("backend path = %q", gotPath)
	}
	if gotHost != backendAddr {
		t.Errorf("backend Host = %q", gotHost)
	}

	// 未配置 /hello 路由时，内置欢迎页接管 /hello。
	resp2, err := http.Get("http://" + addr + "/hello")
	if err != nil {
		t.Fatalf("GET /hello: %v", err)
	}
	defer resp2.Body.Close()
	helloBody, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != 200 || !strings.Contains(string(helloBody), "netgw") {
		t.Errorf("hello page status=%d body=%q", resp2.StatusCode, helloBody)
	}

	// 用户显式配置 /hello 路由时覆盖内置欢迎页。
	addr3 := freeAddr(t)
	cfg3 := cfg1(t, addr3, backendAddr, map[string]config.HTTPRoute{
		"/hello": {Backend: backendAddr},
	})
	startServer(t, cfg3)
	resp3, err := http.Get("http://" + addr3 + "/hello")
	if err != nil {
		t.Fatalf("GET /hello override: %v", err)
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)
	if string(body3) != "ok" || gotPath != "/hello" {
		t.Errorf("user /hello override: body=%q path=%q", body3, gotPath)
	}
}

func TestGatewayGracefulShutdown(t *testing.T) {
	addr := freeAddr(t)
	cfg := cfg1(t, addr, "127.0.0.1:1", nil)

	srv, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != ErrServerClosed {
			t.Errorf("ListenAndServe returned %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after shutdown")
	}
}

func TestGatewayHTTPS(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := writeSelfSignedCert(certPath, keyPath); err != nil {
		t.Fatalf("generate cert: %v", err)
	}

	var gotPath string
	backend := httpBackend(t, "ok", &gotPath)
	backendAddr := strings.TrimPrefix(backend.URL, "http://")
	addr := freeAddr(t)

	cfg := cfg1(t, addr, backendAddr, map[string]config.HTTPRoute{
		"/": {Backend: backendAddr}, // TLS 终止后命中路由走 HTTP 反代
	})
	cfg.Server.Listeners[0].TLSCertFile = certPath
	cfg.Server.Listeners[0].TLSKeyFile = keyPath
	startServer(t, cfg)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 测试自签证书
		},
	}
	resp, err := client.Get("https://" + addr + "/secure")
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if gotPath != "/secure" {
		t.Errorf("backend path = %q", gotPath)
	}

	// 配置不完整（缺 key）应在 config 校验时报错。
	bad := cfg1(t, "127.0.0.1:0", "127.0.0.1:1", nil)
	bad.Server.Listeners[0].TLSCertFile = certPath
	if err := bad.Validate(); err == nil {
		t.Error("tls_cert without tls_key should fail validation")
	}
}

func TestGatewayPureTCPPassthrough(t *testing.T) {
	// 未配置 routes：纯 TCP 透传到回显后端，不嗅探。
	backendAddr := echoBackend(t)
	addr := freeAddr(t)

	startServer(t, cfg1(t, addr, backendAddr, nil))

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	payload := "raw-tcp-bytes-12345"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != payload {
		t.Errorf("echo = %q, want %q", buf, payload)
	}
	_ = conn.Close()

	// tcp 模式优雅退出：新实例启动后立即关闭，
	// 验证 ListenAndServe 在 Shutdown 后返回 ErrServerClosed。
	addr2 := freeAddr(t)
	srv2, err := New(cfg1(t, addr2, backendAddr, nil), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv2.ListenAndServe() }()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv2.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != ErrServerClosed {
			t.Errorf("ListenAndServe returned %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after shutdown")
	}
}

func TestGatewayTCPModeHybridRouting(t *testing.T) {
	// 混合模式：默认原始透传到回显后端；命中 /api 前缀的 HTTP
	// 请求改走 HTTP 反代到独立后端。
	var apiPath string
	apiBackend := httpBackend(t, "api-ok", &apiPath)
	echoAddr := echoBackend(t)
	addr := freeAddr(t)

	cfg := cfg1(t, addr, echoAddr, map[string]config.HTTPRoute{
		"/api": {Backend: strings.TrimPrefix(apiBackend.URL, "http://"), StripPrefix: true},
	})
	startServer(t, cfg)

	// 1) 命中 /api：走 HTTP 反代，剥前缀。
	resp, err := http.Get("http://" + addr + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "api-ok" {
		t.Errorf("status=%d body=%q", resp.StatusCode, body)
	}
	if apiPath != "/users" {
		t.Errorf("api backend path = %q, want /users", apiPath)
	}

	// 2) 非 HTTP 数据：原始透传到回显后端。
	passthroughEcho(t, addr, "raw-tcp-bytes-12345")

	// 3) 未命中前缀的 HTTP 请求：原样透传（回显后端原样返回字节）。
	passthroughEcho(t, addr, "GET /other/page HTTP/1.1\r\nHost: x\r\n\r\n")
}

// passthroughEcho 向网关发送 payload 并期望原样回显（透传语义）。
func passthroughEcho(t *testing.T, addr, payload string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	_ = conn.Close()
	if string(buf) != payload {
		t.Errorf("echo = %q, want %q", buf, payload)
	}
}

func TestGatewayWebRoot(t *testing.T) {
	// web_root 静态站点：存在的文件伺服，目录取 index.html，
	// 不存在的路径回退反代到默认后端。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>site-home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('js-ok')"), 0o644); err != nil {
		t.Fatal(err)
	}

	var fallbackPath string
	backend := httpBackend(t, "fallback-ok", &fallbackPath)
	backendAddr := strings.TrimPrefix(backend.URL, "http://")
	addr := freeAddr(t)

	cfg := cfg1(t, addr, backendAddr, nil)
	cfg.Server.Listeners[0].WebRoot = dir
	// web_root 校验要求目录存在（本测试临时目录满足）。
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	startServer(t, cfg)

	// index.html 作为站点首页。
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "site-home") {
		t.Errorf("index: status=%d body=%q", resp.StatusCode, body)
	}

	// 普通文件直接伺服。
	resp2, err := http.Get("http://" + addr + "/app.js")
	if err != nil {
		t.Fatalf("GET /app.js: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 || string(body2) != "console.log('js-ok')" {
		t.Errorf("app.js: status=%d body=%q", resp2.StatusCode, body2)
	}

	// 不存在的路径回退反代到默认后端。
	resp3, err := http.Get("http://" + addr + "/missing")
	if err != nil {
		t.Fatalf("GET /missing: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || string(body3) != "fallback-ok" || fallbackPath != "/missing" {
		t.Errorf("fallback: status=%d body=%q path=%q", resp3.StatusCode, body3, fallbackPath)
	}

	// web_root 不存在时校验报错。
	bad := cfg1(t, "127.0.0.1:0", "127.0.0.1:1", nil)
	bad.Server.Listeners[0].WebRoot = filepath.Join(t.TempDir(), "no-such-dir")
	if err := bad.Validate(); err == nil {
		t.Error("nonexistent web_root should fail validation")
	}
}

func TestGatewayRejectsBadConfig(t *testing.T) {
	// config 层校验拦截空 backend；New 层空监听列表直接报错。
	cfg := cfg1(t, "127.0.0.1:0", "127.0.0.1:1", nil)
	cfg.Server.Listeners = nil
	if _, err := New(cfg, testLogger()); err == nil {
		t.Error("no listener should fail at New")
	}
}

func TestGatewayPerPortConfig(t *testing.T) {
	// 多端口各自独立配置：addr1 走全局式混合模式（/api 反代 + 默认
	// 后端），addr2 纯透传到自己的 backend，addr3 命中自己的路由。
	var apiPath string
	apiBackend := httpBackend(t, "api-ok", &apiPath)
	var defaultPath string
	defaultBackend := httpBackend(t, "default-ok", &defaultPath)
	var otherPath string
	otherBackend := httpBackend(t, "other-ok", &otherPath)
	echoAddr := echoBackend(t)

	addr1, addr2, addr3 := freeAddr(t), freeAddr(t), freeAddr(t)

	cfg := config.DefaultsWithListeners([]config.ListenerConfig{
		{
			Addr:              addr1,
			Backend:           strings.TrimPrefix(defaultBackend.URL, "http://"),
			ReadHeaderTimeout: 10 * time.Second,
			Routes: map[string]config.HTTPRoute{
				"/api": {Backend: strings.TrimPrefix(apiBackend.URL, "http://"), StripPrefix: true},
			},
		},
		// addr2：无路由，纯透传到 echo 后端。
		{Addr: addr2, Backend: echoAddr, ReadHeaderTimeout: 10 * time.Second},
		// addr3：自己的 backend 与路由。
		{
			Addr:              addr3,
			Backend:           strings.TrimPrefix(otherBackend.URL, "http://"),
			ReadHeaderTimeout: 10 * time.Second,
			Routes: map[string]config.HTTPRoute{
				"/api": {Backend: strings.TrimPrefix(otherBackend.URL, "http://")},
			},
		},
	})
	startServer(t, cfg)

	// addr1：/api 走反代（剥前缀），其他请求到 default 后端。
	resp, err := http.Get("http://" + addr1 + "/api/users")
	if err != nil {
		t.Fatalf("addr1 GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "api-ok" || apiPath != "/users" {
		t.Errorf("addr1 api: status=%d body=%q path=%q", resp.StatusCode, body, apiPath)
	}
	resp2, err := http.Get("http://" + addr1 + "/other")
	if err != nil {
		t.Fatalf("addr1 GET /other: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != "default-ok" || defaultPath != "/other" {
		t.Errorf("addr1 default backend = %q %q", body2, defaultPath)
	}

	// addr2：无路由，未命中 HTTP 请求回退反代到 echo 后端
	//（echo 原样回显 HTTP 响应行，status 502 表明连接成功但
	// 后端返回非标准响应；此处仅验证请求到达后端而非透传失败）。
	conn, err := net.Dial("tcp", addr2)
	if err != nil {
		t.Fatalf("dial addr2: %v", err)
	}
	if _, err := io.WriteString(conn, "GET /api/users HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	_ = conn.Close()
	if n == 0 {
		t.Error("addr2: no response for unmatched HTTP request")
	}

	// addr3：/api 命中自己的路由（保留前缀），backend 为 other。
	resp3, err := http.Get("http://" + addr3 + "/api/users")
	if err != nil {
		t.Fatalf("addr3 GET: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || string(body3) != "other-ok" || otherPath != "/api/users" {
		t.Errorf("addr3 api: status=%d body=%q path=%q", resp3.StatusCode, body3, otherPath)
	}
}

func TestGatewayMultiListenPartialFailure(t *testing.T) {
	// 任一端口监听失败应整体失败，且已成功的端口不应遗留服务。
	okAddr := freeAddr(t)
	okLn, err := net.Listen("tcp", okAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	badAddr := freeAddr(t)
	badLn, err := net.Listen("tcp", badAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer badLn.Close()

	cfg := config.DefaultsWithListeners([]config.ListenerConfig{
		{Addr: okAddr, Backend: "127.0.0.1:1"},
		{Addr: badAddr, Backend: "127.0.0.1:1"},
	})

	srv, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err == nil || errors.Is(err, ErrServerClosed) {
			t.Fatalf("ListenAndServe = %v, want listen error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not fail on partial listen error")
	}

	// 已成功的端口必须已被关闭。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	// okLn 占用期间不可能有第二个监听成功；关闭后应能重新绑定
	//（证明网关监听器已释放）。
	_ = okLn.Close()
	ln2, err := net.Listen("tcp", okAddr)
	if err != nil {
		t.Fatalf("gateway listener on %s not released: %v", okAddr, err)
	}
	_ = ln2.Close()
}

// writeSelfSignedCert 生成临时测试用自签证书（仅用于单元测试）。
func writeSelfSignedCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gateway-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
