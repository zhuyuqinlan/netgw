// Package config 加载并校验网关配置。
//
// 本项目仅依赖 Go 标准库，因此内置了一个只覆盖本项目所需语法子集的
// YAML 解析器（见 yaml.go）：嵌套映射、列表、标量值、双引号字符串、
// 行注释。完整 YAML 语法不在支持范围内。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ListenerConfig 是单个监听端口的完整配置。
type ListenerConfig struct {
	// Addr 是监听地址（如 "0.0.0.0:8000"）。
	Addr string `yaml:"addr"`
	// ReadHeaderTimeout 是读取请求头（含 TLS 握手后的首个请求）的
	// 期限，防止慢连接长期占用 goroutine。
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// TLSCertFile / TLSKeyFile 均非空时该端口启用 HTTPS（TLS 终止
	// 在本网关）。按连接嗅探：同一端口可同时服务 HTTPS 与原始 TCP。
	TLSCertFile string `yaml:"tls_cert"`
	TLSKeyFile  string `yaml:"tls_key"`
	// Backend 是该端口默认后端 TCP 地址：连接未命中任何前缀路由时，
	// 整条连接原始透传到该地址。
	Backend string `yaml:"backend"`
	// Routes 是该端口可选的 HTTP 路径前缀路由表（最长前缀优先）。
	// WebSocket 等 Upgrade 请求命中前缀后同样由反向代理透传。
	Routes map[string]HTTPRoute `yaml:"routes"`
	// WebRoot 是该端口可选的静态站点目录（类似 nginx root）：请求
	// 未命中 routes 前缀时按静态文件伺服（index.html 为目录索引），
	// 文件不存在时回退透传到 backend。空表示不伺服静态文件。
	WebRoot string `yaml:"web_root"`
}

// TLSEnabled 返回该端口是否启用 HTTPS。
func (l ListenerConfig) TLSEnabled() bool {
	return l.TLSCertFile != "" || l.TLSKeyFile != ""
}

// ServerConfig 监听相关全局配置。
type ServerConfig struct {
	// Listeners 是全部监听端口配置（顺序即配置顺序）。
	Listeners []ListenerConfig `yaml:"listeners"`
	// ReadHeaderTimeout 是端口未显式配置时的默认请求头读取期限。
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
}

// HTTPConfig HTTP 代理全局配置。
type HTTPConfig struct {
	// ConnectTimeout 是连接后端的拨号超时。
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	// TLSSkipVerify 为 true 时，网关不校验 HTTPS 后端返回的证书
	// （自签名证书场景使用，仅建议测试环境开启）。
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
}

// HTTPRoute 是一条 HTTP 路径前缀路由。
type HTTPRoute struct {
	// Backend 是该前缀命中的请求转发目标。
	Backend string `yaml:"backend"`
	// StripPrefix 为 true 时，转发前把命中的前缀从请求路径中
	// 剥掉（如 /api/users → /users；剥完为空时转发为 /）。
	// false 表示透明转发（保留前缀）。
	StripPrefix bool `yaml:"strip_prefix"`
	// WebRoot 是该路由可选的静态站点目录：命中该前缀且文件存在时
	// 优先伺服静态文件（index.html 为目录索引），文件不存在时
	// 反代到 Backend。空表示该路由不伺服静态文件。
	WebRoot string `yaml:"web_root"`
}

// LoggingConfig 日志配置。
type LoggingConfig struct {
	Level string `yaml:"level"`
}

// Config 顶层配置。
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	HTTP    HTTPConfig    `yaml:"http"`
	Logging LoggingConfig `yaml:"logging"`
}

