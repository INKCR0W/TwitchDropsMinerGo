package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"twitchdropsminergo/internal/progress"
	"twitchdropsminergo/internal/rewards"
)

func TestAccountProgressRejectsUnverifiedIdentity(t *testing.T) {
	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []int64{0, -1} {
		if _, err := layout.ProgressFilesForUser(userID); err == nil {
			t.Fatalf("无效用户 ID %d 不应得到进度路径", userID)
		}
	}
}

func TestAccountProgressSurvivesSwitchAndRestart(t *testing.T) {
	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a, err := layout.ProgressFilesForUser(101)
	if err != nil {
		t.Fatal(err)
	}
	watch, reward := openProgressFiles(t, a)
	if err := watch.Record("ordinary", "drop", 20, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := reward.RecordProgress("partial", "partial-drop", 4, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := reward.RecordCompletion("complete", "complete-drop", 5, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	b, err := layout.ProgressFilesForUser(202)
	if err != nil {
		t.Fatal(err)
	}
	bWatch, bReward := openProgressFiles(t, b)
	if len(bWatch.Snapshot()) != 0 || len(bReward.Snapshot()) != 0 {
		t.Fatalf("账号 B 继承了 A 的记录: watch=%v rewards=%v", bWatch.Snapshot(), bReward.Snapshot())
	}
	if err := bWatch.Record("ordinary", "drop", 1, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}

	reopened, err := ResolveLayout(layout.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	aAgain, err := reopened.ProgressFilesForUser(101)
	if err != nil {
		t.Fatal(err)
	}
	aWatch, aReward := openProgressFiles(t, aAgain)
	if entries := aWatch.Snapshot(); len(entries) != 1 || entries[0].MinutesWatched != 20 {
		t.Fatalf("账号 A 重启后进度不匹配: %v", entries)
	}
	entries := aReward.Snapshot()
	if len(entries) != 2 || entries["partial"].MinutesWatched != 4 || entries["complete"].CompletedAt.IsZero() {
		t.Fatalf("账号 A 的部分/完成奖励记录不匹配: %v", entries)
	}
}

func TestAccountProgressLeavesUnownedLegacyFilesUntouched(t *testing.T) {
	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := ProgressFiles{RewardsFile: layout.RewardsFile, ProgressFile: layout.ProgressFile}
	watch, reward := openProgressFiles(t, legacy)
	now := time.Now().UTC()
	if err := watch.Record("old", "old-drop", 20, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := reward.RecordCompletion("old", "old-drop", 20, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, path := range []string{legacy.RewardsFile, legacy.ProgressFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(data)
	}
	for _, userID := range []int64{101, 202} {
		files, err := layout.ProgressFilesForUser(userID)
		if err != nil {
			t.Fatal(err)
		}
		accountWatch, accountReward := openProgressFiles(t, files)
		if len(accountWatch.Snapshot()) != 0 || len(accountReward.Snapshot()) != 0 {
			t.Fatalf("旧无身份记录不应归属账号 %d", userID)
		}
	}
	for path, expected := range before {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != expected {
			t.Fatalf("旧文件已被修改: %s", path)
		}
	}
}

func TestAccountProgressSaveFailureDoesNotFallBackToSharedFiles(t *testing.T) {
	layout, err := ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.StateDir, "accounts"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := layout.ProgressFilesForUser(101)
	if err != nil {
		t.Fatal(err)
	}
	watch, watchErr := progress.NewFileStore(files.ProgressFile)
	reward, rewardErr := rewards.NewFileStore(files.RewardsFile)
	now := time.Now().UTC()
	// 部分文件系统在读取阶段返回错误，其他系统到保存时才报错
	if watchErr == nil {
		if err := watch.Record("campaign", "drop", 1, now.Add(time.Hour), now); err == nil {
			t.Fatal("账号目录不可写时观看进度保存应失败")
		}
		if len(watch.Snapshot()) != 0 {
			t.Fatal("失败保存不应提交内存观看记录")
		}
	}
	if rewardErr == nil {
		if _, err := reward.RecordCompletion("campaign", "drop", 1, now, now.Add(time.Hour)); err == nil {
			t.Fatal("账号目录不可写时奖励进度保存应失败")
		}
		if len(reward.Snapshot()) != 0 {
			t.Fatal("失败保存不应提交内存奖励记录")
		}
	}
	for _, path := range []string{layout.ProgressFile, layout.RewardsFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("保存失败不应回退共享路径: path=%s err=%v", path, err)
		}
	}
}

func openProgressFiles(t *testing.T, files ProgressFiles) (*progress.FileStore, *rewards.FileStore) {
	t.Helper()
	watch, err := progress.NewFileStore(files.ProgressFile)
	if err != nil {
		t.Fatal(err)
	}
	reward, err := rewards.NewFileStore(files.RewardsFile)
	if err != nil {
		t.Fatal(err)
	}
	return watch, reward
}
