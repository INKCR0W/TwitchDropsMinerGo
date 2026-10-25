package inventory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

const discoveryChannelLimit = 10

type discoveryChannel struct {
	id, login string
	game      map[string]any
}

func (r *Refresher) fetchChannelCampaigns(ctx context.Context, inventory map[string]any, options RefreshOptions) (map[string]any, error) {
	r.discoveryMu.Lock()
	defer r.discoveryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nextCursor := r.discoveryCursor
	var globalGames []domain.Game
	if options.GlobalDiscovery {
		var cursor string
		var err error
		globalGames, cursor, err = r.fetchGlobalDiscoveryGames(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.logger.Warn("全局游戏目录暂不可用，继续发现配置与库存游戏", "error", err)
		} else {
			nextCursor = cursor
			r.logger.Info("TV 全局活动按游戏目录逐页发现，仅覆盖当前在线频道", "page_games", len(globalGames), "has_next_page", cursor != "")
		}
	}
	games := discoveryGames(inventory, options, globalGames)
	if len(games) == 0 {
		r.logger.Warn("TV 活动发现没有可扫描的游戏，请在 priority 中添加游戏名称")
	}
	payload := r.unexpiredDiscoveredCampaigns(options.DiscoveryExcludes)
	seenChannels := make(map[string]struct{})
	for _, game := range games {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		game, err := r.resolveDiscoveryGame(ctx, game)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.logger.Warn("跳过暂时无法解析的游戏", "game", game.Name, "error", err)
			continue
		}
		channels, err := r.fetchDiscoveryChannels(ctx, game)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.logger.Warn("跳过暂时无法扫描的游戏目录", "game", game.Name, "error", err)
			continue
		}
		uniqueChannels := make([]discoveryChannel, 0, len(channels))
		for _, channel := range channels {
			if _, exists := seenChannels[channel.id]; exists {
				continue
			}
			seenChannels[channel.id] = struct{}{}
			uniqueChannels = append(uniqueChannels, channel)
		}
		campaigns, err := r.fetchChannelOffers(ctx, uniqueChannels)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.logger.Warn("频道活动扫描暂不可用，保留库存与已发现活动", "game", game.Name, "error", err)
			continue
		}
		for _, campaign := range campaigns {
			id := stringValue(campaign, "id")
			if existing := optionalMap(payload[id]); existing != nil {
				merged, err := mergeCampaignPayloads(map[string]any{id: campaign}, map[string]any{id: existing})
				if err != nil {
					r.logger.Warn("跳过无法合并的频道活动", "campaign_id", id, "error", err)
					continue
				}
				campaign = optionalMap(merged[id])
				if start := timeValue(existing, "startAt"); !start.IsZero() && start.Before(timeValue(campaign, "startAt")) {
					campaign["startAt"] = existing["startAt"]
				}
				mergeDiscoveryChannels(campaign, existing)
			}
			payload[id] = campaign
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	merged, err := mergeCampaignPayloads(inventory, payload)
	if err != nil {
		return nil, err
	}
	r.discoveryCursor = nextCursor
	r.discoveredCampaigns = payload
	return merged, nil
}

