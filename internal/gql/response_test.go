package gql

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
