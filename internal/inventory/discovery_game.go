package inventory

import (
	"context"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

// 调用方持有 discoveryMu，缓存只记录服务端确认的 slug
func (r *Refresher) resolveDiscoveryGame(ctx context.Context, game domain.Game) (domain.Game, error) {
	key := strings.ToLower(strings.TrimSpace(game.Name))
	if game.SlugText != "" {
		return game, nil
	}
	if slug := r.discoveryGameSlugs[key]; slug != "" {
		game.SlugText = slug
		return game, nil
	}
	operation, err := gql.MustLookup(gql.OperationSlugRedirect).WithVariables(map[string]any{"name": game.Name})
	if err != nil {
		return game, err
	}
	response, err := r.gqlClient.Do(ctx, operation)
	if err != nil {
		return game, err
	}
	value, err := nestedValue(response.Data, "game")
	if err != nil {
		return game, err
	}
	slug := stringValue(optionalMap(value), "slug")
	if slug == "" {
		return game, fmt.Errorf("未找到游戏 %q 的目录 slug", game.Name)
	}
	if r.discoveryGameSlugs == nil {
		r.discoveryGameSlugs = make(map[string]string)
	}
	r.discoveryGameSlugs[key] = slug
	game.SlugText = slug
	return game, nil
}
