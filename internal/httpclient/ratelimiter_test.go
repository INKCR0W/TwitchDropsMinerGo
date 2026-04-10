package httpclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWindowLimiterBlocksAfterCapacity(t *testing.T) {
	t.Parallel()

	limiter, err := NewWindowLimiter(2, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWindowLimiter 返回错误: %v", err)
	}

	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("第 1 次 Wait 返回错误: %v", err)
	}
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("第 2 次 Wait 返回错误: %v", err)
	}

	start := time.Now()
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("第 3 次 Wait 返回错误: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 90*time.Millisecond {
		t.Fatalf("第 3 次 Wait 未被限流，耗时仅 %v", elapsed)
	}
}

func TestWindowLimiterHonorsContextCancel(t *testing.T) {
	t.Parallel()

	limiter, err := NewWindowLimiter(1, time.Second)
	if err != nil {
		t.Fatalf("NewWindowLimiter 返回错误: %v", err)
	}

	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("预热 Wait 返回错误: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = limiter.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望返回 context.DeadlineExceeded，实际为 %v", err)
	}
}
