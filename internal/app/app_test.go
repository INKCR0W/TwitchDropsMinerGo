package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunWaitsForCancellation(t *testing.T) {
	t.Parallel()

	application := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
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
}

func TestRunRejectsNilContext(t *testing.T) {
	t.Parallel()

	application := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := application.Run(nil); err == nil {
		t.Fatal("期望 nil context 返回错误")
	}
}