func discoveryGames(inventory map[string]any, options RefreshOptions, additional []domain.Game) []domain.Game {
	excluded := make(map[string]struct{}, len(options.DiscoveryExcludes))
	for _, name := range options.DiscoveryExcludes {
		excluded[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	seen := make(map[string]struct{})
	knownByName := make(map[string]domain.Game)
	for _, value := range inventory {
		game := parseGame(optionalMap(optionalMap(value)["game"]))
		if game.SlugText != "" {
			knownByName[strings.ToLower(game.Name)] = game
		}
	}
	for _, game := range additional {
		if game.SlugText != "" {
			knownByName[strings.ToLower(game.Name)] = game
		}
	}
	games := make([]domain.Game, 0, len(options.DiscoveryGames)+len(inventory)+len(additional))
	add := func(game domain.Game) {
		if _, skip := excluded[strings.ToLower(game.Name)]; skip || game.Slug() == "" {
			return
		}
		if _, exists := seen[game.Slug()]; exists {
			return
		}
		seen[game.Slug()] = struct{}{}
		games = append(games, game)
	}
	for _, name := range options.DiscoveryGames {
		game := domain.Game{Name: strings.TrimSpace(name)}
		if known, ok := knownByName[strings.ToLower(game.Name)]; ok {
			game = known
		}
		add(game)
	}
	ids := make([]string, 0, len(inventory))
	for id := range inventory {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		add(parseGame(optionalMap(optionalMap(inventory[id])["game"])))
	}
	for _, game := range additional {
		add(game)
	}
	return games
}

func mergeDiscoveryChannels(campaign, existing map[string]any) {
	allow := optionalMap(campaign["allow"])
	channels := allow["channels"].([]any)
	seen := make(map[string]struct{})
	for _, value := range channels {
		seen[stringValue(optionalMap(value), "id")] = struct{}{}
	}
	oldChannels, _ := optionalMap(existing["allow"])["channels"].([]any)
	for _, value := range oldChannels {
		id := stringValue(optionalMap(value), "id")
		if _, exists := seen[id]; !exists {
			channels = append(channels, cloneValue(value))
			seen[id] = struct{}{}
		}
	}
	allow["channels"] = channels
}

func (r *Refresher) fetchDiscoveryChannels(ctx context.Context, game domain.Game) ([]discoveryChannel, error) {
	operation, err := gql.MustLookup(gql.OperationGameDirectory).WithVariables(map[string]any{
		"slug": game.Slug(), "limit": discoveryChannelLimit,
		"options": map[string]any{"systemFilters": []any{"DROPS_ENABLED"}, "sort": "VIEWER_COUNT"},
	})
	if err != nil {
		return nil, err
	}
	response, err := r.gqlClient.Do(ctx, operation)
	if err != nil {
		return nil, fmt.Errorf("扫描游戏 %q 的直播频道失败: %w", game.Name, err)
	}
	gameRoot, err := nestedValue(response.Data, "game")
	if err != nil {
		return nil, err
	}
	if gameRoot == nil {
		r.logger.Warn("未找到 TV 活动发现所需的游戏目录", "game", game.Name, "slug", game.Slug())
		return nil, nil
	}
	streamRoot, err := nestedValue(gameRoot, "streams", "edges")
	if err != nil {
		return nil, err
	}
	edges, err := asSlice(streamRoot, "GameDirectory.game.streams.edges")
	if err != nil {
		return nil, err
	}
	channels := make([]discoveryChannel, 0, len(edges))
	for _, edge := range edges {
		node := optionalMap(optionalMap(edge)["node"])
		broadcaster := optionalMap(node["broadcaster"])
		id, login := stringValue(broadcaster, "id"), stringValue(broadcaster, "login")
		if id == "" || login == "" {
			continue
		}
		gameData := cloneMap(optionalMap(node["game"]))
		if int64Value(gameData, "id") <= 0 {
			return nil, fmt.Errorf("游戏目录频道 %s 缺少 game.id", login)
		}
		if stringValue(gameData, "slug") == "" {
			gameData["slug"] = game.Slug()
		}
		channels = append(channels, discoveryChannel{id: id, login: login, game: gameData})
		if len(channels) == discoveryChannelLimit {
			break
		}
	}
	return channels, nil
}

func (r *Refresher) fetchChannelOffers(ctx context.Context, channels []discoveryChannel) ([]map[string]any, error) {
	if len(channels) == 0 {
		return nil, nil
	}
	operations := make([]gql.Operation, 0, len(channels))
	for _, channel := range channels {
		operation, err := gql.MustLookup(gql.OperationAvailableDrops).WithVariables(map[string]any{"channelID": channel.id})
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	responses, err := r.gqlClient.DoBatch(ctx, operations)
	if err != nil {
		return nil, err
	}
	if len(responses) != len(channels) {
		return nil, fmt.Errorf("频道活动批量响应数量不匹配: %d vs %d", len(responses), len(channels))
	}
	r.completeDiscoveryBenefitTypes(ctx, channels, responses)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result []map[string]any
	for index, response := range responses {
		campaigns, err := r.parseChannelOffers(response, channels[index])
		if err != nil {
			r.logger.Warn("跳过无法解析的频道活动", "channel", channels[index].login, "error", err)
			continue
		}
		result = append(result, campaigns...)
	}
	return result, nil
}

func (r *Refresher) parseChannelOffers(response gql.Response, channel discoveryChannel) ([]map[string]any, error) {
	channelRoot, err := nestedValue(response.Data, "channel")
	if err != nil {
		return nil, err
	}
	if channelRoot == nil {
		return nil, nil
	}
	offers, err := nestedValue(channelRoot, "viewerDropCampaigns")
	if err != nil {
		return nil, err
	}
	if offers == nil {
		return nil, nil
	}
	list, err := asSlice(offers, "AvailableDrops.channel.viewerDropCampaigns")
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(list))
	for _, value := range list {
		campaign, err := normalizeChannelCampaign(value, channel)
		if err != nil {
			r.logger.Warn("跳过无法解析的频道活动", "channel", channel.login, "error", err)
			continue
		}
		if campaign != nil {
			result = append(result, campaign)
		}
	}
	return result, nil
}

func normalizeChannelCampaign(value any, channel discoveryChannel) (map[string]any, error) {
	raw, err := asMap(value, "AvailableDrops.campaign")
	if err != nil {
		return nil, err
	}
	campaign := cloneMap(raw)
	if stringValue(campaign, "id") == "" {
		return nil, fmt.Errorf("AvailableDrops campaign 缺少 id")
	}
	// 频道查询不返回账户关联状态；允许开始观看，后续以 Inventory 的 self 为准
	campaign["self"] = map[string]any{"isAccountConnected": true}
	campaign["allow"] = map[string]any{"isEnabled": true, "channels": []any{discoveryChannelPayload(channel)}}
	campaignGame := optionalMap(campaign["game"])
	if int64Value(campaignGame, "id") == 0 || int64Value(campaignGame, "id") == int64Value(channel.game, "id") {
		campaign["game"], err = mergeMaps(campaignGame, channel.game)
		if err != nil {
			return nil, err
		}
	}
	drops, err := sliceFromMap(campaign, "timeBasedDrops")
	if err != nil {
		return nil, err
	}
	watchDrops := make([]any, 0, len(drops))
	var startsAt time.Time
	for _, value := range drops {
		drop, err := asMap(value, "AvailableDrops.timeBasedDrops")
		if err != nil {
			return nil, err
		}
		if int64Value(drop, "requiredSubs") > 0 {
			continue
		}
		if err := validateDiscoveryBenefits(drop); err != nil {
			return nil, fmt.Errorf("活动 %s 的奖励类型不完整: %w", stringValue(campaign, "id"), err)
		}
		if start := timeValue(drop, "startAt"); !start.IsZero() && (startsAt.IsZero() || start.Before(startsAt)) {
			startsAt = start
		}
		// 保留 drop 自身时间；仅在缺失时使用服务端提供的活动时间
		for _, field := range []string{"startAt", "endAt"} {
			if stringValue(drop, field) == "" {
				drop[field] = campaign[field]
			}
		}
		watchDrops = append(watchDrops, drop)
	}
	if len(watchDrops) == 0 {
		return nil, nil
	}
	campaign["timeBasedDrops"] = watchDrops
	if timeValue(campaign, "startAt").IsZero() && !startsAt.IsZero() {
		campaign["startAt"] = startsAt.Format(time.RFC3339Nano)
	}
	return campaign, nil
}

func discoveryChannelPayload(channel discoveryChannel) map[string]any {
	return map[string]any{"id": channel.id, "name": channel.login}
}

func mergeCampaignPayloads(primary, secondary map[string]any) (map[string]any, error) {
	merged, err := mergeMaps(primary, secondary)
	if err != nil {
		return nil, err
	}
	for id, value := range primary {
		known := optionalMap(value)
		found := optionalMap(secondary[id])
		if found == nil {
			continue
		}
		knownDrops, err := sliceFromMap(known, "timeBasedDrops")
		if err != nil {
			return nil, err
		}
		foundDrops, err := sliceFromMap(found, "timeBasedDrops")
		if err != nil {
			return nil, err
		}
		byID := make(map[string]any)
		order := make([]string, 0, len(knownDrops)+len(foundDrops))
		for _, value := range append(knownDrops, foundDrops...) {
			drop, err := asMap(value, "campaign.timeBasedDrops")
			if err != nil {
				return nil, err
			}
			dropID := stringValue(drop, "id")
			if primary, exists := byID[dropID]; exists {
				byID[dropID], err = mergeMaps(optionalMap(primary), drop)
				if err != nil {
					return nil, err
				}
			} else {
				byID[dropID] = cloneMap(drop)
				order = append(order, dropID)
			}
		}
		drops := make([]any, 0, len(order))
		for _, id := range order {
			drops = append(drops, byID[id])
		}
		optionalMap(merged[id])["timeBasedDrops"] = drops
	}
	return merged, nil
}
