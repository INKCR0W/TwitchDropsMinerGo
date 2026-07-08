package inventory

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func mergeMaps(primary map[string]any, secondary map[string]any) (map[string]any, error) {
	merged := make(map[string]any, len(primary)+len(secondary))
	for key, value := range primary {
		if secondaryValue, ok := secondary[key]; ok {
			mergedValue, err := mergeValues(value, secondaryValue)
			if err != nil {
				return nil, err
			}
			merged[key] = mergedValue
			continue
		}
		merged[key] = cloneValue(value)
	}
	for key, value := range secondary {
		if _, exists := primary[key]; exists {
			continue
		}
		merged[key] = cloneValue(value)
	}

	return merged, nil
}

func mergeValues(primary any, secondary any) (any, error) {
	primaryNil := isNilValue(primary)
	secondaryNil := isNilValue(secondary)
	if primaryNil || secondaryNil {
		if primaryNil && secondaryNil {
			return nil, nil
		}
		if primaryNil {
			return cloneValue(secondary), nil
		}
		return cloneValue(primary), nil
	}

	if reflect.TypeOf(primary) != reflect.TypeOf(secondary) {
		return nil, fmt.Errorf("合并数据类型不一致: %T vs %T", primary, secondary)
	}

	switch typed := primary.(type) {
	case map[string]any:
		return mergeMaps(typed, secondary.(map[string]any))
	case []any:
		return cloneSlice(typed), nil
	default:
		return cloneValue(primary), nil
	}
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneSlice(source []any) []any {
	cloned := make([]any, len(source))
	for index, value := range source {
		cloned[index] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		return cloneSlice(typed)
	default:
		return typed
	}
}

func nestedValue(root any, path ...string) (any, error) {
	current := root
	for _, part := range path {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("路径 %q 的父节点不是对象", strings.Join(path, "."))
		}

		value, exists := currentMap[part]
		if !exists {
			return nil, fmt.Errorf("缺少字段 %q", strings.Join(path, "."))
		}
		current = value
	}

	return current, nil
}

func asMap(value any, label string) (map[string]any, error) {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是对象", label)
	}
	return mapValue, nil
}

func asSlice(value any, label string) ([]any, error) {
	if sliceValue, ok := value.([]any); ok {
		return sliceValue, nil
	}

	reflectValue := reflect.ValueOf(value)
	if !reflectValue.IsValid() {
		return nil, fmt.Errorf("%s 不是数组", label)
	}
	if reflectValue.Kind() != reflect.Slice && reflectValue.Kind() != reflect.Array {
		return nil, fmt.Errorf("%s 不是数组", label)
	}

	result := make([]any, reflectValue.Len())
	for index := range result {
		result[index] = reflectValue.Index(index).Interface()
	}
	return result, nil
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
	return asSlice(value, key)
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
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func boolValue(source map[string]any, key string) bool {
	if len(source) == 0 {
		return false
	}

	value, ok := source[key]
	if !ok || value == nil {
		return false
	}

	typed, ok := value.(bool)
	return ok && typed
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

func intValue(source map[string]any, key string) int {
	return int(int64Value(source, key))
}

func int64ValuePresent(source map[string]any, key string) (int64, bool) {
	if len(source) == 0 {
		return 0, false
	}

	value, ok := source[key]
	if !ok || value == nil {
		return 0, false
	}

	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
			return parsed, true
		}
	}

	return 0, false
}

func timeValue(source map[string]any, key string) time.Time {
	raw := stringValue(source, key)
	if raw == "" {
		return time.Time{}
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}

	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
