package gql

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
)

type stubLimiter struct {
	calls atomic.Int32
	err   error
}

func (l *stubLimiter) Wait(context.Context) error {
	l.calls.Add(1)
	return l.err
}

func newTestHTTPClient(t *testing.T) *httpclient.Client {
	t.Helper()

	client, err := httpclient.New(httpclient.Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		ClientInfo: httpclient.ClientInfo{
			ClientURL: "https://www.twitch.tv",
			ClientID:  "client-id",
			UserAgent: "unit-test-agent",
		},
	})
	if err != nil {
		t.Fatalf("创建 HTTP 客户端失败: %v", err)
	}

	return client
}

func TestClientDoAddsHeadersAndReturnsResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("请求方法不正确: %s", r.Method)
		}
		if got := r.Header.Get("Client-Id"); got != "client-id" {
			t.Fatalf("Client-Id 不匹配: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "OAuth token-1" {
			t.Fatalf("Authorization 不匹配: %q", got)
		}
		if got := r.Header.Get("X-Device-Id"); got != "device-1" {
			t.Fatalf("X-Device-Id 不匹配: %q", got)
		}
		if got := r.Header.Get("Origin"); got != "https://www.twitch.tv" {
			t.Fatalf("Origin 不匹配: %q", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取请求体失败: %v", err)
		}

		var operation Operation
		if err := json.Unmarshal(body, &operation); err != nil {
			t.Fatalf("请求体 JSON 不合法: %v", err)
		}
		if operation.OperationName != "Inventory" {
			t.Fatalf("operationName 不匹配: %q", operation.OperationName)
		}

		_, _ = io.WriteString(w, `{"data":{"currentUser":{"inventory":{}}},"extensions":{"operationName":"Inventory"}}`)
	}))
	defer server.Close()

	limiter := &stubLimiter{}
	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		ClientInfo: httpclient.ClientInfo{
			ClientURL: "https://www.twitch.tv",
			ClientID:  "client-id",
			UserAgent: "unit-test-agent",
		},
		HeadersProvider: func(context.Context) (http.Header, error) {
			headers := make(http.Header)
			headers.Set("Authorization", "OAuth token-1")
			headers.Set("X-Device-Id", "device-1")
			return headers, nil
		},
		Limiter:  limiter,
		Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.Do(context.Background(), MustLookup(OperationInventory))
	if err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}
	if limiter.calls.Load() != 1 {
		t.Fatalf("限流器调用次数不匹配: %d", limiter.calls.Load())
	}

	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("响应 data 类型不匹配: %T", response.Data)
	}
	if _, ok := data["currentUser"]; !ok {
		t.Fatalf("响应 data 缺少 currentUser: %#v", data)
	}
}

func TestClientDoRawSendsInlineQuery(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"data":{"sendSpadeEvents":{"statusCode":204}}}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.DoRaw(context.Background(), RawQuery{
		Query:     "mutation SendEvents { sendSpadeEvents }",
		Variables: map[string]any{"input": map[string]any{"data": "abc"}},
	})
	if err != nil {
		t.Fatalf("DoRaw 返回错误: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("请求体 JSON 不合法: %v", err)
	}
	if sent["query"] != "mutation SendEvents { sendSpadeEvents }" {
		t.Fatalf("query 不匹配: %#v", sent["query"])
	}
	if _, ok := sent["extensions"]; ok {
		t.Fatalf("内联查询不应带 persistedQuery extensions: %#v", sent)
	}

	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("响应 data 类型不匹配: %T", response.Data)
	}
	events, ok := data["sendSpadeEvents"].(map[string]any)
	if !ok || events["statusCode"] != float64(204) {
		t.Fatalf("statusCode 解析失败: %#v", data)
	}
}

func TestClientHandlesGzipResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write([]byte(`{"data":{"ok":true},"extensions":{"operationName":"Inventory"}}`)); err != nil {
			t.Fatalf("写入 gzip 内容失败: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("关闭 gzip writer 失败: %v", err)
		}

		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buffer.Bytes())
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.Do(context.Background(), MustLookup(OperationInventory))
	if err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}

	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("响应 data 类型不匹配: %T", response.Data)
	}
	if value, ok := data["ok"].(bool); !ok || !value {
		t.Fatalf("gzip 响应未正确解析: %#v", data)
	}
}

func TestClientPropagatesHeaderProviderError(t *testing.T) {
	t.Parallel()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		HeadersProvider: func(context.Context) (http.Header, error) {
			return nil, errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationInventory))
	if err == nil || err.Error() != "构造 GQL 请求头失败: boom" {
		t.Fatalf("期望请求头错误透传，实际为 %v", err)
	}
}
