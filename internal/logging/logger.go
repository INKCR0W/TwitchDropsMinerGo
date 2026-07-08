package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"twitchdropsminergo/internal/config"
)

func New(settings config.LoggingSettings, logFile string) (*slog.Logger, func() error, error) {
	level, err := parseLevel(settings.Level)
	if err != nil {
		return nil, nil, err
	}

	writer := io.Writer(os.Stdout)
	closeFn := func() error { return nil }
	if settings.FileEnabled {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
			return nil, nil, fmt.Errorf("创建日志目录失败: %w", err)
		}

		if err := rotateIfNeeded(logFile, settings.MaxSizeBytes, settings.MaxBackups); err != nil {
			return nil, nil, err
		}

		file, err := newRotatingFile(logFile, settings.MaxSizeBytes, settings.MaxBackups)
		if err != nil {
			return nil, nil, fmt.Errorf("打开日志文件失败: %w", err)
		}

		writer = io.MultiWriter(os.Stdout, file)
		closeFn = file.Close
	}

	options := &slog.HandlerOptions{
		AddSource: settings.AddSource,
		Level:     level,
	}

	var handler slog.Handler
	switch settings.Format {
	case "json":
		handler = slog.NewJSONHandler(writer, options)
	default:
		handler = slog.NewTextHandler(writer, options)
	}

	return slog.New(handler), closeFn, nil
}

type rotatingFile struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
}

func newRotatingFile(path string, maxSize int64, maxBackups int) (*rotatingFile, error) {
	rf := &rotatingFile{path: path, maxSize: maxSize, maxBackups: maxBackups}
	if err := rf.open(); err != nil {
		return nil, err
	}
	return rf, nil
}

func (rf *rotatingFile) open() error {
	file, err := openPrivateAppendFile(rf.path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	rf.file = file
	rf.size = info.Size()
	return nil
}

func (rf *rotatingFile) rotationEnabled() bool {
	return rf.maxSize > 0 && rf.maxBackups > 0
}

func (rf *rotatingFile) Write(p []byte) (int, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 关闭后仍可能有被放弃的 goroutine（如超时关闭后残留的组件）尝试写日志：
	// 返回 os.ErrClosed 而不是解引用 nil 造成 panic（与关闭后的裸 *os.File 行为一致）。
	if rf.file == nil {
		return 0, os.ErrClosed
	}

	if rf.rotationEnabled() && rf.size > 0 && rf.size+int64(len(p)) > rf.maxSize {
		if err := rf.rotateLocked(); err != nil {
			// 轮转失败时继续写入当前文件，优先保证不丢日志。
			fmt.Fprintf(os.Stderr, "警告: 轮转日志文件失败，继续写入当前文件: %v\n", err)
		}
	}

	n, err := rf.file.Write(p)
	rf.size += int64(n)
	return n, err
}

func (rf *rotatingFile) rotateLocked() error {
	if err := rf.file.Close(); err != nil {
		return err
	}
	if err := rotateBackups(rf.path, rf.maxBackups); err != nil {
		_ = rf.open()
		return err
	}
	return rf.open()
}

func (rf *rotatingFile) Close() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.file == nil {
		return nil
	}
	err := rf.file.Close()
	rf.file = nil
	return err
}

func rotateIfNeeded(path string, maxSize int64, maxBackups int) error {
	if maxSize <= 0 || maxBackups <= 0 {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("检查日志文件失败: %w", err)
	}
	if info.Size() < maxSize {
		return nil
	}

	return rotateBackups(path, maxBackups)
}

func rotateBackups(path string, maxBackups int) error {
	if maxBackups <= 0 {
		return nil
	}

	for i := maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		dst := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Remove(dst)
			_ = os.Rename(src, dst)
		}
	}

	backupPath := path + ".1"
	_ = os.Remove(backupPath)
	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("轮转日志文件失败: %w", err)
	}

	return nil
}

func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("日志级别 %q 不受支持", level)
	}
}
