package gql

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

func TestClientRetriesPersistedQueryNotFoundOnce(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current == 1 {
			_, _ = io.WriteString(w, `{"errors":[{"message":"PersistedQueryNotFound"}],"extensions":{"operationName":"Inventory"}}`)
			return
		}

		_, _ = io.WriteString(w, `{"data":{"ok":true},"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	var delays []time.Duration
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Backoff: httpclient.BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Minute,
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	if _, err := client.Do(context.Background(), MustLookup(OperationInventory)); err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("期望请求 2 次，实际为 %d", attempts.Load())
	}
	if len(delays) != 1 || delays[0] != forcedRetryDelay {
		t.Fatalf("重试等待时间不匹配: %#v", delays)
	}
}

func TestClientReturnsRequestErrorAfterSingleRetryIsConsumed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"service error"}],"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationInventory))
	if !IsRequestError(err) {
		t.Fatalf("期望返回 RequestError，实际为 %v", err)
	}
}

func TestClientRetriesServiceUnavailableUntilSuccess(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current <= 2 {
			_, _ = io.WriteString(w, `{"errors":[{"message":"service unavailable"}],"extensions":{"operationName":"Inventory"}}`)
			return
		}

		_, _ = io.WriteString(w, `{"data":{"ok":true},"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	var sleeps atomic.Int32
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Sleep: func(context.Context, time.Duration) error {
			sleeps.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	if _, err := client.Do(context.Background(), MustLookup(OperationInventory)); err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("重试次数不匹配: %d", attempts.Load())
	}
	if sleeps.Load() != 2 {
		t.Fatalf("睡眠次数不匹配: %d", sleeps.Load())
	}
}

func TestClientRetriesRateLimitedResponse(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "rate limited")
			return
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true},"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	var sleeps atomic.Int32
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Sleep: func(context.Context, time.Duration) error {
			sleeps.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	if _, err := client.Do(context.Background(), MustLookup(OperationInventory)); err != nil {
		t.Fatalf("429 应被退避重试而非失败: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("期望请求 2 次，实际为 %d", attempts.Load())
	}
	if sleeps.Load() != 1 {
		t.Fatalf("429 应触发一次退避睡眠，实际为 %d", sleeps.Load())
	}
}

func TestClientStopsRetryingAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.WriteString(w, `{"errors":[{"message":"service unavailable"}],"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient:  newTestHTTPClient(t),
		Endpoint:    server.URL,
		Limiter:     &stubLimiter{},
		MaxAttempts: 3,
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationInventory))
	if !IsRequestError(err) {
		t.Fatalf("重试耗尽应返回 RequestError，实际为 %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("期望重试上限 3 次，实际为 %d", attempts.Load())
	}
}
