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
	"time"
)

const dialTimeout = 10 * time.Second

var (
	dohEndpoints   = []string{"cloudflare-dns.com", "doh.opendns.com"}
	dohFallbackIPs = map[string][]string{
		"cloudflare-dns.com": {"104.16.248.249", "104.16.249.249"},
	}
)

type Dialer struct {
	logger    *slog.Logger
	resolver  *resolver
	testRoots *x509.CertPool // 仅测试注入; 生产为 nil(系统根)
}

func New(logger *slog.Logger) *Dialer {
	d := &Dialer{logger: logger}
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

func (d *Dialer) NetDialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
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
	var lastErr error
	for _, ip := range res.IPs {
		conn, hErr := d.tlsHandshake(ctx, net.JoinHostPort(ip, port), host, sni)
		if hErr == nil {
			return conn, nil
		}
		lastErr = hErr
		d.logger.Debug("大陆模式握手失败, 尝试下一 IP", "host", host, "ip", ip, "sni", sni, "error", hErr)
	}
	d.resolver.invalidate(host)
	return nil, fmt.Errorf("大陆模式: %s 全部 IP 失败(sni=%q): %w", host, sni, lastErr)
}

func (d *Dialer) tlsHandshake(ctx context.Context, ipPort, host, sni string) (*tls.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
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

// dohGet 把一次 DoH 查询打到某个 fronted 端点(空 SNI + 对端点自校验).
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
	ips := d.resolveEndpoint(ctx, endpoint)
	if len(ips) == 0 {
		return nil, fmt.Errorf("解析 DoH 端点 %s 失败", endpoint)
	}
	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, ip := range ips {
				conn, err := d.tlsHandshake(ctx, net.JoinHostPort(ip, "443"), endpoint, "")
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, fmt.Errorf("连接 DoH 端点 %s 失败: %w", endpoint, lastErr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: dialTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH 端点 %s 返回 %s", endpoint, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64*1024))
}

// resolveEndpoint 用系统解析器解析 DoH 端点(未被污染); 失败回退兜底 IP.
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
