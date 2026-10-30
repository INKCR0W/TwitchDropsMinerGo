package runtime

import (
	"path/filepath"
	"testing"
)

func TestResolveLayoutPendingLoginFile(t *testing.T) {
	t.Parallel()

	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveLayout 返回错误: %v", err)
	}

	want := filepath.Join(layout.RootDir, "pending_login.txt")
	if layout.PendingLoginFile != want {
		t.Fatalf("PendingLoginFile 不匹配: got %q want %q", layout.PendingLoginFile, want)
	}
}

func TestResolveLayoutSessionFile(t *testing.T) {
	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if layout.SessionFile != filepath.Join(layout.StateDir, SessionFileName) {
		t.Fatalf("会话路径未使用共享文件名: %q", layout.SessionFile)
	}
}
