package inventory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

type rawGQLClient interface {
	DoRaw(context.Context, gql.RawQuery) (gql.Response, error)
}

const catalogAccountChannelLimit = 3

const catalogAccountFields = `id self { isAccountConnected } timeBasedDrops {
 id self { isClaimed currentMinutesWatched dropInstanceID }
}`

const catalogAccountGameQuery = `query CatalogAccounts($gameID: ID!) {
 game(id: $gameID) {
  streams(first: 3, options: {systemFilters: [DROPS_ENABLED], sort: VIEWER_COUNT}) {
   edges { node { channel { viewerDropCampaigns { ` + catalogAccountFields + ` } } } }
  }
 }
}`

const catalogAccountChannelQuery = `query CatalogChannelAccounts($channelID: ID!) {
 channel(id: $channelID) { viewerDropCampaigns { ` + catalogAccountFields + ` } }
}`

func (r *Refresher) resolveCatalogAccounts(ctx context.Context, payload map[string]any, options RefreshOptions) error {
	pending := make(map[string]map[string]any)
	games := make(map[string]struct{})
	channels := make(map[string]struct{})
	for id, value := range payload {
		campaign := optionalMap(value)
		if !catalogNeedsAccount(campaign, options) {
			continue
		}
		pending[id] = campaign
		allowed := parseAllowedChannels(campaign["allow"])
		if len(allowed) == 0 {
			games[stringValue(optionalMap(campaign["game"]), "id")] = struct{}{}
		}
		for _, channel := range allowed[:min(len(allowed), catalogAccountChannelLimit)] {
			channels[fmt.Sprint(channel.ID)] = struct{}{}
		}
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
	for _, channelID := range sortedAccountIDs(channels) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !pendingAccountChannel(pending, channelID) {
			continue
		}
		response, err := client.DoRaw(ctx, gql.RawQuery{Query: catalogAccountChannelQuery, Variables: map[string]any{"channelID": channelID}})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logger.Warn("校验目录活动账号资格失败", "channel_id", channelID, "error", err)
			continue
		}
		applyCatalogAccounts(pending, optionalMap(optionalMap(response.Data)["channel"])["viewerDropCampaigns"])
	}
	for _, gameID := range sortedAccountIDs(games) {
		if err := r.resolveCatalogGameAccounts(ctx, client, gameID, pending); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logger.Warn("校验目录活动账号资格失败", "game_id", gameID, "error", err)
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

func sortedAccountIDs(values map[string]struct{}) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func pendingAccountChannel(pending map[string]map[string]any, channelID string) bool {
	for _, campaign := range pending {
		for _, channel := range parseAllowedChannels(campaign["allow"]) {
			if fmt.Sprint(channel.ID) == channelID {
				return true
			}
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
	response, err := client.DoRaw(ctx, gql.RawQuery{Query: catalogAccountGameQuery, Variables: map[string]any{"gameID": gameID}})
	if err != nil {
		return err
	}
	streams := optionalMap(optionalMap(optionalMap(response.Data)["game"])["streams"])
	edges, err := sliceFromMap(streams, "edges")
	if err != nil {
		return err
	}
	for _, value := range edges {
		edge := optionalMap(value)
		channel := optionalMap(optionalMap(edge["node"])["channel"])
		applyCatalogAccounts(pending, channel["viewerDropCampaigns"])
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
