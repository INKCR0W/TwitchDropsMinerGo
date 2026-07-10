package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunServiceStopsOnCancellation(t *testing.T) {
	t.Parallel()

	stopped := make(chan struct{})
	service := stubRunner{
		run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if err := runServiceWithTimeout(ctx, cancel, 200*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	); err != nil {
		t.Fatalf("runServiceWithTimeout 返回错误: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("服务在取消后未及时退出")
	}
}

func TestRunServiceReturnsFirstWorkerError(t *testing.T) {
	t.Parallel()

	service := stubRunner{
		run: func(context.Context) error {
			return errors.New("worker failed")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runServiceWithTimeout(ctx, cancel, 200*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	)
	if err == nil || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("期望返回首个 worker 错误，实际为 %v", err)
	}
}

func TestRunServiceTimesOutWhenWorkerIgnoresCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	service := stubRunner{
		run: func(ctx context.Context) error {
			<-ctx.Done()
			<-release
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := runServiceWithTimeout(ctx, cancel, 50*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	)
	close(release)
	if !errors.Is(err, errRuntimeShutdownTimeout) {
		t.Fatalf("期望返回退出超时错误，实际为 %v", err)
	}
}

type stubRunner struct {
	run func(context.Context) error
}

func (s stubRunner) Run(ctx context.Context) error {
	if s.run == nil {
		<-ctx.Done()
		return nil
	}
	return s.run(ctx)
}
