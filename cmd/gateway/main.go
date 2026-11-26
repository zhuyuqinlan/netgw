// Command gateway 是 HTTP(S) 反向代理网关的入口。
//
// 用法:
//
//	tcpgw -config config.yaml
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
	"syscall"
	"time"

	"tcpgw/internal/config"
	"tcpgw/internal/gateway"
)

// version 由 CI 通过 -ldflags "-X main.version=..." 注入，默认开发版本。
var version = "dev"

// defaultConfigYAML 首次安装时写入用户配置目录的默认模板。
// 与仓库根目录 config.yaml 保持同步（打包脚本会校验）。
var defaultConfigYAML = []byte(`# tcpgw 默认配置模板（首次安装生成，升级不会覆盖已有配置）。

server:
  listen: "0.0.0.0:8000"       # 直接监听地址
  read_header_timeout: 10s     # 读取请求头的期限
  # 同时配置以下两项即启用 HTTPS（TLS 在网关终止）：
  # tls_cert: "cert.pem"
  # tls_key: "key.pem"

http:
  connect_timeout: 5s          # 连接后端超时

# 默认后端 TCP 地址：连接未命中任何前缀路由时，整条连接原始透传。
backend: "127.0.0.1:8080"

# 可选的 HTTP 路径前缀路由（最长前缀优先）。
routes:
  http:
    # "/api": "127.0.0.1:8080"
    # "/web":
    #   backend: "127.0.0.1:8081"
    #   strip_prefix: true

logging:
  level: "info"                # debug / info / warn / error
`)

func main() {
	configPath := flag.String("config", "", "path to YAML configuration file (default: user config > system config > ./config.yaml > built-in defaults)")
	showVersion := flag.Bool("version", false, "print version and exit")
	installConfig := flag.Bool("install-config", false, "create default user config if it does not exist, then exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("tcpgw " + version)
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

	logger.Info("tcpgw starting",
		"config", *configPath,
		"listen", cfg.Server.Listen,
		"tls", cfg.Server.TLSEnabled(),
		"backend", cfg.Backend)

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
