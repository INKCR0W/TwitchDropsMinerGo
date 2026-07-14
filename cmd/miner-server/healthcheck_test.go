package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/app"
)

func TestHealthcheckState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	healthyState := app.RuntimeState{
		Healthy:   true,
		UpdatedAt: now.Add(-time.Minute),
		Auth:      app.AuthStatus{LoggedIn: true},
	}

	cases := []struct {
		name    string
		mutate  func(*app.RuntimeState)
		wantErr string
	}{
		{name: "健康", mutate: func(*app.RuntimeState) {}, wantErr: ""},
		{name: "心跳停止", mutate: func(s *app.RuntimeState) {
			s.UpdatedAt = now.Add(-6 * time.Minute)
		}, wantErr: "心跳"},
		{name: "等待登录", mutate: func(s *app.RuntimeState) {
			s.Healthy = false
			s.Auth.LoggedIn = false
		}, wantErr: "登录"},
		{name: "调度错误", mutate: func(s *app.RuntimeState) {
			s.Healthy = false
			s.Schedule.LastError = "持续失败"
		}, wantErr: "持续失败"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := healthyState
			tc.mutate(&state)
			err := healthcheckState(state, now)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应判定健康: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息应包含 %q: %v", tc.wantErr, err)
			}
		})
	}
}

func TestRunHealthcheckMissingStateFile(t *testing.T) {
	t.Parallel()

	if code := runHealthcheck(filepath.Join(t.TempDir(), "runtime")); code != 1 {
		t.Fatalf("状态文件缺失应返回 1: %d", code)
	}
}
