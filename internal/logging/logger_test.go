package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"twitchdropsminergo/internal/config"
)

func TestNewCreatesLogFileWithPrivatePermissions(t *testing.T) {
	t.Parallel()

	logFile := filepath.Join(t.TempDir(), "logs", "miner-server.log")
	logger, closeFn, err := New(config.LoggingSettings{
		Level:       "info",
		Format:      "text",
		FileEnabled: true,
	}, logFile)
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	logger.Info("hello")
	if err := closeFn(); err != nil {
		t.Fatalf("close 返回错误: %v", err)
	}

	got, err := privateFilePermissionForTest(logFile)
	if err != nil {
		t.Fatalf("读取日志文件权限失败: %v", err)
	}
	if got != 0o600 {
		t.Fatalf("日志文件权限不匹配: got=%#o want=0600", got)
	}
}

func TestNewRotatesOversizedLogFile(t *testing.T) {
	t.Parallel()

	logFile := filepath.Join(t.TempDir(), "logs", "miner-server.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("写入旧日志失败: %v", err)
	}

	logger, closeFn, err := New(config.LoggingSettings{
		Level:        "info",
		Format:       "text",
		FileEnabled:  true,
		MaxSizeBytes: 5,
		MaxBackups:   2,
	}, logFile)
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "new")
	if err := closeFn(); err != nil {
		t.Fatalf("close 返回错误: %v", err)
	}

	if _, err := os.Stat(logFile + ".1"); err != nil {
		t.Fatalf("应生成 .1 备份: %v", err)
	}
}
