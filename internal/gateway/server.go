// Package gateway 提供端口监听、混合分流（默认 TCP 透传 +
// routes 命中走 HTTP 反代）与优雅退出。
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"

	"netgw/internal/config"
	"netgw/internal/proxy"
)

// ErrServerClosed 在服务已关闭后返回。
var ErrServerClosed = errors.New("gateway: server closed")

// portConfig 是单个监听端口生效的配置与运行时对象。
type portConfig struct {
	cfg     config.ListenerConfig
	tlsCfg  *tls.Config
	httpSrv *http.Server
	router  *proxy.Router
}

// Server 是网关服务，每个监听端口独立工作（混合模式）：
//
//   - 未命中：每条入站连接默认原始字节透传到该端口的 backend，
//     不做任何协议解析
//   - 命中：配置了 routes 时，对嗅探到 HTTP 请求行且路径命中
//     前缀的连接改走 HTTP 反向代理（WebSocket 等 Upgrade 请求
//     同样透传）；未命中的连接照旧透传
//   - 端口配置了 TLS 证书时按连接嗅探：客户端发起 TLS 握手则
//     网关终止 TLS 后按上述规则处理解密明文，非 TLS 连接不受
//     影响，单端口同时服务 HTTPS 与原始 TCP
//   - 未配置 routes 时不做 HTTP 嗅探，即纯 TCP 透传，零开销
type Server struct {
	cfg    *config.Config
	logger *slog.Logger

	// ports 是按监听地址索引的端口配置（含运行时对象）。
	ports map[string]*portConfig

	// listeners 记录全部监听器，供 Shutdown 统一关闭。
	listenersMu sync.Mutex
	listeners   []net.Listener

	// conns 跟踪活跃连接（含出站连接），供强制清理。
	connsMu sync.Mutex
	conns   map[net.Conn]struct{}
	// wg 等待全部连接处理 goroutine 结束。
	wg sync.WaitGroup
	// closed 标记服务进入关闭流程。
	closed  bool
	closeMu sync.Mutex
}

// New 根据配置创建网关服务。
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Server.Listeners) == 0 {
		return nil, errors.New("gateway: no listener configured")
	}
	s := &Server{
		cfg:    cfg,
		logger: logger,
		conns:  make(map[net.Conn]struct{}),
		ports:  make(map[string]*portConfig, len(cfg.Server.Listeners)),
	}

	for _, lc := range cfg.Server.Listeners {
		p := &portConfig{cfg: lc}
		// 端口未显式配置读取期限时用全局默认。
		if p.cfg.ReadHeaderTimeout == 0 {
			p.cfg.ReadHeaderTimeout = cfg.Server.ReadHeaderTimeout
		}

		var router *proxy.Router
		// 所有端口都启用 HTTP 分流：合法 HTTP 请求进入处理链
		//（欢迎页/静态文件/路由反代/回退反代），非 HTTP 协议
		// 嗅探失败后照旧整条透传，功能不受影响。
		{
			var err error
			if len(lc.Routes) > 0 {
				router, err = proxy.NewRouter(routesFromConfig(lc.Routes))
				if err != nil {
					return nil, fmt.Errorf("gateway: build http router for %s: %w", lc.Addr, err)
				}
			}
			proxyH, perr := proxy.New(lc.Backend, router, cfg.HTTP.ConnectTimeout, cfg.HTTP.TLSSkipVerify, logger)
			if perr != nil {
				return nil, fmt.Errorf("gateway: create proxy for %s: %w", lc.Addr, perr)
			}
			p.router = router
			// web_root 相对路径按进程工作目录解析为绝对路径。
			root := lc.WebRoot
			if root != "" {
				if abs, aerr := filepath.Abs(root); aerr == nil {
					root = abs
				}
			}
			var staticH http.Handler
			if lc.WebRoot != "" {
				staticH = newStaticHandler(root)
			}
			p.httpSrv = &http.Server{
				Handler:           newPortHandler(router, proxyH, staticH, root, hasUserHelloRoute(lc.Routes)),
				ReadHeaderTimeout: p.cfg.ReadHeaderTimeout,
			}
		}
		s.ports[lc.Addr] = p
	}
	return s, nil
}

// hasUserHelloRoute 判断用户路由表是否显式配置了 /hello。
func hasUserHelloRoute(routes map[string]config.HTTPRoute) bool {
	_, ok := routes[helloPath]
	return ok
}

// ListenAndServe 监听全部配置端口并开始服务，阻塞直到返回错误。
// 通过 Shutdown 关闭时返回 ErrServerClosed。
func (s *Server) ListenAndServe() error {
	return s.listenAndServe()
}

// Shutdown 优雅关闭：停止接收新连接，等待活跃连接结束；
// 超过 ctx 期限后强制关闭并返回。
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeMu.Lock()
	s.closed = true
	s.closeMu.Unlock()
	return s.shutdown(ctx)
}

// routesFromConfig 把配置中的 HTTP 路由表转换为代理路由表。
func routesFromConfig(routes map[string]config.HTTPRoute) []proxy.Route {
	list := make([]proxy.Route, 0, len(routes))
	for prefix, r := range routes {
		list = append(list, proxy.Route{
			Prefix:      prefix,
			Backend:     r.Backend,
			StripPrefix: r.StripPrefix,
			WebRoot:     r.WebRoot,
		})
	}
	return list
}
