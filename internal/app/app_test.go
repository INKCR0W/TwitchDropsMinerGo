package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

func TestRunWaitsForCancellation(t *testing.T) {
	t.Parallel()

	store := &memoryStateStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: store,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run 在取消前提前退出: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run 在取消后未及时退出")
	}

	if len(store.savedStates) != 2 {
		t.Fatalf("期望保存 2 次状态，实际为 %d", len(store.savedStates))
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	t.Parallel()

	application, err := New(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	var nilCtx context.Context
	if err := application.Run(nilCtx); err == nil {
		t.Fatal("期望 nil context 返回错误")
	}
}

func TestRunPersistsLifecycleState(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 11, 1, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Minute)
	nowValues := []time.Time{start, stop}
	store := &memoryStateStore{
		state: RuntimeState{
			SchemaVersion: 1,
			RunCount:      4,
		},
	}

	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: store,
		Now: func() time.Time {
			value := nowValues[0]
			nowValues = nowValues[1:]
			return value
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run 返回错误: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Run 在取消后未及时退出")
	}

	if store.state.RunCount != 5 {
		t.Fatalf("期望 run_count 为 5，实际为 %d", store.state.RunCount)
	}
	if store.state.SchemaVersion != stateSchemaVersion {
		t.Fatalf("SchemaVersion 不匹配: %d", store.state.SchemaVersion)
	}
	if store.state.Running {
		t.Fatal("停止后 running 应为 false")
	}
	if !store.state.Healthy {
		t.Fatal("正常停止后 healthy 应保持为 true")
	}

	if !store.state.LastStartedAt.Equal(start) {
		t.Fatalf("LastStartedAt 不匹配: %v", store.state.LastStartedAt)
	}

	if !store.state.LastStoppedAt.Equal(stop) {
		t.Fatalf("LastStoppedAt 不匹配: %v", store.state.LastStoppedAt)
	}
}

func TestNewFailsWhenStateLoadFails(t *testing.T) {
	t.Parallel()

	_, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: &memoryStateStore{loadErr: errors.New("boom")},
	})
	if err == nil {
		t.Fatal("期望状态装载失败时返回错误")
	}
}

func TestNewLoadsSettingsFromStore(t *testing.T) {
	t.Parallel()

	application, err := New(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: &memorySettingsStore{
			settings: config.Settings{
				Language: "简体中文",
			},
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if got := application.Settings().Language; got != "简体中文" {
		t.Fatalf("Settings 未装载外部配置: %q", got)
	}
}
