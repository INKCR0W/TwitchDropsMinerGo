package mainland

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoHEvictionClosesIdleConnections(t *testing.T) {
	const endpoint = "doh-lifecycle.internal"
	var open atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	cert, roots := selfSignedServerCert(t, endpoint)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed:
			open.Add(-1)
		}
	}
	srv.StartTLS()
	defer srv.Close()
	original := dohFallbackIPs
	dohFallbackIPs = map[string][]string{endpoint: {"127.0.0.1"}}
	defer func() { dohFallbackIPs = original }()
	d := New(nil, 1)
	t.Cleanup(d.Close)
	d.testRoots = roots
	_, d.testDoHPort, _ = net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	for range 5 {
		if _, err := d.dohGetVia(context.Background(), endpoint, "/dns-query"); err == nil {
			t.Fatal("500 response should fail")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := open.Load(); got != 0 {
		t.Fatalf("evicted transports retained %d connections", got)
	}
}

func TestDoHEvictionPreservesReplacementClient(t *testing.T) {
	oldTransport := &closeTrackingTransport{}
	newTransport := &closeTrackingTransport{}
	oldClient := &http.Client{Transport: oldTransport}
	newClient := &http.Client{Transport: newTransport}
	d := New(nil, 1)
	d.dohClients["endpoint"] = newClient
	d.evictDoHClient("endpoint", oldClient)
	if d.dohClients["endpoint"] != newClient || oldTransport.closed.Load() != 1 || newTransport.closed.Load() != 0 {
		t.Fatal("late failure should close only the old client's transport")
	}
	d.Close()
	d.Close()
	if newTransport.closed.Load() != 1 {
		t.Fatal("close should release the cached replacement once")
	}
	if _, err := d.dohClientFor(context.Background(), "endpoint"); err == nil {
		t.Fatal("closed dialer should reject new clients")
	}
	if _, err := d.DialTLSContext(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Fatal("closed dialer should reject new connections")
	}
}

func TestDoHCloseReleasesHealthyIdleConnections(t *testing.T) {
	var open, created atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			created.Add(1)
			open.Add(1)
		case http.StateClosed:
			open.Add(-1)
		}
	}
	srv.StartTLS()
	defer srv.Close()
	endpoint := strings.TrimPrefix(srv.URL, "https://")
	d := New(nil, 1)
	t.Cleanup(d.Close)
	d.dohClients[endpoint] = srv.Client()
	for range 5 {
		if _, err := d.dohGetVia(context.Background(), endpoint, "/dns-query"); err != nil {
			t.Fatal(err)
		}
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("healthy requests should reuse one connection, created=%d", got)
	}
	d.Close()
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := open.Load(); got != 0 {
		t.Fatalf("closed dialer retained %d healthy connections", got)
	}
}

type closeTrackingTransport struct {
	closed atomic.Int32
}

func (t *closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("unexpected network request")
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closed.Add(1)
}
