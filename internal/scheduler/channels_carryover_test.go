package scheduler

import (
	"testing"

	"twitchdropsminergo/internal/domain"
)

func carryOverChannel(broadcastID int64, offered []string) domain.Channel {
	return domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:        broadcastID,
			Game:               &domain.Game{ID: 7, Name: "Game"},
			OfferedCampaignIDs: offered,
		},
	}
}

func TestCarryOverKnownStreamKeepsAnswerForSameBroadcast(t *testing.T) {
	t.Parallel()

	existing := carryOverChannel(100, []string{"campaign-1"})
	fromDirectory := carryOverChannel(100, nil)

	carryOverKnownStream(existing, &fromDirectory)

	if !fromDirectory.OffersCampaign("campaign-1") {
		t.Fatal("同一场直播应沿用已查到的结果, 否则目录抓取会把答案抹回未知")
	}
}

func TestCarryOverKnownStreamDropsAnswerFromOtherBroadcast(t *testing.T) {
	t.Parallel()

	existing := carryOverChannel(100, []string{})
	fromDirectory := carryOverChannel(200, nil)

	carryOverKnownStream(existing, &fromDirectory)

	if fromDirectory.Stream.OfferedCampaignIDs != nil {
		t.Fatal("换了一场直播不应沿用上一场的结论, 会把频道永久判死")
	}
	if !fromDirectory.OffersCampaign("campaign-1") {
		t.Fatal("新广播尚未查到结果时应放行")
	}
}