// Defaults 返回一份带全部默认值的配置。未在配置文件中出现的字段
// 将保留这里的默认值。
func Defaults() *Config {
	return &Config{
		Server: ServerConfig{
			ReadHeaderTimeout: 10 * time.Second,
			Listeners: []ListenerConfig{
				{
					Addr:    "0.0.0.0:7822",
					Backend: "127.0.0.1:8080",
				},
			},
		},
		HTTP: HTTPConfig{
			ConnectTimeout: 5 * time.Second,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
	}
}

// DefaultsWithListeners 返回带给定监听端口列表的默认配置。
func DefaultsWithListeners(listeners []ListenerConfig) *Config {
	cfg := Defaults()
	cfg.Server.Listeners = listeners
	return cfg
}

// Validate 校验配置（供调用方在 Load 之外复用校验逻辑）。
func (c *Config) Validate() error { return c.validate() }

// Load 从 path 读取 YAML 配置并叠加到默认值上。
// path 为空字符串时直接返回默认配置。
func Load(path string) (*Config, error) {
	cfg := Defaults()
	if path == "" {
		return cfg, cfg.validate()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	root, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	// listeners 与 read_header_timeout 直接放顶层（无 server: 层级）。
	if err := func() error {
		if err := decodeDuration(root, "read_header_timeout", &cfg.Server.ReadHeaderTimeout); err != nil {
			return err
		}
		return decodeListeners(root, "listeners", cfg)
	}(); err != nil {
		return nil, err
	}

	if err := applySection(root, "http", func(m map[string]any) error {
		if err := decodeDuration(m, "connect_timeout", &cfg.HTTP.ConnectTimeout); err != nil {
			return err
		}
		return decodeBool(m, "tls_skip_verify", &cfg.HTTP.TLSSkipVerify)
	}); err != nil {
		return nil, fmt.Errorf("section http: %w", err)
	}

	if err := applySection(root, "logging", func(m map[string]any) error {
		return decodeString(m, "level", &cfg.Logging.Level)
	}); err != nil {
		return nil, fmt.Errorf("section logging: %w", err)
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if len(c.Server.Listeners) == 0 {
		return fmt.Errorf("listeners must not be empty")
	}
	seen := map[string]bool{}
	for i, l := range c.Server.Listeners {
		if l.Addr == "" {
			return fmt.Errorf("listeners[%d]: addr must not be empty", i)
		}
		if seen[l.Addr] {
			return fmt.Errorf("listeners[%d]: duplicate address %q", i, l.Addr)
		}
		seen[l.Addr] = true
		if l.Backend == "" {
			return fmt.Errorf("listeners[%d] (%s): backend must not be empty", i, l.Addr)
		}
		if l.TLSEnabled() && (l.TLSCertFile == "" || l.TLSKeyFile == "") {
			return fmt.Errorf("listeners[%d] (%s): tls_cert and tls_key must be configured together", i, l.Addr)
		}
		if l.ReadHeaderTimeout < 0 {
			return fmt.Errorf("listeners[%d] (%s): read_header_timeout must not be negative", i, l.Addr)
		}
		for prefix, route := range l.Routes {
			if !strings.HasPrefix(prefix, "/") {
				return fmt.Errorf("listeners[%d] (%s) routes prefix %q must start with '/'", i, l.Addr, prefix)
			}
			if route.Backend == "" {
				return fmt.Errorf("listeners[%d] (%s) routes[%s]: backend must not be empty", i, l.Addr, prefix)
			}
			if route.WebRoot != "" {
				st, err := os.Stat(route.WebRoot)
				if err != nil {
					return fmt.Errorf("listeners[%d] (%s) routes[%s]: web_root %q: %w", i, l.Addr, prefix, route.WebRoot, err)
				}
				if !st.IsDir() {
					return fmt.Errorf("listeners[%d] (%s) routes[%s]: web_root %q is not a directory", i, l.Addr, prefix, route.WebRoot)
				}
			}
		}
		if l.WebRoot != "" {
			st, err := os.Stat(l.WebRoot)
			if err != nil {
				return fmt.Errorf("listeners[%d] (%s): web_root %q: %w", i, l.Addr, l.WebRoot, err)
			}
			if !st.IsDir() {
				return fmt.Errorf("listeners[%d] (%s): web_root %q is not a directory", i, l.Addr, l.WebRoot)
			}
		}
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("read_header_timeout must be > 0")
	}
	if c.HTTP.ConnectTimeout <= 0 {
		return fmt.Errorf("http.connect_timeout must be > 0")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be one of debug/info/warn/error, got %q", c.Logging.Level)
	}
	return nil
}

// applySection 取 root[section] 并在存在时调用 fn。
func applySection(root map[string]any, section string, fn func(map[string]any) error) error {
	v, ok := root[section]
	if !ok || v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("expected a mapping")
	}
	return fn(m)
}

func decodeString(m map[string]any, key string, dst *string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("field %s: expected string, got %T", key, v)
	}
	*dst = s
	return nil
}

// decodeListeners 将 listeners 解码为监听端口列表。默认值中
// 预置的单端口条目会被整体替换（列表是显式配置）。
func decodeListeners(m map[string]any, key string, cfg *Config) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	// "listeners:" 后无列表项（如全注释模板）时解析器留下空映射
	// 占位，按未配置处理，走默认值。
	if em, isMap := v.(map[string]any); isMap && len(em) == 0 {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return fmt.Errorf("field %s: expected a list, got %T", key, v)
	}
	cfg.Server.Listeners = []ListenerConfig{}
	for i, item := range items {
		im, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("field %s[%d]: expected a mapping, got %T", key, i, item)
		}
		var l ListenerConfig
		if err := decodeString(im, "addr", &l.Addr); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		if err := decodeString(im, "backend", &l.Backend); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		if err := decodeString(im, "tls_cert", &l.TLSCertFile); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		if err := decodeString(im, "tls_key", &l.TLSKeyFile); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		if err := decodeDuration(im, "read_header_timeout", &l.ReadHeaderTimeout); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		l.Routes = map[string]HTTPRoute{}
		if err := decodeHTTPRoutes(im, "routes", l.Routes); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		if err := decodeString(im, "web_root", &l.WebRoot); err != nil {
			return fmt.Errorf("field %s[%d]: %w", key, i, err)
		}
		cfg.Server.Listeners = append(cfg.Server.Listeners, l)
	}
	return nil
}

