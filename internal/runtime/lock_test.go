package runtime

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireInstanceLockRejectsCompetingProcess(t *testing.T) {
	t.Parallel()

	if os.Getenv("GO_WANT_LOCK_HELPER") == "1" {
		lockHelperProcess()
		return
	}

	tempDir := t.TempDir()
	lockPath := filepath.Join(tempDir, "lock.file")
	readyPath := filepath.Join(tempDir, "ready")

	command := exec.Command(os.Args[0], "-test.run=TestAcquireInstanceLockRejectsCompetingProcess")
	command.Env = append(
		os.Environ(),
		"GO_WANT_LOCK_HELPER=1",
		"LOCK_PATH="+lockPath,
		"LOCK_READY_PATH="+readyPath,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("创建 helper stdin 失败: %v", err)
	}

	if err := command.Start(); err != nil {
		t.Fatalf("启动 helper 失败: %v", err)
	}

	waitForReadyFile(t, readyPath)

	secondLock, err := AcquireInstanceLock(lockPath)
	if !errors.Is(err, ErrAlreadyRunning) {
		if secondLock != nil {
			_ = secondLock.Close()
		}
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("期望返回 ErrAlreadyRunning，实际错误: %v", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("关闭 helper stdin 失败: %v", err)
	}

	if err := command.Wait(); err != nil {
		t.Fatalf("等待 helper 退出失败: %v", err)
	}

	lock, err := AcquireInstanceLock(lockPath)
	if err != nil {
		t.Fatalf("helper 退出后重新获取锁失败: %v", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			t.Fatalf("关闭锁失败: %v", closeErr)
		}
	}()
}

func lockHelperProcess() {
	lockPath := os.Getenv("LOCK_PATH")
	readyPath := os.Getenv("LOCK_READY_PATH")

	lock, err := AcquireInstanceLock(lockPath)
	if err != nil {
		os.Exit(2)
	}
	defer func() {
		_ = lock.Close()
	}()

	if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
		os.Exit(3)
	}

	_, _ = io.ReadAll(os.Stdin)
	os.Exit(0)
}

func waitForReadyFile(t *testing.T, readyPath string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("等待 helper 就绪超时: %s", readyPath)
}
