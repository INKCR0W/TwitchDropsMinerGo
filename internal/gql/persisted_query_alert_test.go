package gql

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientLogsPersistedQueryHashExpiredAfterRetry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"PersistedQueryNotFound"}],"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	var logs bytes.Buffer
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Logger:     slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})),
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	if _, err := client.Do(context.Background(), MustLookup(OperationInventory)); !IsRequestError(err) {
		t.Fatalf("期望返回 RequestError，实际为 %v", err)
	}

	recorded := logs.String()
	if !strings.Contains(recorded, "persisted query hash 已失效") {
		t.Fatalf("缺少 hash 失效告警: %s", recorded)
	}
	if !strings.Contains(recorded, "operation=Inventory") {
		t.Fatalf("告警未带上 operationName: %s", recorded)
	}
}

func TestClientDoesNotLogHashExpiredWhenRetrySucceeds(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"errors":[{"message":"PersistedQueryNotFound"}],"extensions":{"operationName":"Inventory"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true},"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	var logs bytes.Buffer
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
		Limiter:    &stubLimiter{},
		Logger:     slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})),
		Sleep: func(context.Context, time.Duration) error {
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
	if strings.Contains(logs.String(), "persisted query hash 已失效") {
		t.Fatalf("重试成功时不应告警: %s", logs.String())
	}
}
