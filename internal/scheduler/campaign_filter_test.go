package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
)

func TestChannelGameChangeRespectsSpecialCampaignFilters(t *testing.T) {
	for _, mode := range []string{"excluded", "excluded-priority", "priority-only", "priority-empty", "allowed", "allowed-priority"} {
		t.Run(mode, func(t *testing.T) {
			now := testTime()
			game := domain.Game{ID: 1, Name: "Wanted"}
			other := domain.Game{ID: 2, Name: "Other"}
			special := domain.Game{ID: domain.SpecialEventsGameID, Name: "Special Events"}
			settings := config.Settings{Priority: []string{game.Name}}
			switch mode {
			case "excluded":
				settings.Exclude = []string{special.Name}
			case "excluded-priority":
				settings.Exclude = []string{special.Name}
				settings.Priority = []string{special.Name}
				settings.PriorityMode = config.PriorityOnly
			case "priority-only":
				settings.PriorityMode = config.PriorityOnly
			case "priority-empty":
				settings.Priority = nil
				settings.PriorityMode = config.PriorityOnly
			case "allowed-priority":
				settings.Priority = []string{special.Name}
				settings.PriorityMode = config.PriorityOnly
			}
			s := newTestScheduler(t, testSchedulerOptions{settings: settings})
			normalCampaign := mustCampaign(t, campaignSpec(now, "normal", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
			specialCampaign := mustCampaign(t, campaignSpec(now, "special", special, now.Add(-time.Hour), now.Add(time.Hour), nil))
			specialCampaign.Drop("special-drop").RequiredMinutes = 1
			s.snapshot = snapshotFromCampaigns(normalCampaign, specialCampaign)
			s.wantedGames = []domain.Game{game}
			before := domain.Channel{ID: 9, Login: "watching", Stream: &domain.Stream{Game: &game, DropsEnabled: true, OfferedCampaignIDs: []string{"normal", "special"}}}
			after := before
			after.Stream = &domain.Stream{Game: &other, OfferedCampaignIDs: []string{"special"}}
			s.channels = map[int64]domain.Channel{9: before}
			s.watchingChannelID = 9
			s.state = StateChannelSwitch
			if mode == "excluded" || mode == "priority-only" {
				if active := s.ActiveCampaign(&before); active == nil || active.ID != "normal" {
					t.Fatal("mixed channel should select the allowed normal campaign")
				}
			}
			s.onChannelChange(before, after)
			s.handleChannelSwitch()
			active := s.ActiveCampaign(&after)
			if mode == "allowed" || mode == "allowed-priority" {
				if active == nil || active.ID != "special" || !s.canWatch(after) {
					t.Fatal("allowed special campaign should remain watchable across games")
				}
				return
			}
			if active != nil || s.canWatch(after) || s.State() != StateIdle {
				t.Fatalf("filtered special retained: active=%v watchable=%v state=%v", active, s.canWatch(after), s.State())
			}
			if s.campaignCanEarn("special", &after) {
				t.Fatal("claim continuation should respect filters")
			}
			specialCampaign.IsRewardCampaign = true
			for _, drop := range specialCampaign.Drops() {
				drop.RealCurrentMinutes = drop.RequiredMinutes
			}
			if s.pendingRewardCompletionCampaignLocked(now, &after) != nil {
				t.Fatal("pending reward completion should respect filters")
			}
			s.handleIdle()
			if s.currentWatchingChannel() != nil {
				t.Fatal("idle should stop watching filtered campaign")
			}
		})
	}
}
