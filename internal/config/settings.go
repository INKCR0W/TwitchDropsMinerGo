package config

import (
	"fmt"
	"net/url"
	"strings"

	"twitchdropsminergo/internal/storage"
)

type PriorityMode string

const (
	PriorityOnly         PriorityMode = "priority_only"
	EndingSoonest        PriorityMode = "ending_soonest"
	LowAvailabilityFirst PriorityMode = "low_availability_first"
)

type LoggingSettings struct {
	Level       string `json:"level"`
	Format      string `json:"format"`
	FileEnabled bool   `json:"file_enabled"`
	AddSource   bool   `json:"add_source"`
}

type Store interface {
	Load() (Settings, error)
	Save(Settings) error
}

type FileStore struct {
	path string
}

type Settings struct {
	Proxy               string          `json:"proxy"`
	Language            string          `json:"language"`
	DarkMode            bool            `json:"dark_mode"`
	Exclude             []string        `json:"exclude"`
	Priority            []string        `json:"priority"`
	AutostartTray       bool            `json:"autostart_tray"`
	ConnectionQuality   int             `json:"connection_quality"`
	TrayNotifications   bool            `json:"tray_notifications"`
	EnableBadgesEmotes  bool            `json:"enable_badges_emotes"`
	AvailableDropsCheck bool            `json:"available_drops_check"`
	PriorityMode        PriorityMode    `json:"priority_mode"`
	Log                 LoggingSettings `json:"log"`
}

func DefaultSettings() Settings {
	return Settings{
		Language:          "English",
		Exclude:           []string{},
		Priority:          []string{},
		ConnectionQuality: 1,
		TrayNotifications: true,
		PriorityMode:      PriorityOnly,
		Log: LoggingSettings{
			Level:       "info",
			Format:      "text",
			FileEnabled: true,
			AddSource:   false,
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
		s.Language == "" &&
		!s.DarkMode &&
		len(s.Exclude) == 0 &&
		len(s.Priority) == 0 &&
		!s.AutostartTray &&
		s.ConnectionQuality == 0 &&
		!s.TrayNotifications &&
		!s.EnableBadgesEmotes &&
		!s.AvailableDropsCheck &&
		s.PriorityMode == "" &&
		s.Log == (LoggingSettings{})
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

	if err := storage.SaveJSONFile(path, settings); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	return nil
}

func (s *Settings) Validate() error {
	if s == nil {
		return fmt.Errorf("配置不能为空")
	}

	defaults := DefaultSettings()

	s.Language = strings.TrimSpace(s.Language)
	if s.Language == "" {
		s.Language = defaults.Language
	}

	s.Proxy = strings.TrimSpace(s.Proxy)
	if s.Proxy != "" {
		parsedProxy, err := url.Parse(s.Proxy)
		if err != nil {
			return fmt.Errorf("代理地址无效: %w", err)
		}
		if parsedProxy.Scheme == "" || parsedProxy.Host == "" {
			return fmt.Errorf("代理地址必须包含协议和主机")
		}
	}

	s.Priority = normalizeStringList(s.Priority)
	s.Exclude = normalizeStringList(s.Exclude)

	if s.ConnectionQuality < 1 || s.ConnectionQuality > 6 {
		return fmt.Errorf("connection_quality 必须在 1 到 6 之间")
	}

	if s.PriorityMode == "" {
		s.PriorityMode = defaults.PriorityMode
	}
	switch s.PriorityMode {
	case PriorityOnly, EndingSoonest, LowAvailabilityFirst:
	default:
		return fmt.Errorf("priority_mode %q 不受支持", s.PriorityMode)
	}

	s.Log.Level = strings.ToLower(strings.TrimSpace(s.Log.Level))
	if s.Log.Level == "" {
		s.Log.Level = defaults.Log.Level
	}
	switch s.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("日志级别 %q 不受支持", s.Log.Level)
	}

	s.Log.Format = strings.ToLower(strings.TrimSpace(s.Log.Format))
	if s.Log.Format == "" {
		s.Log.Format = defaults.Log.Format
	}
	switch s.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("日志格式 %q 不受支持", s.Log.Format)
	}

	return nil
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}

	return normalized
}

func sanitizeProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	parsed.User = nil
	return parsed.String()
}
