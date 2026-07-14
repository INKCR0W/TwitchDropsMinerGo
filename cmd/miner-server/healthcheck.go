package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/runtime"
)

const healthcheckStaleAfter = 5 * time.Minute

func runHealthcheck(runtimeDir string) int {
	layout, err := runtime.ResolveLayout(runtimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析运行目录失败: %v\n", err)
		return 1
	}

	raw, err := os.ReadFile(layout.StateFile)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(layout.StateFile + ".bak")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取状态文件失败: %v\n", err)
		return 1
	}

	var state app.RuntimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		fmt.Fprintf(os.Stderr, "解析状态文件失败: %v\n", err)
		return 1
	}

	if err := healthcheckState(state, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func healthcheckState(state app.RuntimeState, now time.Time) error {
	if now.Sub(state.UpdatedAt) > healthcheckStaleAfter {
		return fmt.Errorf("状态心跳停止: updated_at=%s", state.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if state.Healthy {
		return nil
	}

	reason := strings.TrimSpace(state.Schedule.LastError)
	if !state.Auth.LoggedIn {
		reason = "等待 Twitch 登录授权"
	}
	if reason == "" {
		reason = strings.TrimSpace(state.LastError)
	}
	if reason == "" {
		reason = "未知原因"
	}
	return fmt.Errorf("不健康: %s", reason)
}
