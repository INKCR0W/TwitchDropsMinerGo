package inventory

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

func builderCapeRewardCampaignMap() map[string]any {
	return map[string]any{
		"__typename":  "RewardCampaign",
		"id":          "a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
		"name":        "Builder Cape",
		"startsAt":    "2026-05-30T15:30:00Z",
		"endsAt":      "2026-06-15T06:59:59.999Z",
		"status":      "UNKNOWN",
		"summary":     "Watch 5 minutes of Minecraft gameplay to get the Builder Cape reward!",
		"externalURL": "https://www.minecraft.net/redeem",
		"aboutURL":    "https://www.minecraft.net/redeem",
		"game": map[string]any{
			"__typename":  "Game",
			"id":          "27471",
			"slug":        "minecraft",
			"displayName": "Minecraft",
			"boxArtURL":   "https://static-cdn.jtvnw.net/ttv-boxart/27471_IGDB-120x160.jpg",
		},
		"unlockRequirements": map[string]any{
			"__typename":        "QuestRewardUnlockRequirements",
			"subsGoal":          0,
			"minuteWatchedGoal": 5,
		},
		"image": map[string]any{
			"__typename": "RewardCampaignImageSet",
			"image1xURL": "https://static-cdn.jtvnw.net/twitch-quests-assets/CAMPAIGN/campaign.png",
		},
		"rewards": []any{
			map[string]any{
				"__typename":     "Reward",
				"id":             "8659c1c1-5926-11f1-a66f-0a58a9feac02",
				"name":           "Builder Cape",
				"earnableUntil":  "2026-06-15T06:59:59.999Z",
				"redemptionURL":  "https://www.minecraft.net/redeem",
				"thumbnailImage": map[string]any{"image1xURL": "https://static-cdn.jtvnw.net/twitch-quests-assets/REWARD/thumb.png"},
				"bannerImage":    map[string]any{"image1xURL": "https://static-cdn.jtvnw.net/twitch-quests-assets/REWARD/banner.png"},
			},
		},
	}
}

func TestRewardCampaignToDropCampaignConvertsBuilderCape(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	converted, ok, err := rewardCampaignToDropCampaign(builderCapeRewardCampaignMap(), now)
	if err != nil {
		t.Fatalf("转换 Builder Cape 失败: %v", err)
	}
	if !ok {
		t.Fatal("完整 Builder Cape reward campaign 应被转换")
	}

	if got := stringValue(converted, "id"); got != "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b" {
		t.Fatalf("campaign id 不匹配: %q", got)
	}
	if got := stringValue(converted, "name"); got != "Builder Cape" {
		t.Fatalf("campaign name 不匹配: %q", got)
	}
	if got := stringValue(converted, "status"); got != "ACTIVE" {
		t.Fatalf("status 应按时间计算为 ACTIVE: %q", got)
	}
	if !boolValue(converted, "isRewardCampaign") {
		t.Fatal("转换后 campaign 应标记 isRewardCampaign")
	}
	if got := stringValue(converted, "accountLinkURL"); got != "https://www.minecraft.net/redeem" {
		t.Fatalf("accountLinkURL 不匹配: %q", got)
	}

	self := optionalMap(converted["self"])
	if !boolValue(self, "isAccountConnected") {
		t.Fatal("reward campaign 应视为账户已连接")
	}
	allow := optionalMap(converted["allow"])
	if boolValue(allow, "isEnabled") {
		t.Fatal("reward campaign 不应启用 ACL")
	}
	if channels, err := sliceFromMap(allow, "channels"); err != nil || len(channels) != 0 {
		t.Fatalf("reward campaign 应无固定频道: channels=%#v err=%v", channels, err)
	}

	drops, err := sliceFromMap(converted, "timeBasedDrops")
	if err != nil {
		t.Fatalf("timeBasedDrops 解析失败: %v", err)
	}
	if len(drops) != 1 {
		t.Fatalf("应生成一个伪 timeBasedDrop: %d", len(drops))
	}
	drop := optionalMap(drops[0])
	if got := stringValue(drop, "id"); got != "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02" {
		t.Fatalf("drop id 不匹配: %q", got)
	}
	if got := intValue(drop, "requiredMinutesWatched"); got != 5 {
		t.Fatalf("requiredMinutesWatched 不匹配: %d", got)
	}
	benefitEdges, err := sliceFromMap(drop, "benefitEdges")
	if err != nil {
		t.Fatalf("benefitEdges 解析失败: %v", err)
	}
	benefit := optionalMap(optionalMap(benefitEdges[0])["benefit"])
	if got := stringValue(benefit, "distributionType"); got != string(domain.BenefitTypeDirectEntitlement) {
		t.Fatalf("distributionType 不匹配: %q", got)
	}
	if got := stringValue(benefit, "imageAssetURL"); got != "https://static-cdn.jtvnw.net/twitch-quests-assets/REWARD/thumb.png" {
		t.Fatalf("图片 URL 应优先使用 thumbnailImage: %q", got)
	}
}

