package scheduler

import (
	"fmt"
	"strconv"
	"strings"

	"twitchdropsminergo/internal/domain"
)

func parseGame(data map[string]any) *domain.Game {
	if len(data) == 0 {
		return nil
	}

	game := domain.Game{
		ID:       int64Value(data, "id"),
		Name:     firstNonEmpty(stringValue(data, "displayName"), stringValue(data, "name")),
		SlugText: stringValue(data, "slug"),
	}
	if game.ID == 0 && game.Name == "" {
		return nil
	}
	return &game
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func asMap(value any, label string) (map[string]any, error) {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是对象", label)
	}
	return mapValue, nil
}

func optionalMap(value any) map[string]any {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return mapValue
}

func mapFromMap(source map[string]any, key string) (map[string]any, error) {
	value, ok := source[key]
	if !ok {
		return nil, fmt.Errorf("缺少字段 %q", key)
	}
	return asMap(value, key)
}

func sliceFromMap(source map[string]any, key string) ([]any, error) {
	value, ok := source[key]
	if !ok || value == nil {
		return nil, nil
	}
	sliceValue, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是数组", key)
	}
	return sliceValue, nil
}

func stringValue(source map[string]any, key string) string {
	if len(source) == 0 {
		return ""
	}
	value, ok := source[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func int64Value(source map[string]any, key string) int64 {
	if len(source) == 0 {
		return 0
	}
	value, ok := source[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}
