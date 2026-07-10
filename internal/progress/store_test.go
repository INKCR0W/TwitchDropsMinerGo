package progress

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) (*FileStore, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "progress.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore 返回错误: %v", err)
	}
	return store, path
}

func TestRecordKeepsHighestMinutesAndSurvivesReload(t *testing.T) {
	t.Parallel()

	store, path := testStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)

	for _, minutes := range []int{300, 1006, 12} {
		if err := store.Record("campaign", "diamond", minutes, expires, now); err != nil {
			t.Fatalf("Record(%d) 返回错误: %v", minutes, err)
		}
	}

	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("重新加载返回错误: %v", err)
	}
	entries := reloaded.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("应只有一条记录: %#v", entries)
	}
	if entries[0].MinutesWatched != 1006 {
		t.Fatalf("回退的分钟数不应覆盖已记录的峰值: %d", entries[0].MinutesWatched)
	}
}

func TestRecordRejectsMissingExpiry(t *testing.T) {
	t.Parallel()

	store, _ := testStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	if err := store.Record("campaign", "drop", 30, time.Time{}, now); err == nil {
		t.Fatal("缺少到期时间时应拒绝写入, 否则记录永远清不掉")
	}
	if entries := store.Snapshot(); len(entries) != 0 {
		t.Fatalf("拒绝的记录不应落盘: %#v", entries)
	}
}

func TestPruneExpiredRemovesEndedAndUndatedEntries(t *testing.T) {
	t.Parallel()

	store, path := testStore(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	if err := store.Record("alive", "drop", 30, now.Add(time.Hour), now); err != nil {
		t.Fatalf("写入未过期记录失败: %v", err)
	}
	if err := store.Record("ended", "drop", 30, now.Add(-time.Minute), now); err != nil {
		t.Fatalf("写入已过期记录失败: %v", err)
	}
	store.data.Entries[entryKey("undated", "drop")] = Entry{CampaignID: "undated", DropID: "drop", MinutesWatched: 30, UpdatedAt: now}

	removed, err := store.PruneExpired(now)
	if err != nil {
		t.Fatalf("PruneExpired 返回错误: %v", err)
	}
	if removed != 2 {
		t.Fatalf("应清理已过期与无到期时间的记录: %d", removed)
	}

	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("重新加载返回错误: %v", err)
	}
	entries := reloaded.Snapshot()
	if len(entries) != 1 || entries[0].CampaignID != "alive" {
		t.Fatalf("未过期记录应保留: %#v", entries)
	}
}

func TestRecordRollsBackInMemoryStateWhenSaveFails(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, err := NewFileStore(filepath.Join(directory, "progress.json"))
	if err != nil {
		t.Fatalf("NewFileStore 返回错误: %v", err)
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if err := store.Record("campaign", "drop", 30, now.Add(time.Hour), now); err != nil {
		t.Fatalf("首次 Record 失败: %v", err)
	}

	store.path = filepath.Join(directory, "missing", "\x00", "progress.json")
	if err := store.Record("campaign", "drop", 90, now.Add(time.Hour), now); err == nil {
		t.Fatal("写盘失败时 Record 应返回错误")
	}

	entries := store.Snapshot()
	if len(entries) != 1 || entries[0].MinutesWatched != 30 {
		t.Fatalf("写盘失败后内存状态应回滚: %#v", entries)
	}
}

func TestNewFileStoreRecoversFromCorruptFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "progress.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("损坏文件不应阻止启动: %v", err)
	}
	if entries := store.Snapshot(); len(entries) != 0 {
		t.Fatalf("损坏文件应重新开始积累: %#v", entries)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("损坏文件应被保留一份副本: %v", err)
	}
}
