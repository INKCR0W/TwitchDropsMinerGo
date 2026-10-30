package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
)

func nativeTestCampaign(id string, linked, claimed bool) map[string]any {
	now := testNow()
	return testCampaignMap(testCampaignInput{
		id: id, name: id, status: "ACTIVE", linked: linked,
		startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour),
		game: testGameMap(511224, "Apex Legends", "apex-legends", ""),
		drops: []map[string]any{testDropMap(testDropInput{
			id: id + "-drop", name: id + "-reward", required: 15, isClaimed: claimed,
			startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour),
			benefits: []map[string]any{testBenefitMap(id+"-benefit", "Reward", "DIRECT_ENTITLEMENT")},
		})},
	})
}

func TestNativeRefreshConfirmsNewCampaignWithoutChannelQueries(t *testing.T) {
	campaigns := []any{nativeTestCampaign("new-linked", true, false), nativeTestCampaign("unlinked", false, false), nativeTestCampaign("claimed", true, true)}
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{
					"dropCampaignsInProgress": []any{}, "gameEventDrops": []any{},
				}}}}, nil
			case "ViewerDropsDashboard":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": campaigns}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("不应发起频道查询: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			responses := make([]gql.Response, len(operations))
			for index, operation := range operations {
				if operation.OperationName != "DropCampaignDetails" || operation.Variables["channelLogin"] != "77" {
					t.Fatalf("活动详情查询不匹配: %+v", operation)
				}
				for _, campaign := range campaigns {
					if stringValue(optionalMap(campaign), "id") == operation.Variables["dropID"] {
						responses[index] = gql.Response{Data: map[string]any{"user": map[string]any{"dropCampaign": campaign}}}
					}
				}
			}
			return responses, nil
		},
	}
	r, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}}, Clock: testNow})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"new-linked": true, "unlinked": false, "claimed": false} {
		campaign := snapshot.Campaigns[id]
		if campaign == nil || campaign.CanEarnWithin(testNow(), testNow().Add(time.Hour), false) != want {
			t.Fatalf("原生活动资格或完成状态不匹配: id=%s campaign=%+v", id, campaign)
		}
	}
}
