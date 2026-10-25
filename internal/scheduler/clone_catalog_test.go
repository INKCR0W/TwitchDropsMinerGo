package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
)

func TestCatalogAccountStateSurvivesSnapshotCloneAndGameSelection(t *testing.T) {
	now := testTime()
	var campaigns []*domain.DropsCampaign
	for index, test := range []struct {
		id      string
		linked  bool
		unknown bool
	}{
		{"inventory-linked", true, false},
		{"catalog-unknown", false, true},
		{"catalog-another", false, true},
		{"inventory-unlinked", false, false},
	} {
		campaigns = append(campaigns, mustCampaign(t, domain.CampaignSpec{
			ID: test.id, Name: test.id, Game: domain.Game{ID: int64(index + 1), Name: test.id},
			Linked: test.linked, AccountConnectionUnknown: test.unknown,
			Status: "ACTIVE", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
			Drops: []domain.TimedDropSpec{{ID: test.id + "-drop", RequiredMinutes: 30,
				StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
				Benefits: []domain.Benefit{{ID: test.id + "-benefit", Type: domain.BenefitTypeDirectEntitlement}},
			}},
		}))
	}
	cloned, err := cloneInventorySnapshot(inventory.Snapshot{Inventory: campaigns})
	if err != nil {
		t.Fatal(err)
	}
	wanted := computeLegacyWantedGames(cloned.Inventory, now, now.Add(time.Hour), config.Settings{
		PriorityMode: config.EndingSoonest, Priority: []string{"inventory-linked"},
	})
	if len(wanted) != 1 || wanted[0].Name != "inventory-linked" {
		t.Fatalf("snapshot clone changed account eligibility: %+v", wanted)
	}
	wanted = computeLegacyWantedGames(cloned.Inventory, now, now.Add(time.Hour), config.Settings{
		PriorityMode: config.PriorityOnly, Priority: []string{"catalog-another"},
	})
	if len(wanted) != 0 {
		t.Fatalf("unknown catalog account entered priority-only selection: %+v", wanted)
	}
}
