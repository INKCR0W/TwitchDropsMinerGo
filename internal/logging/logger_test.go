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

func TestNewRotatesDuringRun(t *testing.T) {
	t.Parallel()

	logFile := filepath.Join(t.TempDir(), "logs", "miner-server.log")
	const maxSize = int64(256)

	logger, closeFn, err := New(config.LoggingSettings{
		Level:        "info",
		Format:       "text",
		FileEnabled:  true,
		MaxSizeBytes: maxSize,
		MaxBackups:   3,
	}, logFile)
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	// 运行期持续写入，远超 MaxSizeBytes；活动日志文件必须保持有界，而不是无限增长
	for i := 0; i < 200; i++ {
		logger.LogAttrs(context.Background(), slog.LevelInfo, "运行期日志行", slog.Int("i", i))
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close 返回错误: %v", err)
	}

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("读取活动日志文件失败: %v", err)
	}
	// 活动文件大小应受 MaxSizeBytes 约束（允许一行的溢出：轮转在写入前检查）
	if info.Size() > maxSize*2 {
		t.Fatalf("活动日志文件未按大小轮转，size=%d 超过上限约束", info.Size())
	}
	if _, err := os.Stat(logFile + ".1"); err != nil {
		t.Fatalf("运行期应生成轮转备份 .1: %v", err)
	}
}

func TestRotatingFileWriteAfterCloseDoesNotPanic(t *testing.T) {
	t.Parallel()

	logFile := filepath.Join(t.TempDir(), "logs", "miner-server.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	rf, err := newRotatingFile(logFile, 0, 0)
	if err != nil {
		t.Fatalf("newRotatingFile 返回错误: %v", err)
	}
	if err := rf.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}

	// 关闭后仍被残留 goroutine 写入时应返回错误，而不是解引用 nil 造成 panic
	if _, err := rf.Write([]byte("late write")); err == nil {
		t.Fatal("关闭后写入应返回错误")
	}
}
