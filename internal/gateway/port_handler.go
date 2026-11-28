// 端口级 HTTP 处理链：内置 /hello 欢迎页 → routes 前缀反代 →
// web_root 静态站点 → 回退反代到默认后端。
package gateway

import (
	"net/http"
	"os"
	"path"
	"path/filepath"

	"netgw/internal/proxy"
)

// newStaticHandler 返回 web_root 静态文件伺服器（类似 nginx root）：
// 目录请求优先 index.html。
func newStaticHandler(root string) http.Handler {
	return http.FileServer(http.Dir(root))
}

// helloPath 是内置欢迎页路径（全端口内置，用户配置同名路由可覆盖）。
const helloPath = "/hello"

// helloPage 是 /hello 的欢迎页内容。
const helloPage = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>netgw 网关</title>
<style>
  body { font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
         display: flex; align-items: center; justify-content: center;
         min-height: 100vh; margin: 0; background: #f5f7fa; color: #333; }
  .card { text-align: center; padding: 48px 64px; background: #fff;
          border-radius: 12px; box-shadow: 0 2px 16px rgba(0,0,0,.08); }
  h1 { margin: 0 0 8px; font-size: 28px; }
  p { margin: 6px 0; color: #666; }
  code { background: #eef1f5; padding: 2px 6px; border-radius: 4px; }
</style>
</head>
<body>
<div class="card">
  <h1>&#10004; netgw 网关正在运行</h1>
  <p>本端口由 netgw 提供服务。</p>
  <p>配置路由与静态站点见 <code>config.yaml</code> 的 <code>listeners</code> 段。</p>
</div>
</body>
</html>
`

// newHelloHandler 返回内置欢迎页处理器。
func newHelloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(helloPage))
	})
}

// staticExists 判断 web_root 下 upath 是否存在可伺服的文件
// （目录含 index.html 视为存在）。
func staticExists(root, upath string) bool {
	name := filepath.Join(root, filepath.FromSlash(path.Clean("/"+upath)))
	st, err := os.Stat(name)
	if err != nil {
		return false
	}
	if st.IsDir() {
		idx, err := os.Stat(filepath.Join(name, "index.html"))
		return err == nil && !idx.IsDir()
	}
	return true
}

// portHandler 是单个端口的 HTTP 处理链（仅配置了 routes 或
// web_root 的端口启用；纯透传端口不做任何 HTTP 处理）：
//
//  1. /hello 且用户未配置同名路由：内置欢迎页；
//  2. routes 前缀命中（最长前缀优先）：反代到命中后端；
//  3. web_root 配置且文件存在：伺服静态文件（index.html 为目录索引）；
//  4. 回退：反代到该端口默认后端。
type portHandler struct {
	router    *proxy.Router // 路由表（nil 表示无路由）
	proxy     http.Handler  // 反代处理器（含默认后端回退）
	static    http.Handler  // web_root 静态伺服（nil 表示未配置）
	root      string        // web_root 目录（供 staticExists 判断）
	hello     http.Handler
	userHello bool // 用户 routes 显式配置了 /hello
}

func newPortHandler(router *proxy.Router, proxyH http.Handler, staticH http.Handler, root string, userHello bool) *portHandler {
	return &portHandler{
		router:    router,
		proxy:     proxyH,
		static:    staticH,
		root:      root,
		hello:     newHelloHandler(),
		userHello: userHello,
	}
}

// staticRoot 返回静态站点根目录（已配置时为绝对路径）。
func (h *portHandler) staticRoot() string { return h.root }

func (h *portHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1) 内置欢迎页（用户配置 /hello 路由时由路由接管）。
	if r.URL.Path == helloPath && !h.userHello {
		h.hello.ServeHTTP(w, r)
		return
	}

	// 2) routes 前缀命中：反代（最长前缀优先）。
	if h.router != nil {
		if _, matched := h.router.Match(r.URL.Path); matched {
			h.proxy.ServeHTTP(w, r)
			return
		}
	}

	// 3) web_root 静态文件（文件存在才伺服；不存在走回退反代，
	// 保持"静态站点 + API 后端"混合部署）。
	if h.static != nil && staticExists(h.staticRoot(), r.URL.Path) {
		h.static.ServeHTTP(w, r)
		return
	}

	// 4) 回退：反代到该端口默认后端。
	h.proxy.ServeHTTP(w, r)
}
