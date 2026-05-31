package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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

		file, err := openPrivateAppendFile(logFile)
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
