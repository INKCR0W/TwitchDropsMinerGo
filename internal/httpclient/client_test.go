package httpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

func TestNewClampsConnectionQuality(t *testing.T) {
	t.Parallel()

	client, err := New(Options{
		Settings: config.Settings{
			ConnectionQuality: 9,
		},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if client.ConnectTimeout() != 30*time.Second {
		t.Fatalf("ConnectTimeout 不匹配: %v", client.ConnectTimeout())
	}
	if client.RequestTimeout() != 60*time.Second {
		t.Fatalf("RequestTimeout 不匹配: %v", client.RequestTimeout())
	}
}

func TestClientRetriesServerErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current <= 2 {
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}

		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	var sleeps []time.Duration
	client, err := New(Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		Backoff: BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Minute,
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    server.URL,
	})
	if err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}

	if response.StatusCode != http.StatusOK || response.Text() != "ok" {
		t.Fatalf("响应不匹配: %+v", response)
	}
	if attempts.Load() != 3 {
		t.Fatalf("期望请求 3 次，实际为 %d", attempts.Load())
	}
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != 2*time.Second {
		t.Fatalf("退避序列不匹配: %#v", sleeps)
	}
}

func TestClientStopsRetryingAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "still failing", http.StatusBadGateway)
	}))
	defer server.Close()

	client, err := New(Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		Backoff: BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Millisecond,
		},
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	_, err = client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    server.URL,
	})
	if err == nil {
		t.Fatal("持续 5xx 后应返回错误")
	}
	if got := attempts.Load(); got != int32(DefaultMaxAttempts) {
		t.Fatalf("重试次数不匹配: got=%d want=%d", got, DefaultMaxAttempts)
	}
	if !strings.Contains(err.Error(), "达到最大重试次数") {
		t.Fatalf("错误信息应说明达到最大重试次数: %v", err)
	}
}

func TestClientDoesNotRetryCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := New(Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("context 已取消时不应进入 Sleep 重试")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	_, err = client.Do(ctx, Request{
		Method: http.MethodGet,
		URL:    "http://127.0.0.1:1",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际为 %v", err)
	}
}

func TestClientLogsRetryDetailsWithoutQueryString(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "retry", http.StatusBadGateway)
	}))
	defer server.Close()

	var logBuffer strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

	client, err := New(Options{
		Logger:      logger,
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		Backoff: BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Minute,
		},
		Sleep: func(context.Context, time.Duration) error {
			return context.Canceled
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	_, err = client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    server.URL + "/foo?token=secret",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 sleep 中断请求，实际为 %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("期望仅请求 1 次，实际为 %d", attempts.Load())
	}

	logs := logBuffer.String()
	if !strings.Contains(logs, "HTTP 请求失败，准备退避重试") {
		t.Fatalf("缺少重试日志: %q", logs)
	}
	if !strings.Contains(logs, "status_code=502") {
		t.Fatalf("缺少状态码日志: %q", logs)
	}
	if strings.Contains(logs, "token=secret") {
		t.Fatalf("日志不应包含查询参数: %q", logs)
	}
	if !strings.Contains(logs, "/foo") {
		t.Fatalf("日志缺少路径信息: %q", logs)
	}
}

func TestClientUsesConfiguredProxy(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxy.Close()

	client, err := New(Options{
		Settings: config.Settings{
			ConnectionQuality: 1,
			Proxy:             proxy.URL,
		},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    target.URL,
	})
	if err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}

	if response.Text() != "proxied" {
		t.Fatalf("期望命中代理响应，实际为 %q", response.Text())
	}
	if proxyHits.Load() == 0 {
		t.Fatal("期望代理收到请求")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("目标服务不应直接收到请求，实际为 %d", targetHits.Load())
	}
}

func TestClientInvalidatesExpiredRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 3, 0, 0, 0, time.UTC)
	client, err := New(Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		Clock: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	_, err = client.Do(context.Background(), Request{
		Method:          http.MethodGet,
		URL:             "https://example.com",
		InvalidateAfter: now.Add(9 * time.Second),
	})
	if !errors.Is(err, ErrRequestInvalid) {
		t.Fatalf("期望返回 ErrRequestInvalid，实际为 %v", err)
	}
}
