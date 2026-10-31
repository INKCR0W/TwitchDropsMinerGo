package domain

import (
	"fmt"
	"testing"
	"time"
)

func TestRewardMinutesReachGoalBeyondOptimisticLimit(t *testing.T) {
	t.Parallel()
	for _, goal := range []int{0, 1, 15, 16, 20, 30} {
		for _, initial := range []int{0, 3} {
			t.Run(fmt.Sprintf("goal=%d/initial=%d", goal, initial), func(t *testing.T) {
				now := testTime()
				campaign := rewardMinuteCampaign(t, goal, initial)
				completed := false
				for minute := 0; minute < goal+1; minute++ {
					completed = campaign.BumpRewardMinutes(now, nil, false, false) || completed
				}
				drop := campaign.Drop("drop")
				if goal == 0 {
					if completed || drop.CurrentMinutes() != initial {
						t.Fatal("zero-minute goal must not advance or complete")
					}
					return
				}
				if !completed || drop.CurrentMinutes() < goal {
					t.Fatalf("minutes=%d completed=%v, want goal=%d", drop.CurrentMinutes(), completed, goal)
				}
			})
		}
	}
}

func TestRewardMinutesStillRequireEligibilityAndTimeWindow(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"expired", "future", "unlinked", "precondition", "claimed"} {
		t.Run(invalid, func(t *testing.T) {
			campaign := rewardMinuteCampaign(t, 30, 0)
			drop := campaign.Drop("drop")
			now := testTime()
			switch invalid {
			case "expired":
				now = campaign.EndsAt
			case "future":
				now = campaign.StartsAt.Add(-time.Second)
			case "unlinked":
				campaign.Linked = false
			case "precondition":
				drop.PreconditionDropIDs = []string{"missing"}
			case "claimed":
				drop.IsClaimed = true
			}
			if campaign.BumpRewardMinutes(now, nil, false, false) || drop.CurrentMinutes() != 0 {
				t.Fatalf("invalid reward advanced: %s", invalid)
			}
		})
	}
	campaign := rewardMinuteCampaign(t, 30, 0)
	campaign.IsRewardCampaign = false
	for range 30 {
		campaign.BumpMinutes(testTime(), nil, false, false)
	}
	if got := campaign.Drop("drop").CurrentMinutes(); got != MaxExtraMinutes {
		t.Fatalf("ordinary drop exceeded optimistic limit: %d", got)
	}
}

func rewardMinuteCampaign(t *testing.T, goal, initial int) *DropsCampaign {
	t.Helper()
	now := testTime()
	return mustCampaign(t, CampaignSpec{
		ID: "reward", Name: "reward", Game: Game{ID: 1, Name: "Game"},
		Linked: true, Status: "ACTIVE", IsRewardCampaign: true,
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
		Drops: []TimedDropSpec{{
			ID: "drop", Name: "drop", RequiredMinutes: goal, RealCurrentMinutes: initial,
			StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
			Benefits: []Benefit{{ID: "benefit", Name: "reward", Type: BenefitTypeDirectEntitlement}},
		}},
	})
}
