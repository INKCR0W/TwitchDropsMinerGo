package config

import (
	"fmt"
	"net/url"
	"strings"
)

func (s *Settings) Validate() error {
	if s == nil {
		return fmt.Errorf("配置不能为空")
	}

	defaults := DefaultSettings()

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
	case PriorityOnly, EndingSoonest, LowAvailabilityFirst, SmartBalance:
	default:
		return fmt.Errorf("priority_mode %q 不受支持", s.PriorityMode)
	}

	if s.SmartPrioritySafetyMinutes < 0 {
		return fmt.Errorf("smart_priority_safety_minutes 不能小于 0")
	}
	if s.SmartPrioritySafetyMinutes == 0 {
		s.SmartPrioritySafetyMinutes = defaults.SmartPrioritySafetyMinutes
	}

	if s.WatchStallMinutes < 0 {
		return fmt.Errorf("watch_stall_minutes 不能小于 0")
	}
	if s.WatchStallMinutes == 0 {
		s.WatchStallMinutes = defaults.WatchStallMinutes
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

	if s.Log.MaxSizeBytes < 0 {
		return fmt.Errorf("日志最大大小不能小于 0")
	}
	if s.Log.MaxBackups < 0 {
		return fmt.Errorf("日志备份数量不能小于 0")
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
