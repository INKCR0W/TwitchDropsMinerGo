package config

import (
	"path/filepath"
	"testing"

	"twitchdropsminergo/internal/storage"
)

func TestSmartPrioritySafetyMinutesDefaultAndValidation(t *testing.T) {
	t.Parallel()

	if got := DefaultSettings().SmartPrioritySafetyMinutes; got != DefaultSmartPrioritySafetyMinutes {
		t.Fatalf("默认 smart_priority_safety_minutes 应为 %d, 实际 %d", DefaultSmartPrioritySafetyMinutes, got)
	}

	zero := DefaultSettings()
	zero.SmartPrioritySafetyMinutes = 0
	if err := zero.Validate(); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}
	if zero.SmartPrioritySafetyMinutes != DefaultSmartPrioritySafetyMinutes {
		t.Fatalf("0 应回退为默认 %d, 实际 %d", DefaultSmartPrioritySafetyMinutes, zero.SmartPrioritySafetyMinutes)
	}

	negative := DefaultSettings()
	negative.SmartPrioritySafetyMinutes = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("smart_priority_safety_minutes 为负值应返回错误")
	}

	custom := DefaultSettings()
	custom.SmartPrioritySafetyMinutes = 45
	if err := custom.Validate(); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}
	if custom.SmartPrioritySafetyMinutes != 45 {
		t.Fatalf("自定义值应保留为 45, 实际 %d", custom.SmartPrioritySafetyMinutes)
	}
}

func TestLoadAppliesFileOverridesAndDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	input := map[string]any{
		"language":           "简体中文",
		"connection_quality": 3,
		"priority":           []string{"A", "A", "B", ""},
		"exclude":            []string{"X", "X", "  "},
		"log": map[string]any{
			"level":        "debug",
			"format":       "json",
			"file_enabled": false,
		},
	}

	if err := storage.SaveJSONFile(path, input); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	if settings.Language != "简体中文" {
		t.Fatalf("Language 不匹配: %q", settings.Language)
	}

	if settings.ConnectionQuality != 3 {
		t.Fatalf("ConnectionQuality 不匹配: %d", settings.ConnectionQuality)
	}

	if len(settings.Priority) != 2 || settings.Priority[0] != "A" || settings.Priority[1] != "B" {
		t.Fatalf("Priority 未按预期去重: %#v", settings.Priority)
	}

	if len(settings.Exclude) != 1 || settings.Exclude[0] != "X" {
		t.Fatalf("Exclude 未按预期去重: %#v", settings.Exclude)
	}

	if settings.PriorityMode != PriorityOnly {
		t.Fatalf("PriorityMode 默认值错误: %q", settings.PriorityMode)
	}

	if !settings.TrayNotifications {
		t.Fatal("TrayNotifications 默认值应为 true")
	}

	if settings.Log.Level != "debug" || settings.Log.Format != "json" || settings.Log.FileEnabled {
		t.Fatalf("Log 配置不匹配: %#v", settings.Log)
	}
}

func TestLoadRejectsInvalidConnectionQuality(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := storage.SaveJSONFile(path, map[string]any{
		"connection_quality": 0,
	}); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("期望非法 connection_quality 返回错误")
	}
}

func TestLoadAcceptsSmartBalancePriorityMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := storage.SaveJSONFile(path, map[string]any{
		"priority_mode": "smart_balance",
	}); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if settings.PriorityMode != SmartBalance {
		t.Fatalf("PriorityMode 应为 smart_balance: %q", settings.PriorityMode)
	}
}

func TestDefaultSettingsConfiguresLogRotation(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings()
	if settings.Log.MaxSizeBytes <= 0 {
		t.Fatalf("默认日志最大大小应启用: %d", settings.Log.MaxSizeBytes)
	}
	if settings.Log.MaxBackups <= 0 {
		t.Fatalf("默认日志备份数量应启用: %d", settings.Log.MaxBackups)
	}
}

func TestSettingsCloneCopiesSlices(t *testing.T) {
	t.Parallel()

	original := Settings{
		Exclude:  []string{"A"},
		Priority: []string{"B"},
	}

	cloned := original.Clone()
	cloned.Exclude[0] = "X"
	cloned.Priority[0] = "Y"

	if original.Exclude[0] != "A" {
		t.Fatalf("Exclude 应保持独立副本: %#v", original.Exclude)
	}
	if original.Priority[0] != "B" {
		t.Fatalf("Priority 应保持独立副本: %#v", original.Priority)
	}
}

func TestSettingsSanitizedRemovesProxyCredentials(t *testing.T) {
	t.Parallel()

	settings := Settings{
		Proxy: "http://user:pass@proxy.example.com:8080",
	}

	sanitized := settings.Sanitized()
	if sanitized.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("Proxy 脱敏结果错误: %q", sanitized.Proxy)
	}
}

func TestFileStoreLoadsAndSavesSettings(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewFileStore(path)

	settings := DefaultSettings()
	settings.Language = "简体中文"

	if err := store.Save(settings); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if loaded.Language != "简体中文" {
		t.Fatalf("Load 未返回保存后的配置: %#v", loaded)
	}
}
