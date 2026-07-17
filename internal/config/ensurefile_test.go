package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFileAddsMissingKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"proxy":"http://p:1"}`+"\n"), 0o600); err != nil {
		t.Fatalf("写入初始文件失败: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	rewrote, err := EnsureFile(path, loaded)
	if err != nil {
		t.Fatalf("EnsureFile 返回错误: %v", err)
	}
	if !rewrote {
		t.Fatal("缺键文件应触发重写")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取重写后文件失败: %v", err)
	}
	if !bytes.Contains(data, []byte(`"watch_stall_minutes"`)) {
		t.Fatalf("重写后文件缺少新键:\n%s", data)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatalf("重载返回错误: %v", err)
	}
	if again.Proxy != "http://p:1" {
		t.Fatalf("重写丢失了原有值: %q", again.Proxy)
	}
	if again.WatchStallMinutes != DefaultWatchStallMinutes {
		t.Fatalf("新键未取默认值: %d", again.WatchStallMinutes)
	}
}

func TestEnsureFileNoRewriteWhenCanonical(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, DefaultSettings()); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	rewrote, err := EnsureFile(path, DefaultSettings())
	if err != nil {
		t.Fatalf("EnsureFile 返回错误: %v", err)
	}
	if rewrote {
		t.Fatal("已规范文件不应重写")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("不应产生 .bak 备份, stat err=%v", err)
	}
}

func TestEnsureFileNormalizesBeforeCompare(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	settings := DefaultSettings()
	settings.Priority = []string{"a", "a", " b "}

	first, err := EnsureFile(path, settings)
	if err != nil {
		t.Fatalf("首次 EnsureFile 返回错误: %v", err)
	}
	if !first {
		t.Fatal("文件不存在时应写入")
	}

	second, err := EnsureFile(path, settings)
	if err != nil {
		t.Fatalf("二次 EnsureFile 返回错误: %v", err)
	}
	if second {
		t.Fatal("未归一化输入重复调用不应再次重写")
	}
}

func TestEnsureFileDropsObsoleteKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"obsolete_key":true}`+"\n"), 0o600); err != nil {
		t.Fatalf("写入初始文件失败: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if _, err := EnsureFile(path, loaded); err != nil {
		t.Fatalf("EnsureFile 返回错误: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if bytes.Contains(data, []byte("obsolete_key")) {
		t.Fatalf("废弃键应被移除:\n%s", data)
	}
}
