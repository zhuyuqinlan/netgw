package gateway

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// certReloader 实现 TLS 证书热重载：tls.Config 通过 GetCertificate
// 回调在每次 TLS 握手时向其取证书，内部按证书文件的 mtime + size
// 缓存判断是否需要重新加载。
//
// 兼容"先删后写"的证书替换方式：文件不存在或内容损坏（写了一半）
// 时沿用上一次成功加载的旧证书继续服务，等文件恢复完整后的下一次
// 握手再自动加载新证书。
type certReloader struct {
	certFile string
	keyFile  string

	mu   sync.Mutex
	cert *tls.Certificate
	// cachedMtime 与 cachedSize 记录缓存对应文件状态；两者任一
	// 变化都触发重新加载（mtime 粒度粗的文件系统下 size 可补充
	// 区分同秒内的替换）。
	cachedMtime time.Time
	cachedSize  int64
}

// newCertReloader 启动时加载一次证书，加载失败立即报错（启动期
// 就应暴露证书问题，不做延迟加载）。
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetCertificate 供 tls.Config.GetCertificate 使用：按需重载并返回
// 当前证书。
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, err := os.Stat(r.certFile)
	if err != nil {
		// 文件被删（替换窗口期）：沿用旧证书，下次握手重试。
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, fmt.Errorf("gateway: stat cert file: %w", err)
	}
	if r.cert != nil && st.ModTime().Equal(r.cachedMtime) && st.Size() == r.cachedSize {
		return r.cert, nil
	}
	if err := r.reload(); err != nil {
		// 新文件损坏（写了一半）或读取失败：沿用旧证书，下次握手重试。
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, fmt.Errorf("gateway: reload tls cert: %w", err)
	}
	return r.cert, nil
}

// reload 从磁盘加载证书并更新缓存状态。调用方必须已持有 r.mu。
// 加载失败时不改动现有缓存。
func (r *certReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	st, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	r.cert = &cert
	r.cachedMtime = st.ModTime()
	r.cachedSize = st.Size()
	return nil
}
