package storage

import (
	"errors"
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

func TestQuarantineCorruptMovesPrimaryAndBackup(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("写入主文件失败: %v", err)
	}
	if err := os.WriteFile(path+".bak", []byte("{also broken"), 0o600); err != nil {
		t.Fatalf("写入备份文件失败: %v", err)
	}

	moved, err := QuarantineCorrupt(path)
	if err != nil {
		t.Fatalf("QuarantineCorrupt 返回错误: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("应隔离主文件与备份文件, got=%#v", moved)
	}
	for _, source := range []string{path, path + ".bak"} {
		if _, statErr := os.Stat(source); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s 应已被移走, err=%v", source, statErr)
		}
	}
	data, err := os.ReadFile(path + ".corrupt")
	if err != nil {
		t.Fatalf("读取隔离文件失败: %v", err)
	}
	if string(data) != "{broken" {
		t.Errorf("隔离文件应保留原始内容, got=%q", data)
	}
}

func TestQuarantineCorruptSkipsMissingFilesAndOverwritesStaleQuarantine(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path, []byte("new garbage"), 0o600); err != nil {
		t.Fatalf("写入主文件失败: %v", err)
	}
	if err := os.WriteFile(path+".corrupt", []byte("old garbage"), 0o600); err != nil {
		t.Fatalf("写入旧隔离文件失败: %v", err)
	}

	moved, err := QuarantineCorrupt(path)
	if err != nil {
		t.Fatalf("QuarantineCorrupt 返回错误: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("备份文件不存在时只应隔离主文件, got=%#v", moved)
	}
	data, err := os.ReadFile(path + ".corrupt")
	if err != nil {
		t.Fatalf("读取隔离文件失败: %v", err)
	}
	if string(data) != "new garbage" {
		t.Errorf("应覆盖旧的隔离文件, got=%q", data)
	}
}
