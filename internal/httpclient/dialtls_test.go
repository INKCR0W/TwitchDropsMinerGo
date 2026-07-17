package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

func TestOptionsDialTLSContextIsUsedAndDisablesProxy(t *testing.T) {
	t.Parallel()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
	}))
	defer proxy.Close()

	called := make(chan struct{}, 1)
	client, err := New(Options{
		Settings:    config.Settings{ConnectionQuality: 1, Proxy: proxy.URL},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		MaxAttempts: 1,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			select {
			case called <- struct{}{}:
			default:
			}
			return nil, fmt.Errorf("stub: %s", addr)
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if _, err := client.Do(context.Background(), Request{Method: http.MethodGet, URL: "https://gql.twitch.tv/"}); err == nil {
		t.Fatal("stub dialer 应导致请求失败")
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("DialTLSContext 未被调用")
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("DialTLSContext 非空时代理不应被使用，实际命中 %d 次", proxyHits.Load())
	}
}
