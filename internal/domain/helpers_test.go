package domain

import (
	"testing"
	"time"
)

func mustCampaign(t *testing.T, spec CampaignSpec) *DropsCampaign {
	t.Helper()

	campaign, err := NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func testTime() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
