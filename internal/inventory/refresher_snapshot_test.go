package inventory

import (
	"context"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestRefresherRefreshBuildsInventoryIndicesAndTriggers(t *testing.T) {
	t.Parallel()

	now := testNow()
	client := snapshotFixtureClient(t, now)

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 42}},
		Clock:     func() time.Time { return now },
		ChunkSize: 2,
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{
		EnableBadgesEmotes: false,
	})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}

	if got := campaignIDs(snapshot.Inventory); !slices.Equal(got, []string{"campaign-claimed", "campaign-upcoming", "campaign-active"}) {
		t.Fatalf("inventory 排序或过滤不符合预期: %#v", got)
	}
	if _, exists := snapshot.Campaigns["campaign-invalid"]; exists {
		t.Fatal("game 为空的 campaign 应被过滤")
	}
	if _, exists := snapshot.Campaigns["campaign-expired"]; exists {
		t.Fatal("EXPIRED campaign 不应进入 details/最终 inventory")
	}
	if len(snapshot.Drops) != 4 {
		t.Fatalf("drop 索引数量不匹配: %d", len(snapshot.Drops))
	}

	activeCampaign := snapshot.Campaigns["campaign-active"]
	if activeCampaign == nil {
		t.Fatal("campaign-active 未写入 campaign 索引")
	} else if !activeCampaign.Linked {
		t.Fatal("inventory 主数据应覆盖 details 中的 linked=false")
	} else if activeCampaign.ImageURL != "https://static.example.com/game-alpha.jpg" {
		t.Fatalf("boxArtURL 去尺寸失败: %q", activeCampaign.ImageURL)
	} else if len(activeCampaign.AllowedChannels) != 1 || !activeCampaign.AllowedChannels[0].ACLBased {
		t.Fatalf("ACL 频道映射不正确: %#v", activeCampaign.AllowedChannels)
	}

	activeDrop := snapshot.Drops["drop-active"]
	if activeDrop == nil {
		t.Fatal("drop-active 未写入 drop 索引")
	} else if activeDrop.RealCurrentMinutes != 12 {
		t.Fatalf("inventory 主数据的 currentMinutesWatched 应保留: %d", activeDrop.RealCurrentMinutes)
	} else if activeDrop.ClaimID != "claim-active" {
		t.Fatalf("inventory 主数据的 claim_id 应保留: %q", activeDrop.ClaimID)
	}

	claimedDrop := snapshot.Drops["drop-claimed"]
	if claimedDrop == nil || !claimedDrop.IsClaimed {
		t.Fatalf("claimed_benefits 未能推断已领取掉宝: %#v", claimedDrop)
	}
	if claimedDrop.RealCurrentMinutes != claimedDrop.RequiredMinutes {
		t.Fatalf("已领取掉宝应归一化为满进度: current=%d required=%d", claimedDrop.RealCurrentMinutes, claimedDrop.RequiredMinutes)
	}

	expectedTriggers := []time.Time{
		now.Add(30 * time.Minute),
		now.Add(45 * time.Minute),
		now.Add(2 * time.Hour),
		now.Add(3 * time.Hour),
	}
	if !slices.Equal(snapshot.MaintenanceTriggers, expectedTriggers) {
		t.Fatalf("maintenance triggers 不匹配:\n got=%v\nwant=%v", snapshot.MaintenanceTriggers, expectedTriggers)
	}
}
