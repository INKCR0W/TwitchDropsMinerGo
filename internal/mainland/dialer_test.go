package mainland

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 用测试 TLS server 的证书池 + host 覆盖, 验证 dial 全链路(不依赖真实网络).
func TestDialHandshakesAndVerifies(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	d := newTestDialer(t, srv, map[string]dohResult{
		"example.com": {IPs: []string{"127.0.0.1"}, TTL: 300},
	})
	// dial 用 host:port 形式; 覆盖端口为测试端口.
	conn, err := d.dial(context.Background(), net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("dial 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()
}

func TestDialFallsBackAcrossIPs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	// 第一个 IP 是黑洞(不可连), 第二个是测试 server, 应回退成功.
	d := newTestDialer(t, srv, map[string]dohResult{
		"example.com": {IPs: []string{"192.0.2.1", "127.0.0.1"}, TTL: 300},
	})
	conn, err := d.dial(context.Background(), net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("多 IP 兜底应成功: %v", err)
	}
	_ = conn.Close()
}

func TestDialAllIPsFailInvalidates(t *testing.T) {
	t.Parallel()
	d := newTestDialer(t, nil, map[string]dohResult{
		"dead.test": {IPs: []string{"192.0.2.1"}, TTL: 300},
	})
	_, err := d.dial(context.Background(), "dead.test:1")
	if err == nil {
		t.Fatal("全部 IP 失败应返回错误")
	}
	if !strings.Contains(err.Error(), "dead.test") {
		t.Fatalf("错误应点名域名, 实际: %v", err)
	}
}

func newTestDialer(t *testing.T, srv *httptest.Server, stub map[string]dohResult) *Dialer {
	t.Helper()
	d := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.resolver = newResolver(func(ctx context.Context, url string) ([]byte, error) {
		return nil, nil // 不走真实 DoH
	}, func() time.Time { return time.Unix(0, 0) })
	// 直接塞缓存, 绕过 httpGet
	for host, res := range stub {
		d.resolver.cache[host] = cacheEntry{result: res, expires: time.Unix(1<<62, 0)}
	}
	if srv != nil {
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		d.testRoots = pool
	}
	return d
}
