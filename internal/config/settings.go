package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"twitchdropsminergo/internal/secure"
	"twitchdropsminergo/internal/storage"
)

type PriorityMode string

const (
	PriorityOnly         PriorityMode = "priority_only"
	EndingSoonest        PriorityMode = "ending_soonest"
	LowAvailabilityFirst PriorityMode = "low_availability_first"
	SmartBalance         PriorityMode = "smart_balance"
)

// DefaultSmartPrioritySafetyMinutes 是 smart_balance 下允许非 priority 游戏插队前,
// 每个 priority 游戏必须保留的最小富余时间(分钟)。只有当所有 priority 游戏的富余
// 时间都不低于该值时,才允许更紧急的非 priority 游戏插队;从而保证 priority 游戏
// 可以晚挖但不会因插队而来不及完成。
const DefaultSmartPrioritySafetyMinutes = 120

const DefaultWatchStallMinutes = 10

type LoggingSettings struct {
	Level        string `json:"level"`
	Format       string `json:"format"`
	FileEnabled  bool   `json:"file_enabled"`
	AddSource    bool   `json:"add_source"`
	MaxSizeBytes int64  `json:"max_size_bytes"`
	MaxBackups   int    `json:"max_backups"`
}

type MainlandSettings struct {
	Enabled bool `json:"enabled"`
}

type Store interface {
	Load() (Settings, error)
	Save(Settings) error
}

type FileStore struct {
	path string
}

type Settings struct {
	Proxy              string       `json:"proxy"`
	Exclude            []string     `json:"exclude"`
	Priority           []string     `json:"priority"`
	ConnectionQuality  int          `json:"connection_quality"`
	EnableBadgesEmotes bool         `json:"enable_badges_emotes"`
	PriorityMode       PriorityMode `json:"priority_mode"`

	SmartPrioritySafetyMinutes int              `json:"smart_priority_safety_minutes"`
	WatchStallMinutes          int              `json:"watch_stall_minutes"`
	Log                        LoggingSettings  `json:"log"`
	Mainland                   MainlandSettings `json:"mainland"`
}

func DefaultSettings() Settings {
	return Settings{
		Exclude:                    []string{},
		Priority:                   []string{},
		ConnectionQuality:          1,
		PriorityMode:               PriorityOnly,
		SmartPrioritySafetyMinutes: DefaultSmartPrioritySafetyMinutes,
		WatchStallMinutes:          DefaultWatchStallMinutes,
		Log: LoggingSettings{
			Level:        "info",
			Format:       "text",
			FileEnabled:  true,
			AddSource:    false,
			MaxSizeBytes: 10 * 1024 * 1024,
			MaxBackups:   3,
		},
	}
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: strings.TrimSpace(path)}
}

func (s Settings) Clone() Settings {
	cloned := s
	cloned.Exclude = append([]string(nil), s.Exclude...)
	cloned.Priority = append([]string(nil), s.Priority...)
	return cloned
}

func (s Settings) Sanitized() Settings {
	sanitized := s.Clone()
	sanitized.Proxy = sanitizeProxyURL(sanitized.Proxy)
	return sanitized
}

func (s Settings) IsZero() bool {
	return s.Proxy == "" &&
		len(s.Exclude) == 0 &&
		len(s.Priority) == 0 &&
		s.ConnectionQuality == 0 &&
		!s.EnableBadgesEmotes &&
		s.PriorityMode == "" &&
		s.SmartPrioritySafetyMinutes == 0 &&
		s.WatchStallMinutes == 0 &&
		s.Log == (LoggingSettings{}) &&
		s.Mainland == (MainlandSettings{})
}

func (s Settings) MainlandEnabled() bool {
	return s.Mainland.Enabled
}

func (f *FileStore) Load() (Settings, error) {
	return Load(f.path)
}

func (f *FileStore) Save(settings Settings) error {
	return Save(f.path, settings)
}

func Load(path string) (Settings, error) {
	settings, err := storage.LoadJSONFile(path, DefaultSettings())
	if err != nil {
		return Settings{}, fmt.Errorf("读取配置文件失败: %w", err)
	}

	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}

	return settings, nil
}

func Save(path string, settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	return writeSettings(path, settings)
}

func writeSettings(path string, settings Settings) error {
	if err := storage.SaveJSONFile(path, settings); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	_ = secure.HardenFile(path)

	return nil
}

type EnsureOutcome int

const (
	EnsureUnchanged EnsureOutcome = iota
	EnsureCreated
	EnsureUpdated
)

func EnsureFile(path string, settings Settings) (EnsureOutcome, error) {
	if err := settings.Validate(); err != nil {
		return EnsureUnchanged, err
	}

	want, err := storage.MarshalJSONFile(settings)
	if err != nil {
		return EnsureUnchanged, fmt.Errorf("序列化配置失败: %w", err)
	}

	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, want) {
		return EnsureUnchanged, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return EnsureUnchanged, fmt.Errorf("读取配置文件失败: %w", err)
	}

	existed := err == nil
	if err := writeSettings(path, settings); err != nil {
		return EnsureUnchanged, err
	}
	if existed {
		return EnsureUpdated, nil
	}
	return EnsureCreated, nil
}
