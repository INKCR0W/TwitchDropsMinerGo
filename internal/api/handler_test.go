package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

func TestHealthzReturnsOK(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC)
	handler := newTestHandler(t, testHandlerOptions{
		now: now,
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码错误: %d", response.Code)
	}

	var payload HealthResponse
	decodeJSON(t, response.Body.Bytes(), &payload)
	if payload.Status != "ok" || !payload.Time.Equal(now) {
		t.Fatalf("healthz 响应不匹配: %#v", payload)
	}
}

func TestStatusIncludesMetricsAndSanitizedSettings(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC)
	handler := newTestHandler(t, testHandlerOptions{
		now: now,
		status: StatusResponse{
			Healthy: true,
			Settings: config.Settings{
				Proxy: "http://user:pass@proxy.example.com:8080",
			},
		},
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码错误: %d", response.Code)
	}

	var payload StatusResponse
	decodeJSON(t, response.Body.Bytes(), &payload)
	if payload.Settings.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("status 设置脱敏失败: %q", payload.Settings.Proxy)
	}
	if payload.API.TotalRequests != 0 {
		t.Fatalf("/status 响应中的 API 指标应反映请求前快照: %#v", payload.API)
	}

	metrics := handler.metricsSnapshot()
	if metrics.TotalRequests != 1 || metrics.Routes["GET /status"] != 1 {
		t.Fatalf("请求指标未累计: %#v", metrics)
	}
}

func TestGetSettingsSanitizesProxyCredentials(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, testHandlerOptions{
		currentSettings: config.Settings{
			Proxy: "http://user:pass@proxy.example.com:8080",
		},
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/settings", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码错误: %d", response.Code)
	}

	var payload config.Settings
	decodeJSON(t, response.Body.Bytes(), &payload)
	if payload.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("settings 脱敏失败: %q", payload.Proxy)
	}
}

func TestPutSettingsValidatesAndPersists(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC)
	var stored config.Settings
	handler := newTestHandler(t, testHandlerOptions{
		now: now,
		updateSettings: func(settings config.Settings) (config.Settings, error) {
			stored = settings.Clone()
			return settings, nil
		},
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"language":"简体中文","connection_quality":2}`))
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码错误: %d body=%s", response.Code, response.Body.String())
	}
	if stored.Language != "简体中文" || stored.ConnectionQuality != 2 {
		t.Fatalf("更新回调未收到配置: %#v", stored)
	}
}

func TestPutSettingsRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	handler := newTestHandler(t, testHandlerOptions{})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"connection_quality":0}`))
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("非法配置应返回 400，实际为 %d", response.Code)
	}
}

func TestReloadRequestsAccepted(t *testing.T) {
	t.Parallel()

	reloaded := false
	handler := newTestHandler(t, testHandlerOptions{
		reload: func() error {
			reloaded = true
			return nil
		},
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/reload", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("状态码错误: %d", response.Code)
	}
	if !reloaded {
		t.Fatal("reload 回调未执行")
	}
}

type testHandlerOptions struct {
	now             time.Time
	status          StatusResponse
	currentSettings config.Settings
	updateSettings  func(config.Settings) (config.Settings, error)
	reload          func() error
}

func newTestHandler(t *testing.T, options testHandlerOptions) *Handler {
	t.Helper()

	if options.now.IsZero() {
		options.now = time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC)
	}

	status := options.status
	handler, err := NewHandler(Options{
		Now: func() time.Time {
			return options.now
		},
		ListenAddress: "127.0.0.1:8080",
		Health: func(_ context.Context) HealthResponse {
			return HealthResponse{
				Status: "ok",
				Time:   options.now,
			}
		},
		Status: func(context.Context) (StatusResponse, error) {
			return status, nil
		},
		CurrentSettings: func(context.Context) (config.Settings, error) {
			return options.currentSettings, nil
		},
		UpdateSettings: func(_ context.Context, settings config.Settings) (config.Settings, error) {
			if options.updateSettings == nil {
				return settings, nil
			}
			return options.updateSettings(settings)
		},
		Reload: func(context.Context) error {
			if options.reload == nil {
				return nil
			}
			return options.reload()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler 返回错误: %v", err)
	}
	return handler
}

func decodeJSON(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("解析 JSON 失败: %v\nbody=%s", err, string(data))
	}
}
