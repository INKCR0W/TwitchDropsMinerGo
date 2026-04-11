package scheduler

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
)

func viewerSortKey(channel domain.Channel) int {
	if viewers := channel.ViewerCount(); viewers > 0 {
		return viewers
	}
	if channel.Online() {
		return 0
	}
	return -1
}

func priorityNameIndex(name string, priority []string) int {
	for index, item := range priority {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return index
		}
	}
	return math.MaxInt
}

func stringInList(value string, values []string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func gameInList(game domain.Game, games []domain.Game) bool {
	for _, item := range games {
		if sameGame(game, item) {
			return true
		}
	}
	return false
}

func sameGame(left domain.Game, right domain.Game) bool {
	switch {
	case left.ID > 0 && right.ID > 0:
		return left.ID == right.ID
	default:
		return strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name))
	}
}

func gameKey(game domain.Game) string {
	if game.ID > 0 {
		return strconv.FormatInt(game.ID, 10)
	}
	return strings.ToLower(strings.TrimSpace(game.Name))
}

func formatGameNames(games []domain.Game) string {
	if len(games) == 0 {
		return ""
	}

	names := make([]string, 0, len(games))
	for _, game := range games {
		names = append(names, gameName(game))
	}
	return strings.Join(names, ", ")
}

func gameName(game domain.Game) string {
	if strings.TrimSpace(game.Name) != "" {
		return game.Name
	}
	if slug := strings.TrimSpace(game.Slug()); slug != "" {
		return slug
	}
	if game.ID > 0 {
		return strconv.FormatInt(game.ID, 10)
	}
	return ""
}

func uniqueInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(values))
	unique := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func min(left int, right int) int {
	if left < right {
		return left
	}
	return right
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
