package rewards

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRecordProgressPersistsCompletion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "reward-progress.json")
	now := time.Date(2026, 5, 31, 13, 0, 0, 0, time.UTC)
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore 返回错误: %v", err)
	}

	progress, err := store.RecordProgress("reward:campaign", "reward:drop", 5, true, now)
	if err != nil {
		t.Fatalf("RecordProgress 返回错误: %v", err)
	}
	if progress.MinutesWatched != 5 || progress.CompletedAt.IsZero() {
		t.Fatalf("完成进度记录不匹配: %#v", progress)
	}

	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("重新加载 FileStore 返回错误: %v", err)
	}
	snapshot := reloaded.Snapshot()
	if got := snapshot["reward:campaign"]; got.MinutesWatched != 5 || !got.CompletedAt.Equal(now) {
		t.Fatalf("持久化进度不匹配: %#v", got)
	}

	completed := CompletedCampaignIDs(snapshot)
	if _, ok := completed["reward:campaign"]; !ok {
		t.Fatalf("CompletedCampaignIDs 应包含已完成 reward campaign: %#v", completed)
	}
}

func TestFileStoreRecordProgressDoesNotRegressMinutesOrCompletionTime(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "reward-progress.json")
	completedAt := time.Date(2026, 5, 31, 13, 0, 0, 0, time.UTC)
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore 返回错误: %v", err)
	}

	if _, err := store.RecordProgress("reward:campaign", "reward:drop", 5, true, completedAt); err != nil {
		t.Fatalf("写入完成进度失败: %v", err)
	}
	later := completedAt.Add(time.Hour)
	progress, err := store.RecordProgress("reward:campaign", "reward:drop", 3, true, later)
	if err != nil {
		t.Fatalf("再次写入进度失败: %v", err)
	}
	if progress.MinutesWatched != 5 {
		t.Fatalf("分钟数不应回退: %#v", progress)
	}
	if !progress.CompletedAt.Equal(completedAt) {
		t.Fatalf("完成时间不应被覆盖: %#v", progress)
	}
}

func TestFileStoreRecordProgressRollsBackWhenSaveFails(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "occupied", "reward-progress.json")
	now := time.Date(2026, 5, 31, 13, 0, 0, 0, time.UTC)
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore 返回错误: %v", err)
	}
	if _, err := store.RecordProgress("reward:campaign", "reward:drop", 1, false, now); err != nil {
		t.Fatalf("首次写入进度失败: %v", err)
	}

	occupiedDir := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除进度文件失败: %v", err)
	}
	if err := os.Remove(occupiedDir); err != nil {
		t.Fatalf("删除进度目录失败: %v", err)
	}
	if err := os.WriteFile(occupiedDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("写入占位文件失败: %v", err)
	}
	if _, err := store.RecordProgress("reward:campaign", "reward:drop", 5, true, now.Add(time.Minute)); err == nil {
		t.Fatal("进度目录被普通文件占用时写入应失败")
	}

	snapshot := store.Snapshot()
	if got := snapshot["reward:campaign"]; got.MinutesWatched != 1 || !got.CompletedAt.IsZero() {
		t.Fatalf("保存失败后内存状态应回滚: %#v", got)
	}
}

func TestNewFileStoreRejectsEmptyPath(t *testing.T) {
	t.Parallel()

	if _, err := NewFileStore(" "); err == nil {
		t.Fatal("空路径应返回错误，避免写入当前目录")
	}
}

func TestNewFileStoreFallsBackToEmptyProgressWhenFileIsCorrupt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "reward-progress.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("写入损坏进度文件失败: %v", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("损坏进度文件应降级为空进度而不是启动失败: %v", err)
	}
	if snapshot := store.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("损坏文件降级后应为空进度: %#v", snapshot)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf("损坏文件应备份为 .bad: %v", err)
	}
}

func TestNewFileStoreFallsBackToEmptyProgressWhenFileIsEmpty(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "reward-progress.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("写入空进度文件失败: %v", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("空进度文件应降级为空进度而不是启动失败: %v", err)
	}
	if snapshot := store.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("空文件降级后应为空进度: %#v", snapshot)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf("空文件应备份为 .bad: %v", err)
	}
}
