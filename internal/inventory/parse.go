package inventory

import (
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"twitchdropsminergo/internal/domain"
)

var boxArtDimensionsPattern = regexp.MustCompile(`-\d+x\d+(\.(?:jpg|png|gif))$`)

func buildCampaigns(payload map[string]any, claimedBenefits map[string]time.Time, logger *slog.Logger) ([]*domain.DropsCampaign, error) {
	campaigns := make([]*domain.DropsCampaign, 0, len(payload))
	for campaignID, rawCampaign := range payload {
		campaignData, err := asMap(rawCampaign, "campaign."+campaignID)
		if err != nil {
			logger.Warn("跳过无法解析的 campaign", "campaign_id", campaignID, "error", err)
			continue
		}
		if isNilValue(campaignData["game"]) {
			continue
		}

		campaign, err := buildCampaign(campaignData, claimedBenefits)
		if err != nil {
			logger.Warn("跳过无法解析的 campaign", "campaign_id", campaignID, "error", err)
			continue
		}
		campaigns = append(campaigns, campaign)
	}

	return campaigns, nil
}

func buildCampaign(data map[string]any, claimedBenefits map[string]time.Time) (*domain.DropsCampaign, error) {
	gameData, err := mapFromMap(data, "game")
	if err != nil {
		return nil, err
	}

	selfData := optionalMap(data["self"])
	spec := domain.CampaignSpec{
		ID:               stringValue(data, "id"),
		Name:             stringValue(data, "name"),
		Game:             parseGame(gameData),
		Linked:           boolValue(selfData, "isAccountConnected"),
		LinkURL:          stringValue(data, "accountLinkURL"),
		ImageURL:         normalizeBoxArtURL(stringValue(gameData, "boxArtURL")),
		StartsAt:         timeValue(data, "startAt"),
		EndsAt:           timeValue(data, "endAt"),
		Status:           stringValue(data, "status"),
		IsRewardCampaign: boolValue(data, "isRewardCampaign"),
		AllowedChannels:  parseAllowedChannels(data["allow"]),
	}
	if spec.ID == "" {
		return nil, fmt.Errorf("campaign id 不能为空")
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("campaign %q name 不能为空", spec.ID)
	}
	if spec.StartsAt.IsZero() || spec.EndsAt.IsZero() {
		return nil, fmt.Errorf("campaign %q 缺少开始或结束时间", spec.ID)
	}

	dropValues, err := sliceFromMap(data, "timeBasedDrops")
	if err != nil {
		return nil, err
	}
	spec.Drops, err = parseDrops(dropValues, claimedBenefits)
	if err != nil {
		return nil, err
	}

	return domain.NewCampaign(spec)
}

func parseGame(data map[string]any) domain.Game {
	name := stringValue(data, "displayName")
	if name == "" {
		name = stringValue(data, "name")
	}

	return domain.Game{
		ID:       int64Value(data, "id"),
		Name:     name,
		SlugText: stringValue(data, "slug"),
	}
}

func parseAllowedChannels(value any) []domain.Channel {
	allowData := optionalMap(value)
	if len(allowData) == 0 || !allowEnabled(allowData) {
		return nil
	}

	channelsValue, ok := allowData["channels"]
	if !ok || channelsValue == nil {
		return nil
	}

	channelList, ok := channelsValue.([]any)
	if !ok {
		return nil
	}

	channels := make([]domain.Channel, 0, len(channelList))
	for _, item := range channelList {
		channelData := optionalMap(item)
		if len(channelData) == 0 {
			continue
		}

		channelID := int64Value(channelData, "id")
		login := stringValue(channelData, "name")
		if channelID == 0 || login == "" {
			continue
		}

		channels = append(channels, domain.Channel{
			ID:          channelID,
			Login:       login,
			DisplayName: stringValue(channelData, "displayName"),
			ACLBased:    true,
		})
	}

	return channels
}

func allowEnabled(data map[string]any) bool {
	if len(data) == 0 {
		return false
	}
	value, ok := data["isEnabled"]
	if !ok {
		return true
	}

	enabled, ok := value.(bool)
	if !ok {
		return true
	}
	return enabled
}

func parseDrops(values []any, claimedBenefits map[string]time.Time) ([]domain.TimedDropSpec, error) {
	drops := make([]domain.TimedDropSpec, 0, len(values))
	for index, item := range values {
		dropData, err := asMap(item, fmt.Sprintf("timeBasedDrops[%d]", index))
		if err != nil {
			return nil, err
		}

		drop, err := parseDrop(dropData, claimedBenefits)
		if err != nil {
			return nil, err
		}
		drops = append(drops, drop)
	}

	return drops, nil
}

