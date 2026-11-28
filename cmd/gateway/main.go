// Command gateway 是 HTTP(S) 反向代理网关的入口。
//
// 用法:
//
//	netgw -config config.yaml
//
// 处理 SIGINT/SIGTERM 触发优雅退出：停止接收新连接、
// 等待活跃连接结束（最长 10 秒），随后强制清理并退出。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"netgw/internal/config"
	"netgw/internal/gateway"
)

// version 由 CI 通过 -ldflags "-X main.version=..." 注入，默认开发版本。
var version = "dev"

// defaultConfigYAML 首次安装时写入用户配置目录的默认模板。
// 与仓库根目录 config.yaml 的默认值保持一致。
var defaultConfigYAML = []byte(`# netgw 默认配置模板（首次安装生成，升级不会覆盖已有配置）。
# 完整示例（含静态站点、HTTPS、多端口）见发行包 config.yaml。

# 全局默认请求头读取期限（listener 未单独配置时生效）。
read_header_timeout: 10s

# 监听端口列表，每个端口独立配置 backend、路由、静态站点与 TLS。
# 未配置 routes/web_root 的端口为纯 TCP 透传（零嗅探开销）；
# 同时配置 tls_cert/tls_key 即启用 HTTPS（按连接嗅探，
# 同一端口同时服务 HTTPS 与原始 TCP）。
# 访问 http://<addr>/hello 可查看内置欢迎页。
listeners:
  - addr: "0.0.0.0:7822"
    backend: "127.0.0.1:8080"
    # routes:
    #   "/api": "127.0.0.1:9091"       # 简写：前缀保留
    #   "/web":
    #     backend: "127.0.0.1:9092"
    #     strip_prefix: true          # /web/x → 后端收到 /x
    #     web_root: "/var/www/webadmin"  # 路由级静态站点
    # web_root: "/var/www/mysite"     # 端口级静态站点目录
    # tls_cert: "cert.pem"
    # tls_key: "key.pem"

http:
  connect_timeout: 5s          # 连接后端超时

logging:
  level: "info"                # debug / info / warn / error
`)

func main() {
	configPath := flag.String("config", "", "path to YAML configuration file (default: user config > system config > ./config.yaml > built-in defaults)")
	showVersion := flag.Bool("version", false, "print version and exit")
	installConfig := flag.Bool("install-config", false, "create default user config if it does not exist, then exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("netgw " + version)
		return
	}

	if *installConfig {
		created, err := config.EnsureUserConfig(defaultConfigYAML)
		if err != nil {
			fmt.Fprintf(os.Stderr, "install config: %v\n", err)
			os.Exit(1)
		}
		if created {
			fmt.Println("created user config:", config.UserConfigPath())
		} else {
			fmt.Println("user config already exists, kept unchanged:", config.UserConfigPath())
		}
		return
	}

	cfg, _, err := config.LoadAuto(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if *configPath == "" {
		// 报告配置来源；未找到文件时使用内置默认值。
		p, src := config.Resolve("")
		switch src {
		case "built-in defaults":
			fmt.Fprintln(os.Stderr, "no config file found, using built-in defaults")
			*configPath = ""
		default:
			fmt.Fprintf(os.Stderr, "using %s: %s\n", src, p)
			*configPath = p
		}
	}

	logger := newLogger(cfg.Logging.Level)
	slog.SetDefault(logger)

	srv, err := gateway.New(cfg, logger)
	if err != nil {
		logger.Error("create gateway", "error", err)
		os.Exit(1)
	}

	// 信号驱动的优雅退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logger.Info("shutdown signal received, draining connections...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("graceful shutdown finished with timeout", "error", err)
		}
	}()

	// 汇总各端口地址与 TLS 状态用于日志。
	addrs := make([]string, 0, len(cfg.Server.Listeners))
	anyTLS := false
	for _, l := range cfg.Server.Listeners {
		addrs = append(addrs, l.Addr)
		if l.TLSEnabled() {
			anyTLS = true
		}
	}

	logger.Info("netgw starting",
		"config", *configPath,
		"listen", strings.Join(addrs, ", "),
		"tls", anyTLS)

	err = srv.ListenAndServe()
	if err == gateway.ErrServerClosed {
		logger.Info("gateway exited cleanly")
		return
	}
	if err != nil {
		logger.Error("gateway exited with error", "error", err)
		os.Exit(1)
	}
}

// newLogger 根据配置级别构造 slog Logger（文本输出到 stderr）。
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
