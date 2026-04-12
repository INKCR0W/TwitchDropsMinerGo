package auth

import (
	"context"
	"testing"
)

func TestStateHeadersProviderReturnsAuthenticatedGQLHeaders(t *testing.T) {
	t.Parallel()

	fixture := newValidatedDeviceCodeState(t)

	headers, err := fixture.state.HeadersProvider(HeadersOptions{GQL: true})(context.Background())
	if err != nil {
		t.Fatalf("HeadersProvider 返回错误: %v", err)
	}
	if got := headers.Get("Authorization"); got != "OAuth token-1" {
		t.Fatalf("Authorization 请求头不匹配: %q", got)
	}
	if got := headers.Get("X-Device-Id"); got != "device-1" {
		t.Fatalf("X-Device-Id 请求头不匹配: %q", got)
	}
	if got := headers.Get("Client-Session-Id"); got != "0123456789abcdef" {
		t.Fatalf("Client-Session-Id 请求头不匹配: %q", got)
	}
}
