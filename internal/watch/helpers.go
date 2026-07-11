package watch

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
)

func chunkSpecs(specs []channelSpec, size int) [][]channelSpec {
	if len(specs) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(specs)
	}

	chunks := make([][]channelSpec, 0, (len(specs)+size-1)/size)
	for start := 0; start < len(specs); start += size {
		end := start + size
		if end > len(specs) {
			end = len(specs)
		}
		chunk := append([]channelSpec(nil), specs[start:end]...)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func cloneChannel(channel *domain.Channel) domain.Channel {
	if channel == nil {
		return domain.Channel{}
	}

	cloned := *channel
	cloned.Stream = cloneStream(channel.Stream)
	return cloned
}

func cloneStream(stream *domain.Stream) *domain.Stream {
	if stream == nil {
		return nil
	}

	cloned := *stream
	if stream.Game != nil {
		game := *stream.Game
		cloned.Game = &game
	}
	cloned.OfferedCampaignIDs = slices.Clone(stream.OfferedCampaignIDs)
	return &cloned
}

func parseGame(data map[string]any) *domain.Game {
	if len(data) == 0 {
		return nil
	}

	name := firstNonEmpty(stringValue(data, "displayName"), stringValue(data, "name"))
	if name == "" && int64Value(data, "id") == 0 {
		return nil
	}

	game := domain.Game{
		ID:       int64Value(data, "id"),
		Name:     name,
		SlugText: stringValue(data, "slug"),
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

func nestedMap(root any, label string, path ...string) (map[string]any, error) {
	current := root
	currentPath := label
	for _, part := range path {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s 的父节点不是对象", currentPath)
		}
		value, exists := currentMap[part]
		if !exists {
			return nil, fmt.Errorf("缺少字段 %s.%s", currentPath, part)
		}
		currentPath += "." + part
		current = value
		if isNilValue(current) {
			return nil, nil
		}
	}

	return asMap(current, currentPath)
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

func asSlice(value any, label string) ([]any, error) {
	sliceValue, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是数组", label)
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

func isNilValue(value any) bool {
	return value == nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
