package inventory

import (
	"context"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

type rawGQLClient interface {
	DoRaw(context.Context, gql.RawQuery) (gql.Response, error)
}

const catalogAccountFields = `id self { isAccountConnected } timeBasedDrops {
 id self { isClaimed currentMinutesWatched dropInstanceID }
}`

const catalogAccountGameQuery = `query CatalogAccounts($gameID: ID!, $after: Cursor) {
 game(id: $gameID) {
  streams(first: 20, after: $after, options: {systemFilters: [DROPS_ENABLED], sort: VIEWER_COUNT}) {
   edges { cursor node { channel { viewerDropCampaigns { ` + catalogAccountFields + ` } } } }
   pageInfo { hasNextPage }
  }
 }
}`

const catalogAccountChannelQuery = `query CatalogChannelAccounts($channelID: ID!) {
 channel(id: $channelID) { viewerDropCampaigns { ` + catalogAccountFields + ` } }
}`

func (r *Refresher) resolveCatalogAccounts(ctx context.Context, payload map[string]any, options RefreshOptions) error {
	pending := make(map[string]map[string]any)
	for id, value := range payload {
		campaign := optionalMap(value)
		if !catalogNeedsAccount(campaign, options) {
			continue
		}
		pending[id] = campaign
	}
	if len(pending) == 0 {
		return nil
	}
	client, ok := r.gqlClient.(rawGQLClient)
	if !ok {
		return fmt.Errorf("公共目录账号资格校验需要原始 GQL 查询支持")
	}
	total := len(pending)
	checked := make([]map[string]any, 0, total)
	for _, campaign := range pending {
		checked = append(checked, campaign)
	}
	queriedChannels := make(map[string]struct{})
	queriedGames := make(map[string]struct{})
	for _, id := range orderedAccountCampaigns(pending, options.PriorityGames) {
		campaign := pending[id]
		if campaign == nil {
			continue
		}
		allowed := parseAllowedChannels(campaign["allow"])
		if len(allowed) == 0 {
			gameID := stringValue(optionalMap(campaign["game"]), "id")
			if _, seen := queriedGames[gameID]; seen {
				continue
			}
			queriedGames[gameID] = struct{}{}
			if err := r.resolveCatalogGameAccounts(ctx, client, gameID, pending); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				for pendingID, unresolved := range pending {
					if stringValue(optionalMap(unresolved["game"]), "id") == gameID && len(parseAllowedChannels(unresolved["allow"])) == 0 {
						r.logger.Warn("游戏目录活动账号资格仍未确认", "campaign_id", pendingID, "campaign_name", stringValue(unresolved, "name"), "game_id", gameID, "error", err)
					}
				}
			}
			continue
		}
		queried, failed := 0, 0
		for offset := 0; offset < len(allowed) && pending[id] != nil; {
			if err := ctx.Err(); err != nil {
				return err
			}
			channelIDs := make([]string, 0, catalogAccountChannelBatchSize)
			for offset < len(allowed) && len(channelIDs) < catalogAccountChannelBatchSize {
				channelID := fmt.Sprint(allowed[offset].ID)
				offset++
				if _, seen := queriedChannels[channelID]; seen {
					continue
				}
				queriedChannels[channelID] = struct{}{}
				channelIDs = append(channelIDs, channelID)
			}
			if len(channelIDs) == 0 {
				continue
			}
			queried += len(channelIDs)
			if err := resolveCatalogChannelAccounts(ctx, client, channelIDs, pending); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed++
				r.logger.Warn("校验目录活动账号资格失败", "campaign_id", id, "channel_count", len(channelIDs), "error", err)
			}
		}
		if pending[id] != nil {
			r.logger.Warn("白名单活动账号资格仍未确认", "campaign_id", id, "campaign_name", stringValue(campaign, "name"), "allowed_channel_count", len(allowed), "queried_channel_count", queried, "failed_request_count", failed, "reason", "白名单频道未返回完整账号数据")
		}
	}
	linked, unlinked := 0, 0
	for _, campaign := range checked {
		if connected, known := optionalMap(campaign["self"])["isAccountConnected"].(bool); known {
			if connected {
				linked++
			} else {
				unlinked++
			}
		}
	}
	r.logger.Info("目录活动账号资格校验完成", "candidate_count", total, "linked_count", linked, "unlinked_count", unlinked, "unknown_count", len(pending))
	return nil
}

func catalogNeedsAccount(campaign map[string]any, options RefreshOptions) bool {
	if !boolValue(campaign, catalogUnknownAccountKey) {
		return false
	}
	if _, known := optionalMap(campaign["self"])["isAccountConnected"].(bool); known {
		return false
	}
	if !options.EnableBadgesEmotes && catalogHasBadgeOrEmote(campaign) {
		return false
	}
	game := parseGame(optionalMap(campaign["game"]))
	return !accountGameListed(game.Name, options.ExcludedGames) &&
		(!options.PriorityOnly || accountGameListed(game.Name, options.PriorityGames))
}

