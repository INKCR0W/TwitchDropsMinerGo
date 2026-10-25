package domain

import "testing"

func TestCampaignUnknownAccountDoesNotChangeDefaultUnlinkedBehavior(t *testing.T) {
	for _, test := range []struct {
		linked   bool
		unknown  bool
		eligible bool
	}{
		{false, false, false},
		{false, true, true},
		{true, false, true},
	} {
		campaign, err := NewCampaign(CampaignSpec{Linked: test.linked, AccountConnectionUnknown: test.unknown})
		if err != nil {
			t.Fatal(err)
		}
		if campaign.Linked != test.linked || campaign.Eligible(false) != test.eligible {
			t.Fatalf("linked=%v unknown=%v campaign=%+v", test.linked, test.unknown, campaign)
		}
	}
}
