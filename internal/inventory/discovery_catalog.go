package inventory

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultCatalogEndpoint   = "https://twitch-drops-api.sunkwi.com/v2/drops"
	catalogMaxAge            = 15 * time.Minute
	catalogClockTolerance    = time.Minute
	catalogUnknownAccountKey = "_catalogAccountConnectionUnknown"
)

type catalogResponse struct {
	LastUpdatedAt          time.Time     `json:"lastUpdatedAt"`
	RefreshIntervalSeconds int           `json:"refreshIntervalSeconds"`
	Data                   []catalogGame `json:"data"`
}

type catalogGame struct {
	GameID          string           `json:"gameId"`
	GameDisplayName string           `json:"gameDisplayName"`
	GameBoxArtURL   string           `json:"gameBoxArtURL"`
	Rewards         []map[string]any `json:"rewards"`
}

func (r *Refresher) fetchCatalogCampaigns(ctx context.Context) (map[string]any, error) {
	response, err := r.catalogHTTPClient.Do(ctx, httpclient.Request{
		Method: http.MethodGet, URL: r.catalogEndpoint,
		Headers: http.Header{"Accept": {"application/json"}, "User-Agent": {"TwitchDropsMinerGo"}},
	})
	if err != nil {
		return nil, fmt.Errorf("获取公共活动目录失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取公共活动目录失败: HTTP %d", response.StatusCode)
	}
	var catalog catalogResponse
	if err := response.DecodeJSON(&catalog); err != nil {
		return nil, fmt.Errorf("解析公共活动目录失败: %w", err)
	}
	now := r.now().UTC()
	if catalog.LastUpdatedAt.IsZero() || catalog.RefreshIntervalSeconds <= 0 || catalog.Data == nil {
		return nil, fmt.Errorf("公共活动目录缺少更新时间、刷新间隔或 data")
	}
	age := now.Sub(catalog.LastUpdatedAt)
	if age > catalogMaxAge || age < -catalogClockTolerance {
		return nil, fmt.Errorf("公共活动目录更新时间异常: last_updated_at=%s age=%s", catalog.LastUpdatedAt.Format(time.RFC3339), age.Round(time.Second))
	}
	payload := make(map[string]any)
	for _, group := range catalog.Data {
		if group.GameID == "" || group.Rewards == nil {
			return nil, fmt.Errorf("公共活动目录游戏项缺少 gameId 或 rewards")
		}
		for _, raw := range group.Rewards {
			campaign, err := catalogCampaign(raw, group, now)
			if err != nil {
				return nil, err
			}
			if campaign == nil {
				continue
			}
			id := stringValue(campaign, "id")
			if existing := optionalMap(payload[id]); existing != nil {
				merged, err := mergeCampaignPayloads(map[string]any{id: existing}, map[string]any{id: campaign})
				if err != nil {
					return nil, fmt.Errorf("合并目录活动 %s 失败: %w", id, err)
				}
				campaign = optionalMap(merged[id])
			}
			payload[id] = campaign
		}
	}
	r.logger.Info("公共活动目录已更新", "last_updated_at", catalog.LastUpdatedAt, "age", age.Round(time.Second).String(), "refresh_interval_seconds", catalog.RefreshIntervalSeconds, "game_count", len(catalog.Data), "active_campaign_count", len(payload))
	return payload, nil
}

func catalogCampaign(raw map[string]any, group catalogGame, now time.Time) (map[string]any, error) {
	id := stringValue(raw, "id")
	if id == "" {
		return nil, fmt.Errorf("公共活动目录包含缺少 id 的活动")
	}
	if !strings.EqualFold(stringValue(raw, "status"), "ACTIVE") {
		return nil, nil
	}
	startsAt, endsAt := timeValue(raw, "startAt"), timeValue(raw, "endAt")
	if startsAt.IsZero() || endsAt.IsZero() || !endsAt.After(startsAt) {
		return nil, fmt.Errorf("公共活动目录活动 %s 的起止时间无效", id)
	}
	if now.Before(startsAt) || !now.Before(endsAt) {
		return nil, nil
	}
	campaign := stripCatalogSelf(raw).(map[string]any)
	campaign[catalogUnknownAccountKey] = true
	game := optionalMap(campaign["game"])
	if stringValue(game, "id") == "" {
		return nil, fmt.Errorf("公共活动目录活动 %s 缺少 game.id", id)
	}
	if stringValue(game, "displayName") == "" && stringValue(game, "name") == "" {
		game["displayName"] = group.GameDisplayName
	}
	if stringValue(game, "boxArtURL") == "" {
		game["boxArtURL"] = group.GameBoxArtURL
	}
	return campaign, nil
}

func stripCatalogSelf(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, nested := range value {
			if key != "self" && key != catalogUnknownAccountKey {
				result[key] = stripCatalogSelf(nested)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, nested := range value {
			result[index] = stripCatalogSelf(nested)
		}
		return result
	default:
		return value
	}
}

func (r *Refresher) filterCatalogCampaigns(payload map[string]any) {
	for id, value := range payload {
		if !boolValue(optionalMap(value), catalogUnknownAccountKey) {
			continue
		}
		campaign, err := normalizeWatchCampaign(value)
		if err != nil {
			r.logger.Warn("跳过无法解析的目录活动", "campaign_id", id, "error", err)
		}
		if err != nil || campaign == nil {
			delete(payload, id)
			continue
		}
		payload[id] = campaign
	}
}
