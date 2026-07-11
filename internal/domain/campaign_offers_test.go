package domain

import (
	"testing"
	"time"
)

func offersTestCampaign(t *testing.T, campaignID string, game Game, isReward bool, allowed []Channel) *DropsCampaign {
	t.Helper()

	now := testTime()
	return mustCampaign(t, CampaignSpec{
		ID:               campaignID,
		Name:             campaignID,
		Game:             game,
		Linked:           true,
		Status:           "ACTIVE",
		IsRewardCampaign: isReward,
		AllowedChannels:  allowed,
		StartsAt:         now.Add(-time.Hour),
		EndsAt:           now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              campaignID + "-drop",
				Name:            campaignID + "-drop",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 15,
				Benefits:        []Benefit{{ID: "benefit", Name: "Badge", Type: BenefitTypeBadge}},
			},
		},
	})
}

func offersTestChannel(offered []string) *Channel {
	return &Channel{
		ID:    620,
		Login: "acl-channel",
		Stream: &Stream{
			BroadcastID:        700,
			Game:               &Game{ID: IRLGameID, Name: "IRL"},
			DropsEnabled:       true,
			OfferedCampaignIDs: offered,
		},
	}
}

func TestSpecialCampaignNeedsTwitchToOfferItOnTheChannel(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel([]string{})
	campaign := offersTestCampaign(t, "football-fest", Game{ID: SpecialEventsGameID, Name: "Special Events"}, false,
		[]Channel{{ID: channel.ID, Login: channel.Login}})

	if campaign.CanEarn(now, channel, true, false) {
		t.Fatal("频道在 ACL 名单内但 Twitch 未报告可推进时, 不应判定为可推进")
	}
}

func TestSpecialCampaignEarnableWhenTwitchOffersIt(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel([]string{"ewc-2026"})
	campaign := offersTestCampaign(t, "ewc-2026", Game{ID: SpecialEventsGameID, Name: "Special Events"}, false,
		[]Channel{{ID: channel.ID, Login: channel.Login}})

	if !campaign.CanEarn(now, channel, true, false) {
		t.Fatal("Twitch 报告该频道可推进该活动时应判定为可推进")
	}
}

func TestSpecialCampaignFallsOpenBeforeOffersAreKnown(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel(nil)
	campaign := offersTestCampaign(t, "football-fest", Game{ID: SpecialEventsGameID, Name: "Special Events"}, false,
		[]Channel{{ID: channel.ID, Login: channel.Login}})

	if !campaign.CanEarn(now, channel, true, false) {
		t.Fatal("尚未查到 AvailableDrops 时不应把频道判死")
	}
}

func TestLocalTimedRewardCampaignIgnoresOffers(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel([]string{})
	campaign := offersTestCampaign(t, "reward:builder-cape", Game{ID: SpecialEventsGameID, Name: "Special Events"}, true, nil)

	if !campaign.CanEarn(now, channel, true, false) {
		t.Fatal("只靠本地计时的 reward 活动不会出现在 viewerDropCampaigns 里, 不应被门控挡掉")
	}
}

func TestSpecialCampaignStillGatedOnChannelInSameSpecialCategory(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel([]string{})
	channel.Stream.Game = &Game{ID: SpecialEventsGameID, Name: "Special Events"}
	campaign := offersTestCampaign(t, "football-fest", Game{ID: SpecialEventsGameID, Name: "Special Events"}, false,
		[]Channel{{ID: channel.ID, Login: channel.Login}})

	if campaign.CanEarn(now, channel, true, false) {
		t.Fatal("频道分类恰好等于活动分类时也不应绕过 viewerDropCampaigns 校验")
	}
}

func TestGameCampaignIgnoresOffers(t *testing.T) {
	t.Parallel()

	now := testTime()
	channel := offersTestChannel([]string{})
	channel.Stream.Game = &Game{ID: 77, Name: "Game"}
	campaign := offersTestCampaign(t, "game-campaign", Game{ID: 77, Name: "Game"}, false, nil)

	if !campaign.CanEarn(now, channel, true, false) {
		t.Fatal("游戏匹配的普通活动不受 viewerDropCampaigns 门控影响")
	}
}
