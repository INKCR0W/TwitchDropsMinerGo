package httpclient

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	GQLRateLimitCapacity = 5
)

var GQLRateLimitWindow = time.Second

type WindowLimiter struct {
	capacity int
	window   time.Duration
	now      func() time.Time

	mu         sync.Mutex
	timestamps []time.Time
}

func NewWindowLimiter(capacity int, window time.Duration) (*WindowLimiter, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("限流容量必须大于 0")
	}
	if window <= 0 {
		return nil, fmt.Errorf("限流窗口必须大于 0")
	}

	return &WindowLimiter{
		capacity: capacity,
		window:   window,
		now:      time.Now,
	}, nil
}

func NewGQLLimiter() *WindowLimiter {
	limiter, err := NewWindowLimiter(GQLRateLimitCapacity, GQLRateLimitWindow)
	if err != nil {
		panic(err)
	}
	return limiter
}

func (l *WindowLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		now := l.now()

		l.mu.Lock()
		l.trimExpired(now)
		if len(l.timestamps) < l.capacity {
			l.timestamps = append(l.timestamps, now)
			l.mu.Unlock()
			return nil
		}

		waitFor := l.timestamps[0].Add(l.window).Sub(now)
		l.mu.Unlock()

		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *WindowLimiter) trimExpired(now time.Time) {
	cutoff := now.Add(-l.window)
	index := 0
	for index < len(l.timestamps) && !l.timestamps[index].After(cutoff) {
		index++
	}

	if index == 0 {
		return
	}

	l.timestamps = append([]time.Time(nil), l.timestamps[index:]...)
}
