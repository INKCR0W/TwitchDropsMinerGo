package inventory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
)

func TestChannelDiscoveryPreservesInventoryAndDiscoversUnwatchedDrops(t *testing.T) {
	now := testNow()
	game := testGameMap(1, "A Game", "a-game", "")
	knownDrop := testDropMap(testDropInput{id: "old-drop", name: "Old", startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour), required: 60, currentMinutes: 17, claimID: "real-claim"})
	known := testCampaignMap(testCampaignInput{id: "known", name: "Known", linked: false, startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour), game: game, drops: []map[string]any{knownDrop}})
	known["timeBasedDrops"] = []any{knownDrop}
	known["allow"] = map[string]any{"isEnabled": false, "channels": []any{}}
	newDrop := testDropMap(testDropInput{id: "new-drop", name: "New", startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour), required: 45, benefits: []map[string]any{testBenefitMap("item", "Item", "DIRECT_ENTITLEMENT")}})
	offer := map[string]any{"id": "known", "name": "Known", "endAt": formatTime(now.Add(time.Hour)), "game": game, "timeBasedDrops": []any{newDrop}}
	unwatched := cloneMap(offer)
	unwatched["id"] = "unwatched"
	client := &fakeGQLClient{doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
		switch operation.OperationName {
		case "Inventory":
			return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{"dropCampaignsInProgress": []any{known}}}}}, nil
		case "ViewerDropsDashboard":
			if operation.Variables["fetchRewardCampaigns"] != true {
				t.Fatal("TV discovery must not use the empty drops dashboard")
			}
			return gql.Response{Data: map[string]any{}}, nil
		case "DirectoryPage_Game":
			filters := operation.Variables["options"].(map[string]any)["systemFilters"].([]any)
			if len(filters) != 1 || filters[0] != "DROPS_ENABLED" {
				t.Fatalf("unexpected filters: %v", filters)
			}
			return discoveryDirectoryResponse(game), nil
		case "DropsHighlightService_AvailableDrops":
			return gql.Response{Data: map[string]any{"channel": map[string]any{"viewerDropCampaigns": []any{offer, unwatched}}}}, nil
		default:
			return gql.Response{}, fmt.Errorf("unexpected operation %s", operation.OperationName)
		}
	}}
	client.doBatchFunc = func(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
		responses := make([]gql.Response, 0, len(operations))
		for _, operation := range operations {
			response, err := client.Do(ctx, operation)
			if err != nil {
				return nil, err
			}
			responses = append(responses, response)
		}
		return responses, nil
	}
	refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 1}}, DiscoveryMode: DiscoveryChannels, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{DiscoveryGames: []string{"A Game"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Campaigns) != 2 || len(snapshot.Campaigns["known"].TimedDrops) != 2 {
			t.Fatalf("missing merged campaigns/drops: %+v", snapshot)
		}
		campaign := snapshot.Campaigns["known"]
		if campaign.Linked || len(campaign.AllowedChannels) != 0 {
			t.Fatal("discovery replaced authoritative account/allow state")
		}
		if drop := campaign.TimedDrops["old-drop"]; drop.RealCurrentMinutes != 17 || drop.ClaimID != "real-claim" {
			t.Fatalf("lost inventory progress: %+v", drop)
		}
		fresh := snapshot.Campaigns["unwatched"]
		if !fresh.Linked || len(fresh.AllowedChannels) != 1 || !fresh.StartsAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("incorrect channel campaign normalization: %+v", fresh)
		}
		if fresh.TimedDrops["new-drop"].ClaimID != "" {
			t.Fatal("discovery fabricated a claim ID")
		}
	}
}

func discoveryDirectoryResponse(game map[string]any) gql.Response {
	return gql.Response{Data: map[string]any{"game": map[string]any{"streams": map[string]any{"edges": []any{
		map[string]any{"node": map[string]any{"game": game, "broadcaster": map[string]any{"id": "123", "login": "streamer"}}},
	}}}}}
}

