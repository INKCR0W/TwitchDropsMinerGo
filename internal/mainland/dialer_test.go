package mainland

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 用测试 TLS server 的证书池 + host 覆盖, 验证 dial 全链路(不依赖真实网络)
func TestDialHandshakesAndVerifies(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	d := newTestDialer(t, srv, map[string]dohResult{
		"example.com": {IPs: []string{"127.0.0.1"}, TTL: 300},
	})
	// dial 用 host:port 形式; 覆盖端口为测试端口
	conn, err := d.dial(context.Background(), net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("dial 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()
}

func TestDialFallsBackAcrossIPs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	// 第一个 IP 是黑洞(不可连), 第二个是测试 server, 应回退成功
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
	if _, ok := d.resolver.cache["dead.test"]; ok {
		t.Fatal("全部 IP 失败后应清除该 host 的缓存")
	}
}

// 用同一 server 挂在多个回环 IP 上, 制造"多个 IP 都能成功握手"的竞速场景;
// 断言首个胜出者返回后, 落败者的连接最终都被关闭(server 侧净开连接数收敛到 1), 不泄漏
func TestDialRaceClosesLoserConnections(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)

	var openConns int32
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			atomic.AddInt32(&openConns, 1)
		case http.StateClosed, http.StateHijacked:
			atomic.AddInt32(&openConns, -1)
		}
	}

	// 监听所有接口, 这样 127.0.0.1/.2/.3 都能连到同一个 server 端口, 三个 IP 皆可成功握手
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	_ = srv.Listener.Close()
	srv.Listener = l
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	d := newTestDialer(t, srv, map[string]dohResult{
		"example.com": {IPs: []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}, TTL: 300},
	})
	conn, err := d.dial(context.Background(), net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("dial 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 给后台排空 goroutine 一点时间关闭落败连接
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := atomic.LoadInt32(&openConns); got == 1 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("竞速落败的连接未被关闭, server 侧剩余打开连接数 %d", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDialUsesBenignSNIButVerifiesRealHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	d := newTestDialer(t, srv, map[string]dohResult{
		"example.com": {IPs: []string{"127.0.0.1"}, CNAMEs: []string{"cdn.example.com"}, TTL: 300},
	})
	conn, err := d.dial(context.Background(), net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("dial 失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := conn.ConnectionState().ServerName; got != "cdn.example.com" {
		t.Fatalf("上送的 SNI 应为良性 CNAME cdn.example.com, 实际 %q", got)
	}
}

// 证明 DoH client 按端点复用: 两次不同域名的解析都经真实 dohGet -> dohGetVia 路径,
// 但底层只应新建一条到 DoH 端点的连接(第二次查询复用第一次的连接), 而非每次都重新握手
func TestDoHClientReusedAcrossQueries(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		_, _ = io.WriteString(w, `{"Answer":[{"name":"`+name+`","type":1,"data":"203.0.113.1","TTL":300}]}`)
	})
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)

	const endpoint = "doh-test.internal"
	cert, pool := selfSignedServerCert(t, endpoint)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}

	var newConns int32
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	// 临时替换包级端点表, 指向本地测试 server; 该变量只在此测试中读写, 未标 t.Parallel
	origEndpoints, origFallback := dohEndpoints, dohFallbackIPs
	dohEndpoints = []string{endpoint}
	dohFallbackIPs = map[string][]string{endpoint: {"127.0.0.1"}}
	defer func() { dohEndpoints, dohFallbackIPs = origEndpoints, origFallback }()

	d := New(slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	d.testRoots = pool
	d.testDoHPort = port

	if _, err := d.resolver.resolve(context.Background(), "host-a.example"); err != nil {
		t.Fatalf("解析 host-a 失败: %v", err)
	}
	if _, err := d.resolver.resolve(context.Background(), "host-b.example"); err != nil {
		t.Fatalf("解析 host-b 失败: %v", err)
	}

	if got := atomic.LoadInt32(&newConns); got != 1 {
		t.Fatalf("两次不同域名的 DoH 查询应复用同一条端点连接, 实际新建连接数 %d", got)
	}
}

// dohGetVia 对非 200 状态码的处理专为让 dohGet 回退到下一端点; 用两个 stub 端点验证:
// 第一个返回 500, 第二个返回有效应答, 断言最终拿到第二个端点的答案.
// 两个端点共用同一物理 server(证书对两个域名都有效), 因为 testDoHPort 是全局单一端口的测试注入点
func TestDoHFailsOverToSecondEndpoint(t *testing.T) {
	const badEndpoint, goodEndpoint = "doh-bad.internal", "doh-good.internal"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == badEndpoint {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"Answer":[{"name":"good.example","type":1,"data":"203.0.113.9","TTL":300}]}`)
	})
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	cert, pool := selfSignedServerCert(t, badEndpoint, goodEndpoint)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))

	// 临时替换包级端点表, 两个端点都指向同一测试 server; 该变量只在此测试中读写, 未标 t.Parallel
	origEndpoints, origFallback := dohEndpoints, dohFallbackIPs
	dohEndpoints = []string{badEndpoint, goodEndpoint}
	dohFallbackIPs = map[string][]string{badEndpoint: {"127.0.0.1"}, goodEndpoint: {"127.0.0.1"}}
	defer func() { dohEndpoints, dohFallbackIPs = origEndpoints, origFallback }()

	d := New(slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	d.testRoots = pool
	d.testDoHPort = port

	res, err := d.resolver.resolve(context.Background(), "good.example")
	if err != nil {
		t.Fatalf("应回退到第二端点成功: %v", err)
	}
	if len(res.IPs) != 1 || res.IPs[0] != "203.0.113.9" {
		t.Fatalf("应返回第二(有效)端点的应答, 实际 %#v", res.IPs)
	}
}

func selfSignedServerCert(t *testing.T, hosts ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: hosts[0]},
		DNSNames:              hosts,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func newTestDialer(t *testing.T, srv *httptest.Server, stub map[string]dohResult) *Dialer {
	t.Helper()
	d := New(slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
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
