package scheduler

import (
	"log/slog"
	"strings"
	"sync"
)

type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&logSyncWriter{b: b}, nil))
}

func (b *logBuffer) contains(substr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buf.String(), substr)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type logSyncWriter struct {
	b *logBuffer
}

func (w *logSyncWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.b.buf.Write(p)
}