// decodeHTTPRoutes 将 m[key] 解码为 HTTP 路由表。条目值支持两种形式：
//
//	"/api": "127.0.0.1:8080"        # 简写：仅后端地址，保留前缀
//	"/web":                         # 完整形式
//	  backend: "127.0.0.1:8081"
//	  strip_prefix: true
//	  web_root: "/var/www/admin"    # 可选：命中且文件存在时伺服静态文件
func decodeHTTPRoutes(m map[string]any, key string, dst map[string]HTTPRoute) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("field %s: expected a mapping", key)
	}
	for prefix, sv := range sub {
		if sv == nil {
			continue
		}
		switch val := sv.(type) {
		case string:
			dst[prefix] = HTTPRoute{Backend: val}
		case map[string]any:
			var r HTTPRoute
			if err := decodeString(val, "backend", &r.Backend); err != nil {
				return fmt.Errorf("field %s.%s: %w", key, prefix, err)
			}
			if raw, present := val["strip_prefix"]; present && raw != nil {
				b, err := parseBool(raw)
				if err != nil {
					return fmt.Errorf("field %s.%s.strip_prefix: %w", key, prefix, err)
				}
				r.StripPrefix = b
			}
			if err := decodeString(val, "web_root", &r.WebRoot); err != nil {
				return fmt.Errorf("field %s.%s: %w", key, prefix, err)
			}
			dst[prefix] = r
		default:
			return fmt.Errorf("field %s.%s: expected backend string or mapping, got %T", key, prefix, sv)
		}
	}
	return nil
}

func decodeDuration(m map[string]any, key string, dst *time.Duration) error {
	v, present := m[key]
	if !present || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("field %s: expected duration string, got %T", key, v)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("field %s: invalid duration %q", key, s)
	}
	if d <= 0 {
		return fmt.Errorf("field %s: duration must be > 0", key)
	}
	*dst = d
	return nil
}

func parseBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return false, fmt.Errorf("invalid bool %q", t)
		}
		return b, nil
	default:
		return false, fmt.Errorf("expected bool, got %T", v)
	}
}

func decodeBool(m map[string]any, key string, dst *bool) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	b, err := parseBool(v)
	if err != nil {
		return fmt.Errorf("field %s: %w", key, err)
	}
	*dst = b
	return nil
}
