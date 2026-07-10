package gql

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
