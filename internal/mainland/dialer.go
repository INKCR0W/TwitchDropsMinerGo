package mainland

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	baseDialTimeout = 5 * time.Second // 单次 IP 尝试上限基准值(quality=1), 随 connection_quality 缩放
	baseDoHTimeout  = 4 * time.Second // 单个 DoH 端点上限基准值(quality=1), 随 connection_quality 缩放
)

var (
	dohEndpoints   = []string{"cloudflare-dns.com", "doh.opendns.com"}
	dohFallbackIPs = map[string][]string{
		"cloudflare-dns.com": {"104.16.248.249", "104.16.249.249"},
	}
)

type Dialer struct {
	logger      *slog.Logger
	resolver    *resolver
	testRoots   *x509.CertPool // 仅测试注入; 生产为 nil(系统根)
	testDoHPort string         // 仅测试注入; 生产为空即用 443

	dialTimeout time.Duration // baseDialTimeout × connection_quality
	dohTimeout  time.Duration // baseDoHTimeout × connection_quality

	dohMu      sync.Mutex
	dohClients map[string]*http.Client // 按端点缓存, 复用底层 TLS 连接
}

// New 按 connectionQuality(1..6, 越大网络越差) 缩放拨号与 DoH 超时
func New(logger *slog.Logger, connectionQuality int) *Dialer {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	quality := connectionQuality
	switch {
	case quality < 1:
		quality = 1
	case quality > 6:
		quality = 6
	}
	d := &Dialer{
		logger:      logger,
		dohClients:  map[string]*http.Client{},
		dialTimeout: baseDialTimeout * time.Duration(quality),
		dohTimeout:  baseDoHTimeout * time.Duration(quality),
	}
	d.resolver = newResolver(d.dohGet, time.Now)
	return d
}

func (d *Dialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (d *Dialer) dial(ctx context.Context, addr string) (*tls.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("解析地址 %q 失败: %w", addr, err)
	}
	res, err := d.resolver.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(res.IPs) == 0 {
		d.resolver.invalidate(host)
		return nil, fmt.Errorf("大陆模式: %s 无 A 记录", host)
	}
	sni := benignSNI(res)
	conn, err := d.dialIPs(ctx, res.IPs, port, host, sni)
	if err != nil {
		d.resolver.invalidate(host)
		return nil, fmt.Errorf("大陆模式: %s 全部 IP 失败(sni=%q): %w", host, sni, err)
	}
	return conn, nil
}

type dialAttempt struct {
	ip   string
	conn *tls.Conn
	err  error
}

// dialIPs 并发竞速 ips 的 TLS 握手, 首个通过验证的连接胜出并立即返回;
// 其余尝试被取消, 若仍产出已建立的连接则由后台 goroutine 排空并关闭, 不阻塞、不泄漏
func (d *Dialer) dialIPs(ctx context.Context, ips []string, port, host, sni string) (*tls.Conn, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	results := make(chan dialAttempt, len(ips))
	for _, ip := range ips {
		go func(ip string) {
			conn, err := d.tlsHandshake(raceCtx, net.JoinHostPort(ip, port), host, sni)
			results <- dialAttempt{ip: ip, conn: conn, err: err}
		}(ip)
	}

	var lastErr error
	for i := 0; i < len(ips); i++ {
		got := <-results
		if got.err != nil {
			lastErr = got.err
			d.logger.Debug("大陆模式握手失败", "host", host, "ip", got.ip, "sni", sni, "error", got.err)
			continue
		}
		cancel()
		go drainDialAttempts(results, len(ips)-i-1)
		return got.conn, nil
	}
	cancel()
	return nil, lastErr
}

// drainDialAttempts 消费剩余握手结果, 关闭竞速落败但仍已建立的连接
func drainDialAttempts(results <-chan dialAttempt, n int) {
	for i := 0; i < n; i++ {
		if got := <-results; got.conn != nil {
			_ = got.conn.Close()
		}
	}
}

func (d *Dialer) tlsHandshake(ctx context.Context, ipPort, host, sni string) (*tls.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, d.dialTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(dctx, "tcp", ipPort)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", ipPort, err)
	}
	cfg := tlsConfigFor(host, sni)
	if d.testRoots != nil {
		roots := d.testRoots
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyPeerCertificates(cs.PeerCertificates, host, roots)
		}
	}
	conn := tls.Client(raw, cfg)
	if deadline, ok := dctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := conn.HandshakeContext(dctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("TLS 握手失败: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// dohGet 把一次 DoH 查询打到某个 fronted 端点(空 SNI + 对端点自校验)
func (d *Dialer) dohGet(ctx context.Context, path string) ([]byte, error) {
	var lastErr error
	for _, endpoint := range dohEndpoints {
		body, err := d.dohGetVia(ctx, endpoint, path)
		if err == nil {
			return body, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("所有 DoH 端点失败: %w", lastErr)
}

func (d *Dialer) dohGetVia(ctx context.Context, endpoint, path string) ([]byte, error) {
	client, err := d.dohClientFor(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		d.evictDoHClient(endpoint)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		d.evictDoHClient(endpoint)
		return nil, fmt.Errorf("DoH 端点 %s 返回 %s", endpoint, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64*1024))
}

// evictDoHClient 清除某端点的缓存 client, 使下次查询重新解析 IP 并重建 transport
func (d *Dialer) evictDoHClient(endpoint string) {
	d.dohMu.Lock()
	delete(d.dohClients, endpoint)
	d.dohMu.Unlock()
}

// dohClientFor 返回按端点缓存的 DoH client, 复用其底层 TLS 连接; 首次调用时解析端点 IP 并建 transport.
// 解析在锁外进行, 避免多个 goroutine 冷启动时都持锁等待网络; 插入前二次检查, 让先完成者的 client 生效
func (d *Dialer) dohClientFor(ctx context.Context, endpoint string) (*http.Client, error) {
	d.dohMu.Lock()
	c, ok := d.dohClients[endpoint]
	d.dohMu.Unlock()
	if ok {
		return c, nil
	}

	ips := d.resolveEndpoint(ctx, endpoint)
	if len(ips) == 0 {
		return nil, fmt.Errorf("解析 DoH 端点 %s 失败", endpoint)
	}
	port := d.testDoHPort
	if port == "" {
		port = "443"
	}
	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, ip := range ips {
				conn, err := d.tlsHandshake(ctx, net.JoinHostPort(ip, port), endpoint, "")
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, fmt.Errorf("连接 DoH 端点 %s 失败: %w", endpoint, lastErr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: d.dohTimeout}

	d.dohMu.Lock()
	defer d.dohMu.Unlock()
	if existing, ok := d.dohClients[endpoint]; ok {
		return existing, nil // 已被并发的另一次调用插入, 丢弃本次新建的 client
	}
	d.dohClients[endpoint] = client
	return client, nil
}

// resolveEndpoint 用系统解析器解析 DoH 端点(未被污染); 失败回退兜底 IP
func (d *Dialer) resolveEndpoint(ctx context.Context, endpoint string) []string {
	if addrs, err := net.DefaultResolver.LookupHost(ctx, endpoint); err == nil {
		var v4 []string
		for _, a := range addrs {
			if net.ParseIP(a).To4() != nil {
				v4 = append(v4, a)
			}
		}
		if len(v4) > 0 {
			return v4
		}
	}
	return dohFallbackIPs[endpoint]
}
