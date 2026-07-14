package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestHandleRuntimeErrorTracksErrorSince(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	current := base
	s := &Scheduler{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		errorRetryDelay: time.Millisecond,
		now:             func() time.Time { return current },
		sleep:           func(context.Context, time.Duration) error { return nil },
		stateChanged:    make(chan struct{}, 1),
	}

	if err := s.handleRuntimeError(context.Background(), StateChannelsFetch, errors.New("第一次失败")); err != nil {
		t.Fatalf("handleRuntimeError 返回错误: %v", err)
	}
	if s.runtimeErrorSince != base {
		t.Fatalf("首次失败应记录起始时间: %v", s.runtimeErrorSince)
	}

	current = base.Add(3 * time.Minute)
	if err := s.handleRuntimeError(context.Background(), StateChannelsFetch, errors.New("第二次失败")); err != nil {
		t.Fatalf("handleRuntimeError 返回错误: %v", err)
	}
	if s.runtimeErrorSince != base {
		t.Fatalf("连续失败期间不应重置起始时间: %v", s.runtimeErrorSince)
	}
	if s.lastRuntimeError == nil || s.lastRuntimeError.Error() != "第二次失败" {
		t.Fatalf("错误内容应更新为最新: %v", s.lastRuntimeError)
	}

	s.clearRuntimeError()
	if s.lastRuntimeError != nil || !s.runtimeErrorSince.IsZero() {
		t.Fatalf("成功后应清空错误与起始时间: %v %v", s.lastRuntimeError, s.runtimeErrorSince)
	}
}
