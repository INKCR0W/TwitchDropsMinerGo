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
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestClientDoBatchParsesListResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"data":{"one":1},"extensions":{"operationName":"Inventory"}},{"data":{"two":2},"extensions":{"operationName":"ViewerDropsDashboard"}}]`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	responses, err := client.DoBatch(context.Background(), []Operation{
		MustLookup(OperationInventory),
		MustLookup(OperationCampaigns),
	})
	if err != nil {
		t.Fatalf("DoBatch 返回错误: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("批量响应数量不匹配: %d", len(responses))
	}
}

func TestClientDoBatchRejectsMismatchedResponseCount(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"data":{"one":1},"extensions":{"operationName":"Inventory"}}]`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.DoBatch(context.Background(), []Operation{
		MustLookup(OperationInventory),
		MustLookup(OperationCampaigns),
	})
	if err == nil {
		t.Fatal("batch 响应数量不一致应返回错误")
	}
	if !strings.Contains(err.Error(), "batch 响应数量不匹配") {
		t.Fatalf("错误信息不匹配: %v", err)
	}
}

func TestClientDoBatchRejectsOperationNameMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[
			{"data":{"one":1},"extensions":{"operationName":"ViewerDropsDashboard"}},
			{"data":{"two":2},"extensions":{"operationName":"Inventory"}}
		]`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.DoBatch(context.Background(), []Operation{
		MustLookup(OperationInventory),
		MustLookup(OperationCampaigns),
	})
	if err == nil {
		t.Fatal("batch operationName 错配应返回错误")
	}
	if !strings.Contains(err.Error(), "operationName 不匹配") {
		t.Fatalf("错误信息不匹配: %v", err)
	}
}

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

func TestClientTurnsServerErrorPathIntoNil(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"currentUser":{"dropCampaigns":[{"id":"1"},{"id":"2"}]}},
			"errors":[{"message":"server error","path":["currentUser","dropCampaigns",1]}],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.Do(context.Background(), MustLookup(OperationCampaigns))
	if err != nil {
		t.Fatalf("Do 返回错误: %v", err)
	}

	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("响应 data 类型不匹配: %T", response.Data)
	}
	currentUser, ok := data["currentUser"].(map[string]any)
	if !ok {
		t.Fatalf("currentUser 类型不匹配: %T", data["currentUser"])
	}
	dropCampaigns, ok := currentUser["dropCampaigns"].([]any)
	if !ok {
		t.Fatalf("dropCampaigns 类型不匹配: %T", currentUser["dropCampaigns"])
	}
	if dropCampaigns[1] != nil {
		t.Fatalf("server error 路径未被置空: %#v", dropCampaigns)
	}
}

func TestClientReturnsErrorWhenServerErrorIsMixedWithUnknownError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"currentUser":{"dropCampaigns":[{"id":"1"},{"id":"2"}]}},
			"errors":[
				{"message":"server error","path":["currentUser","dropCampaigns",1]},
				{"message":"Unauthorized"}
			],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationCampaigns))
	if !IsRequestError(err) {
		t.Fatalf("混合未知错误应返回 RequestError，实际为 %v", err)
	}
}

func TestClientNullifiesAllServerErrorPaths(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"currentUser":{"dropCampaigns":[{"id":"1"},{"id":"2"},{"id":"3"}]}},
			"errors":[
				{"message":"server error","path":["currentUser","dropCampaigns",0]},
				{"message":"server error","path":["currentUser","dropCampaigns",2]}
			],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.Do(context.Background(), MustLookup(OperationCampaigns))
	if err != nil {
		t.Fatalf("纯 server error path 应被降级处理: %v", err)
	}
	data := response.Data.(map[string]any)
	currentUser := data["currentUser"].(map[string]any)
	dropCampaigns := currentUser["dropCampaigns"].([]any)
	if dropCampaigns[0] != nil || dropCampaigns[2] != nil {
		t.Fatalf("所有 server error path 都应置空: %#v", dropCampaigns)
	}
}

func TestClientAllowsIntegrityErrorWithPartialData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"rewardCampaignsAvailableToUser":[{"id":"reward-campaign-1"}]},
			"errors":[{"message":"failed integrity check"}],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	response, err := client.Do(context.Background(), MustLookup(OperationCampaigns))
	if err != nil {
		t.Fatalf("带 partial data 的 integrity error 应被放行: %v", err)
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("响应 data 类型不匹配: %T", response.Data)
	}
	if _, ok := data["rewardCampaignsAvailableToUser"]; !ok {
		t.Fatalf("partial data 应保留: %#v", data)
	}
}

func TestClientStillReportsTopLevelErrorWithIntegrityPartialData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"rewardCampaignsAvailableToUser":[]},
			"errors":[{"message":"failed integrity check"}],
			"error":"Unauthorized",
			"message":"missing token",
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationCampaigns))
	if !IsRequestError(err) {
		t.Fatalf("即使 integrity error 可降级，顶层 error 仍应失败，实际为 %v", err)
	}
}

func TestClientRejectsIntegrityErrorWithoutData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"errors":[{"message":"failed integrity check"}],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationCampaigns))
	if !IsRequestError(err) {
		t.Fatalf("无 data 的 integrity error 仍应失败，实际为 %v", err)
	}
}

func TestClientRejectsIntegrityErrorMixedWithUnknownError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"data":{"rewardCampaignsAvailableToUser":[]},
			"errors":[{"message":"failed integrity check"},{"message":"server rejected request"}],
			"extensions":{"operationName":"ViewerDropsDashboard"}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: newTestHTTPClient(t),
		Endpoint:   server.URL,
	})
	if err != nil {
		t.Fatalf("创建 GQL 客户端失败: %v", err)
	}

	_, err = client.Do(context.Background(), MustLookup(OperationCampaigns))
	if !IsRequestError(err) {
		t.Fatalf("混合未知错误不应被 integrity partial-data 规则吞掉，实际为 %v", err)
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