func catalogHasBadgeOrEmote(campaign map[string]any) bool {
	drops, _ := campaign["timeBasedDrops"].([]any)
	for _, value := range drops {
		benefits, _ := optionalMap(value)["benefitEdges"].([]any)
		for _, edge := range benefits {
			benefit := optionalMap(optionalMap(edge)["benefit"])
			if domain.ParseBenefitType(stringValue(benefit, "distributionType")).IsBadgeOrEmote() {
				return true
			}
		}
	}
	return false
}

func accountGameListed(name string, names []string) bool {
	for _, item := range names {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}

func pendingAccountGame(pending map[string]map[string]any, gameID string) bool {
	for _, campaign := range pending {
		if stringValue(optionalMap(campaign["game"]), "id") == gameID && len(parseAllowedChannels(campaign["allow"])) == 0 {
			return true
		}
	}
	return false
}

func (r *Refresher) resolveCatalogGameAccounts(ctx context.Context, client rawGQLClient, gameID string, pending map[string]map[string]any) error {
	if !pendingAccountGame(pending, gameID) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var after any
	seen := make(map[string]struct{})
	pages, channels := 0, 0
	for pendingAccountGame(pending, gameID) {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := client.DoRaw(ctx, gql.RawQuery{Query: catalogAccountGameQuery, Variables: map[string]any{"gameID": gameID, "after": after}})
		if err != nil {
			return err
		}
		pages++
		streams := optionalMap(optionalMap(optionalMap(response.Data)["game"])["streams"])
		edges, err := sliceFromMap(streams, "edges")
		if err != nil {
			return err
		}
		channels += len(edges)
		for _, value := range edges {
			edge := optionalMap(value)
			channel := optionalMap(optionalMap(edge["node"])["channel"])
			applyCatalogAccounts(pending, channel["viewerDropCampaigns"])
		}
		if !pendingAccountGame(pending, gameID) {
			break
		}
		pageInfo := optionalMap(streams["pageInfo"])
		hasNext, known := pageInfo["hasNextPage"].(bool)
		if !known {
			return fmt.Errorf("游戏 %s 资格查询缺少分页状态", gameID)
		}
		if !hasNext {
			break
		}
		if len(edges) == 0 {
			return fmt.Errorf("游戏 %s 资格查询下一页为空", gameID)
		}
		cursor := stringValue(optionalMap(edges[len(edges)-1]), "cursor")
		if _, repeated := seen[cursor]; cursor == "" || repeated {
			return fmt.Errorf("游戏 %s 资格查询分页游标未前进", gameID)
		}
		seen[cursor] = struct{}{}
		after = cursor
	}
	for id, campaign := range pending {
		if stringValue(optionalMap(campaign["game"]), "id") == gameID && len(parseAllowedChannels(campaign["allow"])) == 0 {
			r.logger.Warn("游戏目录活动账号资格仍未确认", "campaign_id", id, "campaign_name", stringValue(campaign, "name"), "page_count", pages, "channel_count", channels, "reason", "在线频道已查完，未返回完整账号数据")
		}
	}
	return nil
}

func applyCatalogAccounts(pending map[string]map[string]any, value any) {
	campaigns, ok := value.([]any)
	if !ok {
		return
	}
	for _, value := range campaigns {
		observed := optionalMap(value)
		id := stringValue(observed, "id")
		campaign := pending[id]
		self := optionalMap(observed["self"])
		if _, known := self["isAccountConnected"].(bool); campaign == nil || !known {
			continue
		}
		// 频道查询补齐目录缺少的账号数据，库存里已有的进度仍优先
		observedDrops, _ := observed["timeBasedDrops"].([]any)
		byID := make(map[string]map[string]any, len(observedDrops))
		for _, value := range observedDrops {
			drop := optionalMap(value)
			byID[stringValue(drop, "id")] = optionalMap(drop["self"])
		}
		drops, _ := campaign["timeBasedDrops"].([]any)
		if boolValue(self, "isAccountConnected") && !catalogDropAccountsComplete(drops, byID) {
			continue
		}
		campaign["self"] = cloneMap(self)
		for _, value := range drops {
			drop := optionalMap(value)
			observedSelf := byID[stringValue(drop, "id")]
			if observedSelf == nil {
				continue
			}
			merged := cloneMap(observedSelf)
			for key, value := range optionalMap(drop["self"]) {
				merged[key] = value
			}
			drop["self"] = merged
		}
		delete(pending, id)
	}
}

func catalogDropAccountsComplete(drops []any, observed map[string]map[string]any) bool {
	for _, value := range drops {
		drop := optionalMap(value)
		self := optionalMap(drop["self"])
		if _, known := self["isClaimed"].(bool); known {
			continue
		}
		if _, known := observed[stringValue(drop, "id")]["isClaimed"].(bool); !known {
			return false
		}
	}
	return true
}
