package inventory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"twitchdropsminergo/internal/gql"
)

func accountTestCampaign() map[string]any {
	campaign := stripCatalogSelf(catalogTestCampaign()).(map[string]any)
	campaign[catalogUnknownAccountKey] = true
	return campaign
}

func accountTestObservation(linked bool) map[string]any {
	return map[string]any{
		"id": "prime-time", "self": map[string]any{"isAccountConnected": linked},
		"timeBasedDrops": []any{map[string]any{"id": "bebe-sticker", "self": map[string]any{
			"isClaimed": true, "currentMinutesWatched": 60, "dropInstanceID": "account-claim",
		}}},
	}
}

func accountTestResponse(observed ...any) gql.Response {
	return gql.Response{Data: map[string]any{"game": map[string]any{"streams": map[string]any{
		"edges": []any{map[string]any{"node": map[string]any{"channel": map[string]any{
			"viewerDropCampaigns": observed,
		}}}}, "pageInfo": map[string]any{"hasNextPage": true},
	}}}}
}

func accountTestRefresher(client *fakeGQLClient) *Refresher {
	return &Refresher{gqlClient: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestCatalogAccountResolutionUsesOnlyAuthenticatedState(t *testing.T) {
	for _, test := range []struct {
		name     string
		observed any
		err      error
		known    bool
		linked   bool
	}{
		{name: "linked", observed: accountTestObservation(true), known: true, linked: true},
		{name: "unlinked", observed: accountTestObservation(false), known: true},
		{name: "not offered"},
		{name: "missing self", observed: map[string]any{"id": "prime-time"}},
		{name: "missing drop progress", observed: map[string]any{"id": "prime-time", "self": map[string]any{"isAccountConnected": true}}},
		{name: "request failure", err: errors.New("network unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			campaign := accountTestCampaign()
			calls := 0
			r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
				calls++
				if query.Variables["gameID"] != "1910103699" {
					t.Fatalf("unexpected target: %+v", query.Variables)
				}
				return accountTestResponse(test.observed), test.err
			}})
			if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": campaign}, RefreshOptions{}); err != nil {
				t.Fatal(err)
			}
			result, err := buildCampaign(campaign, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.AccountConnectionUnknown == test.known || result.Linked != test.linked || result.Eligible(false) != test.linked {
				t.Fatalf("unexpected account eligibility: linked=%v unknown=%v", result.Linked, result.AccountConnectionUnknown)
			}
			if calls != 1 {
				t.Fatalf("account check traversed additional pages: %d", calls)
			}
			if test.known && !result.TimedDrops["bebe-sticker"].IsClaimed {
				t.Fatal("authenticated completion was not merged")
			}
		})
	}
}

func TestCatalogAccountResolutionFiltersBeforeRequests(t *testing.T) {
	for _, options := range []RefreshOptions{
		{ExcludedGames: []string{" the finals "}},
		{PriorityOnly: true},
		{PriorityOnly: true, PriorityGames: []string{"Rust"}},
	} {
		r := accountTestRefresher(&fakeGQLClient{rawFunc: func(context.Context, gql.RawQuery) (gql.Response, error) {
			t.Fatal("filtered game triggered account query")
			return gql.Response{}, nil
		}})
		if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": accountTestCampaign()}, options); err != nil {
			t.Fatal(err)
		}
	}
	campaign := accountTestCampaign()
	drop := optionalMap(campaign["timeBasedDrops"].([]any)[0])
	drop["benefitEdges"] = []any{map[string]any{"benefit": map[string]any{"distributionType": "BADGE"}}}
	if catalogNeedsAccount(campaign, RefreshOptions{}) || !catalogNeedsAccount(campaign, RefreshOptions{EnableBadgesEmotes: true}) {
		t.Fatal("account lookup ignored badge setting")
	}
}

func TestCatalogAccountResolutionPreservesInventoryState(t *testing.T) {
	for _, linked := range []bool{false, true} {
		campaign := accountTestCampaign()
		campaign["self"] = map[string]any{"isAccountConnected": linked}
		r := accountTestRefresher(&fakeGQLClient{rawFunc: func(context.Context, gql.RawQuery) (gql.Response, error) {
			t.Fatal("known account state triggered query")
			return gql.Response{}, nil
		}})
		if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": campaign}, RefreshOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	campaign := accountTestCampaign()
	drop := optionalMap(campaign["timeBasedDrops"].([]any)[0])
	drop["self"] = map[string]any{"isClaimed": false, "currentMinutesWatched": 17}
	pending := map[string]map[string]any{"prime-time": campaign}
	applyCatalogAccounts(pending, []any{accountTestObservation(true)})
	if len(pending) != 0 || boolValue(optionalMap(drop["self"]), "isClaimed") || int64Value(optionalMap(drop["self"]), "currentMinutesWatched") != 17 {
		t.Fatal("account lookup overwrote inventory progress")
	}
}

func TestCatalogAccountResolutionBoundsAndDeduplicatesAllowedChannels(t *testing.T) {
	first, second := accountTestCampaign(), accountTestCampaign()
	channels := []any{map[string]any{"id": "1", "name": "one"}, map[string]any{"id": "2", "name": "two"}, map[string]any{"id": "3", "name": "three"}, map[string]any{"id": "4", "name": "four"}}
	for _, campaign := range []map[string]any{first, second} {
		campaign["allow"] = map[string]any{"isEnabled": true, "channels": channels}
	}
	second["id"] = "another-campaign"
	calls := 0
	r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
		calls++
		if query.Variables["channelID"] == "4" || query.Variables["gameID"] != nil {
			t.Fatalf("unexpected channel target: %+v", query.Variables)
		}
		return gql.Response{}, nil
	}})
	if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": first, "another-campaign": second}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("channel request count = %d", calls)
	}
}

func TestCatalogAccountResolutionPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := accountTestRefresher(&fakeGQLClient{})
	if err := r.resolveCatalogAccounts(ctx, map[string]any{"prime-time": accountTestCampaign()}, RefreshOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed: %v", err)
	}
}
