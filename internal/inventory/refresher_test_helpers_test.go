package inventory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

type fakeGQLClient struct {
	mu          sync.Mutex
	doFunc      func(context.Context, gql.Operation) (gql.Response, error)
	doBatchFunc func(context.Context, []gql.Operation) ([]gql.Response, error)
	batchSizes  []int
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, fmt.Errorf("缺少 Do 模拟实现")
	}
	return f.doFunc(ctx, operation)
}

func (f *fakeGQLClient) DoBatch(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
	f.mu.Lock()
	f.batchSizes = append(f.batchSizes, len(operations))
	f.mu.Unlock()

	if f.doBatchFunc == nil {
		return nil, fmt.Errorf("缺少 DoBatch 模拟实现")
	}
	return f.doBatchFunc(ctx, operations)
}

type fakeAuthState struct {
	validateErr   error
	validateCalls int
	snapshot      auth.Snapshot
}

func (f *fakeAuthState) Validate(context.Context) error {
	f.validateCalls++
	return f.validateErr
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	return f.snapshot
}

type testCampaignInput struct {
	id       string
	name     string
	status   string
	linked   bool
	startsAt time.Time
	endsAt   time.Time
	game     map[string]any
	allow    map[string]any
	drops    []map[string]any
}

func testCampaignMap(input testCampaignInput) map[string]any {
	allow := input.allow
	if allow == nil {
		allow = map[string]any{
			"isEnabled": true,
			"channels":  []any{},
		}
	}

	return map[string]any{
		"id":             input.id,
		"name":           input.name,
		"status":         input.status,
		"startAt":        formatTime(input.startsAt),
		"endAt":          formatTime(input.endsAt),
		"game":           input.game,
		"self":           map[string]any{"isAccountConnected": input.linked},
		"accountLinkURL": "https://www.twitch.tv/drops/campaigns/" + input.id,
		"allow":          allow,
		"timeBasedDrops": input.drops,
	}
}

type testDropInput struct {
	id             string
	name           string
	startsAt       time.Time
	endsAt         time.Time
	required       int
	currentMinutes int
	claimID        string
	isClaimed      bool
	benefits       []map[string]any
	preconditions  []string
}

func testDropMap(input testDropInput) map[string]any {
	drop := map[string]any{
		"id":                     input.id,
		"name":                   input.name,
		"startAt":                formatTime(input.startsAt),
		"endAt":                  formatTime(input.endsAt),
		"requiredMinutesWatched": input.required,
		"benefitEdges":           edgesFromBenefits(input.benefits),
	}
	if len(input.preconditions) > 0 {
		preconditions := make([]any, 0, len(input.preconditions))
		for _, dropID := range input.preconditions {
			preconditions = append(preconditions, map[string]any{"id": dropID})
		}
		drop["preconditionDrops"] = preconditions
	}
	if input.claimID != "" || input.currentMinutes > 0 || input.isClaimed {
		drop["self"] = map[string]any{
			"dropInstanceID":        input.claimID,
			"isClaimed":             input.isClaimed,
			"currentMinutesWatched": input.currentMinutes,
		}
	}
	return drop
}

func testBenefitMap(id string, name string, distributionType string) map[string]any {
	return map[string]any{
		"benefit": map[string]any{
			"id":               id,
			"name":             name,
			"distributionType": distributionType,
			"imageAssetURL":    "https://static.example.com/" + id + ".png",
		},
	}
}

func edgesFromBenefits(benefits []map[string]any) []any {
	result := make([]any, 0, len(benefits))
	for _, benefit := range benefits {
		result = append(result, benefit)
	}
	return result
}

func testGameMap(id int64, name string, slug string, boxArtURL string) map[string]any {
	return map[string]any{
		"id":          fmt.Sprintf("%d", id),
		"name":        name,
		"displayName": name,
		"slug":        slug,
		"boxArtURL":   boxArtURL,
	}
}

func campaignIDs(campaigns []*domain.DropsCampaign) []string {
	ids := make([]string, 0, len(campaigns))
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}
		ids = append(ids, campaign.ID)
	}
	return ids
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func testNow() time.Time {
	return time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
}