func parseDrop(data map[string]any, claimedBenefits map[string]time.Time) (domain.TimedDropSpec, error) {
	startsAt := timeValue(data, "startAt")
	endsAt := timeValue(data, "endAt")
	benefits, err := parseBenefits(data["benefitEdges"])
	if err != nil {
		return domain.TimedDropSpec{}, err
	}

	selfData := optionalMap(data["self"])
	isClaimed := boolValue(selfData, "isClaimed")
	if len(selfData) == 0 {
		isClaimed = inferClaimedByBenefits(benefits, claimedBenefits, startsAt, endsAt)
	}

	requiredMinutes, hasRequiredMinutes := int64ValuePresent(data, "requiredMinutesWatched")
	spec := domain.TimedDropSpec{
		ID:                  stringValue(data, "id"),
		Name:                stringValue(data, "name"),
		Benefits:            benefits,
		StartsAt:            startsAt,
		EndsAt:              endsAt,
		ClaimID:             stringValue(selfData, "dropInstanceID"),
		IsClaimed:           isClaimed,
		PreconditionDropIDs: parsePreconditionDropIDs(data["preconditionDrops"]),
		RealCurrentMinutes:  intValue(selfData, "currentMinutesWatched"),
		RequiredMinutes:     int(requiredMinutes),
	}
	if spec.ID == "" {
		return domain.TimedDropSpec{}, fmt.Errorf("drop id 不能为空")
	}
	if spec.StartsAt.IsZero() || spec.EndsAt.IsZero() {
		return domain.TimedDropSpec{}, fmt.Errorf("drop %q 缺少开始或结束时间", spec.ID)
	}
	if !hasRequiredMinutes {
		return domain.TimedDropSpec{}, fmt.Errorf("drop %q 缺少 requiredMinutesWatched 字段", spec.ID)
	}

	return spec, nil
}

func parseBenefits(value any) ([]domain.Benefit, error) {
	if value == nil {
		return nil, nil
	}

	edges, err := asSlice(value, "benefitEdges")
	if err != nil {
		return nil, err
	}

	benefits := make([]domain.Benefit, 0, len(edges))
	for index, edge := range edges {
		edgeData, err := asMap(edge, fmt.Sprintf("benefitEdges[%d]", index))
		if err != nil {
			return nil, err
		}
		benefitData, err := mapFromMap(edgeData, "benefit")
		if err != nil {
			return nil, err
		}

		benefits = append(benefits, domain.Benefit{
			ID:       stringValue(benefitData, "id"),
			Name:     stringValue(benefitData, "name"),
			Type:     domain.ParseBenefitType(stringValue(benefitData, "distributionType")),
			ImageURL: stringValue(benefitData, "imageAssetURL"),
		})
	}

	return benefits, nil
}

func inferClaimedByBenefits(benefits []domain.Benefit, claimedBenefits map[string]time.Time, startsAt time.Time, endsAt time.Time) bool {
	for _, benefit := range benefits {
		awardedAt, ok := claimedBenefits[benefit.ID]
		if !ok {
			continue
		}
		if !awardedAt.Before(startsAt) && awardedAt.Before(endsAt) {
			return true
		}
	}

	return false
}

func parsePreconditionDropIDs(value any) []string {
	dropList, ok := value.([]any)
	if !ok {
		return nil
	}

	result := make([]string, 0, len(dropList))
	for _, item := range dropList {
		dropData := optionalMap(item)
		if dropID := stringValue(dropData, "id"); dropID != "" {
			result = append(result, dropID)
		}
	}

	return result
}

func parseClaimedBenefits(values []any) (map[string]time.Time, error) {
	claimedBenefits := make(map[string]time.Time, len(values))
	for index, item := range values {
		benefitData, err := asMap(item, fmt.Sprintf("gameEventDrops[%d]", index))
		if err != nil {
			return nil, err
		}

		benefitID := stringValue(benefitData, "id")
		if benefitID == "" {
			return nil, fmt.Errorf("gameEventDrops[%d].id 不能为空", index)
		}

		awardedAt := timeValue(benefitData, "lastAwardedAt")
		if awardedAt.IsZero() {
			return nil, fmt.Errorf("gameEventDrops[%d].lastAwardedAt 不能为空", index)
		}

		claimedBenefits[benefitID] = awardedAt
	}

	return claimedBenefits, nil
}

func normalizeBoxArtURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	return boxArtDimensionsPattern.ReplaceAllString(rawURL, "$1")
}
