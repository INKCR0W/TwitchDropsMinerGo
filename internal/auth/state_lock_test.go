package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
)

func TestValidateDoesNotHoldStateLockDuringDeviceCodeHandler(t *testing.T) {
	t.Parallel()

	deviceStarted := make(chan struct{})
	releaseDevice := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device-1", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/device":
			_, _ = w.Write([]byte(`{"device_code":"device-code","user_code":"ABCD","verification_uri":"https://example.com","interval":1,"expires_in":600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := httpclient.New(httpclient.Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: filepath.Join(t.TempDir(), "cookies.json"),
		ClientInfo: httpclient.ClientInfo{
			ClientURL: server.URL,
			ClientID:  "client-id",
			UserAgent: "test-agent",
		},
	})
	if err != nil {
		t.Fatalf("创建 HTTP client 失败: %v", err)
	}

	state, err := New(Options{
		HTTPClient:       client,
		ClientInfo:       httpclient.ClientInfo{ClientURL: server.URL, ClientID: "client-id", UserAgent: "test-agent"},
		DeviceEndpoint:   server.URL + "/device",
		TokenEndpoint:    server.URL + "/token",
		ValidateEndpoint: server.URL + "/validate",
		DeviceCodeHandler: func(context.Context, DeviceCode) error {
			close(deviceStarted)
			<-releaseDevice
			return context.Canceled
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- state.Validate(context.Background()) }()

	select {
	case <-deviceStarted:
	case <-time.After(time.Second):
		t.Fatal("Validate 未进入 device code handler")
	}

	snapshotDone := make(chan Snapshot, 1)
	go func() { snapshotDone <- state.Snapshot() }()
	select {
	case <-snapshotDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Snapshot 不应被 device code handler 长时间阻塞")
	}

	close(releaseDevice)
	<-done
}
