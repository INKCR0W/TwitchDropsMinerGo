package inventory

import (
	"context"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

const globalDiscoveryPageSize = 20

const globalDiscoveryQuery = `query DiscoverGames($first: Int!, $after: Cursor) {
  games(first: $first, after: $after) {
    edges { cursor node { id name slug } }
    pageInfo { hasNextPage }
  }
}`

type rawGQLClient interface {
	DoRaw(context.Context, gql.RawQuery) (gql.Response, error)
}

func (r *Refresher) fetchGlobalDiscoveryGames(ctx context.Context) ([]domain.Game, string, error) {
	client, ok := r.gqlClient.(rawGQLClient)
	if !ok {
		return nil, "", fmt.Errorf("TV 全局活动发现需要支持原始查询的 GQL 客户端")
	}
	var cursor any
	if r.discoveryCursor != "" {
		cursor = r.discoveryCursor
	}
	response, err := client.DoRaw(ctx, gql.RawQuery{
		Query:     globalDiscoveryQuery,
		Variables: map[string]any{"first": globalDiscoveryPageSize, "after": cursor},
	})
	if err != nil {
		return nil, "", fmt.Errorf("请求 TV 全局游戏目录失败: %w", err)
	}
	root, err := nestedValue(response.Data, "games")
	if err != nil {
		return nil, "", err
	}
	games, err := asMap(root, "games")
	if err != nil {
		return nil, "", err
	}
	edges, err := sliceFromMap(games, "edges")
	if err != nil {
		return nil, "", err
	}
	result := make([]domain.Game, 0, len(edges))
	var nextCursor string
	for _, value := range edges {
		edge := optionalMap(value)
		node := optionalMap(edge["node"])
		if name := stringValue(node, "name"); name != "" {
			result = append(result, parseGame(node))
		}
		nextCursor = stringValue(edge, "cursor")
	}
	if !boolValue(optionalMap(games["pageInfo"]), "hasNextPage") {
		nextCursor = ""
	} else if nextCursor == "" || nextCursor == r.discoveryCursor {
		return nil, "", fmt.Errorf("TV 全局游戏目录返回无效分页游标")
	}
	return result, nextCursor, nil
}

func (r *Refresher) unexpiredDiscoveredCampaigns(excludes []string) map[string]any {
	excluded := make(map[string]struct{}, len(excludes))
	for _, name := range excludes {
		excluded[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	payload := make(map[string]any)
	for id, value := range r.discoveredCampaigns {
		campaign := optionalMap(value)
		game := parseGame(optionalMap(campaign["game"]))
		if _, skip := excluded[strings.ToLower(game.Name)]; skip {
			continue
		}
		if timeValue(campaign, "endAt").After(r.now().UTC()) {
			payload[id] = cloneMap(campaign)
		}
	}
	return payload
}