func TestNormalizeChannelCampaignRetainsSpecialGameAndSkipsSubscriptionDrops(t *testing.T) {
	now := testNow()
	offer := map[string]any{
		"id": "special", "name": "Special", "endAt": formatTime(now.Add(time.Hour)),
		"game": map[string]any{"id": "509663", "name": "Special Events"},
		"timeBasedDrops": []any{
			map[string]any{"id": "subscription", "requiredSubs": 1, "startAt": formatTime(now.Add(-2 * time.Hour))},
			map[string]any{"id": "watch", "requiredMinutesWatched": 30, "startAt": formatTime(now.Add(-time.Hour)), "endAt": formatTime(now.Add(time.Hour)), "benefitEdges": []any{testBenefitMap("item", "Item", "DIRECT_ENTITLEMENT")}},
		},
	}
	got, err := normalizeChannelCampaign(offer, discoveryChannel{id: "1", login: "streamer", game: map[string]any{"id": "2", "displayName": "Other Game", "slug": "other-game"}})
	if err != nil {
		t.Fatal(err)
	}
	if game := parseGame(optionalMap(got["game"])); game.ID != 509663 || game.Name != "Special Events" || game.Slug() != "special-events" {
		t.Fatalf("campaign game was replaced with streamed game: %+v", game)
	}
	if len(got["timeBasedDrops"].([]any)) != 1 || !timeValue(got, "startAt").Equal(now.Add(-time.Hour)) {
		t.Fatalf("subscription drops or invalid campaign dates: %+v", got)
	}
	offer["timeBasedDrops"] = []any{map[string]any{"id": "subscription", "requiredSubs": 1}}
	if campaign, err := normalizeChannelCampaign(offer, discoveryChannel{id: "1", login: "streamer"}); err != nil || campaign != nil {
		t.Fatalf("subscription-only campaign must be omitted: %v, %v", campaign, err)
	}
}

type discoveryRawClient struct {
	*fakeGQLClient
	raw func(context.Context, gql.RawQuery) (gql.Response, error)
}

func (c *discoveryRawClient) DoRaw(ctx context.Context, query gql.RawQuery) (gql.Response, error) {
	return c.raw(ctx, query)
}

func TestGlobalDiscoveryRotatesPastUnavailableGamesAndExpiresCache(t *testing.T) {
	now := testNow()
	fail := false
	var cursors []any
	var slugs []string
	client := &discoveryRawClient{
		fakeGQLClient: &fakeGQLClient{doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			slugs = append(slugs, operation.Variables["slug"].(string))
			if fail {
				return gql.Response{}, errors.New("directory unavailable")
			}
			return gql.Response{Data: map[string]any{"game": nil}}, nil
		}},
		raw: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
			cursors = append(cursors, query.Variables["after"])
			end, hasNext := "cursor-1", true
			if query.Variables["after"] == "cursor-1" {
				end, hasNext = "cursor-2", false
			}
			return gql.Response{Data: map[string]any{"games": map[string]any{
				"edges":    []any{map[string]any{"cursor": end, "node": map[string]any{"id": "1", "name": "Game II", "slug": "game-2"}}},
				"pageInfo": map[string]any{"hasNextPage": hasNext},
			}}}, nil
		},
	}
	refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{}, DiscoveryMode: DiscoveryChannels, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	refresher.discoveredCampaigns = map[string]any{
		"alive": map[string]any{"id": "alive", "endAt": formatTime(now.Add(time.Hour))},
		"dead":  map[string]any{"id": "dead", "endAt": formatTime(now.Add(-time.Hour))},
	}
	options := RefreshOptions{GlobalDiscovery: true}
	got, err := refresher.fetchChannelCampaigns(context.Background(), nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["alive"] == nil || refresher.discoveryCursor != "cursor-1" || slugs[0] != "game-2" {
		t.Fatalf("incorrect cache/page state: %v, %q, %v", got, refresher.discoveryCursor, slugs)
	}
	fail = true
	if _, err := refresher.fetchChannelCampaigns(context.Background(), nil, options); err != nil {
		t.Fatalf("a failed game must not block inventory: %v", err)
	}
	if refresher.discoveryCursor != "" {
		t.Fatal("unavailable game prevented page rotation")
	}
	fail = false
	if _, err := refresher.fetchChannelCampaigns(context.Background(), nil, options); err != nil {
		t.Fatal(err)
	}
	if refresher.discoveryCursor != "cursor-1" || cursors[0] != nil || cursors[1] != "cursor-1" || cursors[2] != nil {
		t.Fatalf("incorrect rotation: %v, %q", cursors, refresher.discoveryCursor)
	}
}
