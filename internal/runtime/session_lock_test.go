package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionLockWaitsForSameFileAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	first, err := AcquireSessionLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := AcquireSessionLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("同一会话文件未等待已有导入/导出: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireSessionLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}
