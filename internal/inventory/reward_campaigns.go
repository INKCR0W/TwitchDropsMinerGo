package inventory

import (
	"fmt"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
)

const (
	rewardCampaignIDPrefix = "reward:"
	defaultRewardImageURL  = "https://static-cdn.jtvnw.net/assets/favicon-32-e29e246c157142c94346.png"
)

var rewardCampaignKeys = []string{
	"rewardCampaignsAvailableToUser",
	"rewardCampaigns",
	"rewardCampaignsInProgress",
}

func rewardCampaignToDropCampaign(data map[string]any, now time.Time) (map[string]any, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	if isNilValue(data["game"]) {
		return nil, false, nil
	}

	campaignID := stringValue(data, "id")
	if campaignID == "" {
		return nil, false, fmt.Errorf("reward campaign id 不能为空")
	}
	campaignName := stringValue(data, "name")
	if campaignName == "" {
		return nil, false, fmt.Errorf("reward campaign %q name 不能为空", campaignID)
	}

	game, err := mapFromMap(data, "game")
	if err != nil {
		return nil, false, err
	}

	rewards, err := sliceFromMap(data, "rewards")
	if err != nil {
		return nil, false, err
	}
	reward := firstReward(rewards)
	rewardID := stringValue(reward, "id")
	if rewardID == "" {
		rewardID = campaignID
	}
	rewardName := stringValue(reward, "name")
	if rewardName == "" {
		rewardName = campaignName
	}

	startsAt := stringValue(data, "startsAt")
	endsAt := firstNonEmpty(stringValue(reward, "earnableUntil"), stringValue(data, "endsAt"))
	if startsAt == "" || endsAt == "" {
		return nil, false, fmt.Errorf("reward campaign %q 缺少开始或结束时间", campaignID)
	}
	if parseRewardTime(startsAt).IsZero() || parseRewardTime(endsAt).IsZero() {
		return nil, false, fmt.Errorf("reward campaign %q 时间格式无效", campaignID)
	}

	unlockRequirements := optionalMap(data["unlockRequirements"])
	requiredMinutes := intValue(unlockRequirements, "minuteWatchedGoal")

	return map[string]any{
		"id":               rewardCampaignIDPrefix + campaignID,
		"name":             campaignName,
		"game":             cloneMap(game),
		"self":             map[string]any{"isAccountConnected": true},
		"accountLinkURL":   firstNonEmpty(stringValue(data, "externalURL"), stringValue(data, "aboutURL")),
		"startAt":          startsAt,
		"endAt":            endsAt,
		"status":           rewardCampaignStatus(startsAt, endsAt, now),
		"isRewardCampaign": true,
		"allow": map[string]any{
			"isEnabled": false,
			"channels":  []any{},
		},
		"timeBasedDrops": []any{
			map[string]any{
				"id":                     rewardCampaignIDPrefix + rewardID,
				"name":                   rewardName,
				"startAt":                startsAt,
				"endAt":                  endsAt,
				"requiredMinutesWatched": requiredMinutes,
				"preconditionDrops":      []any{},
				"benefitEdges": []any{
					map[string]any{
						"benefit": map[string]any{
							"id":               rewardID,
							"name":             rewardName,
							"distributionType": string(domain.BenefitTypeDirectEntitlement),
							"imageAssetURL":    firstRewardImageURL(reward),
						},
					},
				},
			},
		},
	}, true, nil
}

func rewardCampaignStatus(startsAt string, endsAt string, now time.Time) string {
	start := parseRewardTime(startsAt)
	end := parseRewardTime(endsAt)
	if !end.After(now.UTC()) {
		return "EXPIRED"
	}
	if start.After(now.UTC()) {
		return "UPCOMING"
	}
	return "ACTIVE"
}

func parseRewardTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func firstReward(rewards []any) map[string]any {
	if len(rewards) == 0 {
		return nil
	}
	return optionalMap(rewards[0])
}

func firstRewardImageURL(reward map[string]any) string {
	for _, key := range []string{"thumbnailImage", "bannerImage"} {
		if imageURL := imageURLFromMap(optionalMap(reward[key])); imageURL != "" {
			return imageURL
		}
	}
	return defaultRewardImageURL
}

func imageURLFromMap(image map[string]any) string {
	for _, key := range []string{"image1xURL", "url"} {
		if url := stringValue(image, key); url != "" {
			return url
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func collectRewardCampaigns(root map[string]any) ([]map[string]any, error) {
	if len(root) == 0 {
		return nil, nil
	}

	var campaigns []map[string]any
	for _, key := range rewardCampaignKeys {
		values, err := sliceFromMap(root, key)
		if err != nil {
			return nil, err
		}
		for index, value := range values {
			if value == nil {
				continue
			}
			campaign, err := asMap(value, fmt.Sprintf("%s[%d]", key, index))
			if err != nil {
				return nil, err
			}
			campaigns = append(campaigns, campaign)
		}
	}
	return campaigns, nil
}

func isApplicableRewardStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ACTIVE", "UPCOMING":
		return true
	default:
		return false
	}
}
