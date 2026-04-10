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

		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
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
