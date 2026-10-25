package inventory

import "testing"

func TestCatalogSubscriptionPrerequisitesRespectInventoryClaims(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		campaign := catalogTestCampaign()
		watch := campaign["timeBasedDrops"].([]any)[0].(map[string]any)
		watch["preconditionDrops"] = []any{map[string]any{"id": "subscription"}}
		sub := cloneMap(watch)
		sub["id"], sub["requiredSubs"], sub["requiredMinutesWatched"] = "subscription", 1, 0
		sub["preconditionDrops"] = []any{}
		sub["self"] = map[string]any{"isClaimed": claimed}
		campaign["timeBasedDrops"] = []any{watch, sub}
		result, err := normalizeWatchCampaign(campaign)
		if err != nil {
			t.Fatal(err)
		}
		if !claimed && result != nil {
			t.Fatal("unclaimed subscription unlocked its dependent watch drop")
		}
		if claimed && (result == nil || len(result["timeBasedDrops"].([]any)) != 2) {
			t.Fatal("inventory-claimed subscription prerequisite was removed")
		}
	}
}

func TestCatalogKeepsBadgeEligibilitySetting(t *testing.T) {
	campaign := catalogTestCampaign()
	drop := campaign["timeBasedDrops"].([]any)[0].(map[string]any)
	drop["benefitEdges"] = []any{map[string]any{"benefit": map[string]any{
		"id": "badge", "name": "Badge", "distributionType": "BADGE",
	}}}
	campaign[catalogUnknownAccountKey] = true
	normalized, err := normalizeWatchCampaign(campaign)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := buildCampaign(normalized, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Eligible(false) || !parsed.Eligible(true) {
		t.Fatal("public badge campaign bypassed badge/emote setting")
	}
}
