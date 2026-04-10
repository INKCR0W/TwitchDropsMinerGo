package storage

import (
	"os"
	"path/filepath"
	"testing"
)

type sampleState struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestJSONFileLoadAndSave(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	file := NewJSONFile(path, sampleState{Name: "default", Count: 1})

	state, err := file.Load()
	if err != nil {
		t.Fatalf("首次 Load 返回错误: %v", err)
	}

	if state.Name != "default" || state.Count != 1 {
		t.Fatalf("默认值不匹配: %#v", state)
	}

	expected := sampleState{Name: "saved", Count: 2}
	if err := file.Save(expected); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	actual, err := file.Load()
	if err != nil {
		t.Fatalf("再次 Load 返回错误: %v", err)
	}

	if actual != expected {
		t.Fatalf("Load 结果不匹配: %#v", actual)
	}
}

func TestLoadJSONFileFallsBackToBackup(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	backupPath := path + ".bak"

	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("写入损坏主文件失败: %v", err)
	}

	if err := os.WriteFile(backupPath, []byte("{\"name\":\"backup\",\"count\":9}\n"), 0o644); err != nil {
		t.Fatalf("写入备份文件失败: %v", err)
	}

	state, err := LoadJSONFile(path, sampleState{Name: "default", Count: 1})
	if err != nil {
		t.Fatalf("LoadJSONFile 返回错误: %v", err)
	}

	if state.Name != "backup" || state.Count != 9 {
		t.Fatalf("未按预期回退到备份文件: %#v", state)
	}
}