func TestRewardCampaignToDropCampaignSkipsMissingGame(t *testing.T) {
	t.Parallel()

	input := builderCapeRewardCampaignMap()
	input["game"] = nil

	_, ok, err := rewardCampaignToDropCampaign(input, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("缺失 game 应跳过而不是报错: %v", err)
	}
	if ok {
		t.Fatal("缺失 game 的 reward campaign 不应进入可挖 inventory")
	}
}

func TestRewardCampaignToDropCampaignUsesSafeFallbacksForMissingReward(t *testing.T) {
	t.Parallel()

	input := builderCapeRewardCampaignMap()
	input["rewards"] = []any{}

	converted, ok, err := rewardCampaignToDropCampaign(input, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("缺失 rewards 时应使用 campaign 字段兜底: %v", err)
	}
	if !ok {
		t.Fatal("缺失 rewards 但 campaign 字段完整时仍应转换")
	}

	drops, err := sliceFromMap(converted, "timeBasedDrops")
	if err != nil {
		t.Fatalf("timeBasedDrops 解析失败: %v", err)
	}
	drop := optionalMap(drops[0])
	if got := stringValue(drop, "id"); got != "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b" {
		t.Fatalf("缺失 reward id 时应使用 campaign id 兜底: %q", got)
	}
	if got := stringValue(drop, "name"); got != "Builder Cape" {
		t.Fatalf("缺失 reward name 时应使用 campaign name 兜底: %q", got)
	}
	benefitEdges, err := sliceFromMap(drop, "benefitEdges")
	if err != nil {
		t.Fatalf("benefitEdges 解析失败: %v", err)
	}
	benefit := optionalMap(optionalMap(benefitEdges[0])["benefit"])
	if got := stringValue(benefit, "imageAssetURL"); got != defaultRewardImageURL {
		t.Fatalf("缺失 reward 图片时应使用默认图片: %q", got)
	}
}

func TestRewardCampaignToDropCampaignRejectsInvalidTimes(t *testing.T) {
	t.Parallel()

	input := builderCapeRewardCampaignMap()
	input["startsAt"] = "not-a-time"

	_, ok, err := rewardCampaignToDropCampaign(input, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("无效时间应返回错误，避免后续误判为 EXPIRED")
	}
	if ok {
		t.Fatal("无效时间的 reward campaign 不应被转换")
	}
}

func TestRewardCampaignStatusFromTimes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		now    time.Time
		status string
	}{
		{
			name:   "upcoming",
			now:    time.Date(2026, 5, 30, 15, 29, 0, 0, time.UTC),
			status: "UPCOMING",
		},
		{
			name:   "active",
			now:    time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			status: "ACTIVE",
		},
		{
			name:   "expired",
			now:    time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC),
			status: "EXPIRED",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			converted, ok, err := rewardCampaignToDropCampaign(builderCapeRewardCampaignMap(), tc.now)
			if err != nil {
				t.Fatalf("转换失败: %v", err)
			}
			if !ok {
				t.Fatal("完整 Builder Cape reward campaign 应被转换")
			}
			if got := stringValue(converted, "status"); got != tc.status {
				t.Fatalf("status 不匹配: got=%q want=%q", got, tc.status)
			}
		})
	}
}
