package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testDeviceCode() auth.DeviceCode {
	return auth.DeviceCode{
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://www.twitch.tv/activate",
		Interval:        5 * time.Second,
		ExpiresAt:       time.Now().Add(30 * time.Minute),
	}
}

func TestDeviceCodeNotifierWritesBannerAndPendingFile(t *testing.T) {
	t.Parallel()

	pendingFile := filepath.Join(t.TempDir(), "pending_login.txt")
	var banner bytes.Buffer
	handler := deviceCodeNotifier(testLogger(), &banner, pendingFile)

	if err := handler(context.Background(), testDeviceCode()); err != nil {
		t.Fatalf("handler 返回错误: %v", err)
	}

	for name, text := range map[string]string{"banner": banner.String(), "file": readFile(t, pendingFile)} {
		if !strings.Contains(text, "ABCD-EFGH") || !strings.Contains(text, "https://www.twitch.tv/activate") {
			t.Fatalf("%s 缺少码或网址: %q", name, text)
		}
	}
}

func TestDeviceCodeNotifierToleratesWriteFailure(t *testing.T) {
	t.Parallel()

	pendingFile := filepath.Join(t.TempDir(), "missing", "pending_login.txt")
	handler := deviceCodeNotifier(testLogger(), io.Discard, pendingFile)

	if err := handler(context.Background(), testDeviceCode()); err != nil {
		t.Fatalf("写文件失败不应中断登录: %v", err)
	}
}

func TestClearPendingLoginRemovesFileAndToleratesMissing(t *testing.T) {
	t.Parallel()

	pendingFile := filepath.Join(t.TempDir(), "pending_login.txt")
	if err := os.WriteFile(pendingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}

	clearNotice := clearPendingLogin(testLogger(), pendingFile)
	clearNotice()
	if _, err := os.Stat(pendingFile); !os.IsNotExist(err) {
		t.Fatalf("文件应已删除: %v", err)
	}
	clearNotice()
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	return string(content)
}
