package inventory

import (
	"context"
	"errors"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestDiscoveryUsesCanonicalGameSlugAndCachesRedirect(t *testing.T) {
	requests := 0
	client := &fakeGQLClient{doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
		requests++
		if operation.OperationName != "DirectoryGameRedirect" || operation.Variables["name"] != "Game II" {
			t.Fatalf("unexpected redirect: %+v", operation)
		}
		return gql.Response{Data: map[string]any{"game": map[string]any{"id": "1", "slug": "game-2"}}}, nil
	}}
	refresher := &Refresher{gqlClient: client}
	for range 2 {
		game, err := refresher.resolveDiscoveryGame(context.Background(), domain.Game{Name: "Game II"})
		if err != nil || game.Slug() != "game-2" || game.Name != "Game II" {
			t.Fatalf("incorrect redirect result: %+v, %v", game, err)
		}
	}
	if requests != 1 {
		t.Fatalf("redirect was not cached: %d", requests)
	}
	games := discoveryGames(nil, RefreshOptions{DiscoveryGames: []string{"Game II"}}, []domain.Game{{Name: "Game II", SlugText: "game-2"}})
	if len(games) != 1 || games[0].SlugText != "game-2" {
		t.Fatalf("priority ignored known canonical slug: %+v", games)
	}
}

func TestGlobalDirectoryFailurePreservesInventoryAndConfiguredDiscovery(t *testing.T) {
	var directoryCalls int
	client := &discoveryRawClient{
		fakeGQLClient: &fakeGQLClient{doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			directoryCalls++
			if operation.OperationName != "DirectoryPage_Game" || operation.Variables["slug"] != "game-2" {
				t.Fatalf("unexpected operation: %+v", operation)
			}
			return gql.Response{}, errors.New("temporary directory failure")
		}},
		raw: func(context.Context, gql.RawQuery) (gql.Response, error) {
			return gql.Response{}, errors.New("temporary global directory failure")
		},
	}
	refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{}, DiscoveryMode: DiscoveryChannels, Clock: func() time.Time { return testNow() }})
	if err != nil {
		t.Fatal(err)
	}
	refresher.discoveryCursor = "retry-this-page"
	inventory := map[string]any{"known": map[string]any{"id": "known", "game": map[string]any{"id": "1", "name": "Game II", "slug": "game-2"}}}
	payload, err := refresher.fetchChannelCampaigns(context.Background(), inventory, RefreshOptions{GlobalDiscovery: true, DiscoveryGames: []string{"Game II"}})
	if err != nil || payload["known"] == nil || directoryCalls != 1 || refresher.discoveryCursor != "retry-this-page" {
		t.Fatalf("partial discovery discarded inventory: %v, %v, calls=%d, cursor=%q", payload, err, directoryCalls, refresher.discoveryCursor)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := refresher.fetchChannelCampaigns(ctx, inventory, RefreshOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed: %v", err)
	}
}

func TestChannelOfferBatchKeepsValidResponses(t *testing.T) {
	batchCalls := 0
	now := testNow()
	client := &fakeGQLClient{doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
		batchCalls++
		if len(operations) != 2 || operations[0].Variables["channelID"] != "1" || operations[1].Variables["channelID"] != "2" {
			t.Fatalf("unexpected batch: %+v", operations)
		}
		return []gql.Response{
			{Data: map[string]any{}},
			{Data: map[string]any{"channel": map[string]any{"viewerDropCampaigns": []any{
				map[string]any{"id": "campaign", "name": "Campaign", "endAt": formatTime(now.Add(time.Hour)), "timeBasedDrops": []any{
					map[string]any{"id": "drop", "name": "Drop", "startAt": formatTime(now), "endAt": formatTime(now.Add(time.Hour)), "requiredMinutesWatched": 30, "benefitEdges": []any{testBenefitMap("item", "Item", "DIRECT_ENTITLEMENT")}},
				}},
			}}}},
		}, nil
	}}
	refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := refresher.fetchChannelOffers(context.Background(), []discoveryChannel{{id: "1", login: "bad"}, {id: "2", login: "good", game: map[string]any{"id": "1", "name": "Game"}}})
	if err != nil || len(got) != 1 || stringValue(got[0], "id") != "campaign" || batchCalls != 1 {
		t.Fatalf("batch failed to isolate malformed response: %v, %v", got, err)
	}
}
