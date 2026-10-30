package inventory

import (
	"context"
	"errors"
	"fmt"
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

func TestCatalogAccountResolutionChecksAllAllowedChannelsOnce(t *testing.T) {
	first, second := accountTestCampaign(), accountTestCampaign()
	channels := []any{map[string]any{"id": "1", "name": "one"}, map[string]any{"id": "2", "name": "two"}, map[string]any{"id": "3", "name": "three"}, map[string]any{"id": "4", "name": "four"}}
	for _, campaign := range []map[string]any{first, second} {
		campaign["allow"] = map[string]any{"isEnabled": true, "channels": channels}
	}
	second["id"] = "another-campaign"
	calls := 0
	r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
		calls++
		if query.Variables["gameID"] != nil {
			t.Fatalf("unexpected channel target: %+v", query.Variables)
		}
		return gql.Response{}, nil
	}})
	if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": first, "another-campaign": second}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
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

func TestCatalogAccountResolutionFindsCampaignBeyondFirstChannelBatch(t *testing.T) {
	for _, target := range []string{"4", "11"} {
		t.Run(target, func(t *testing.T) {
			campaign := accountTestCampaign()
			channels := []any{}
			for index := 1; index <= 25; index++ {
				id := fmt.Sprint(index)
				channels = append(channels, map[string]any{"id": id, "name": "channel" + id})
			}
			campaign["allow"] = map[string]any{"isEnabled": true, "channels": channels}
			calls := 0
			seen := make(map[string]bool)
			r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, q gql.RawQuery) (gql.Response, error) {
				calls++
				data := map[string]any{}
				for key, value := range q.Variables {
					id := value.(string)
					if seen[id] {
						t.Fatalf("channel queried twice: %s", id)
					}
					seen[id] = true
					observed := []any{}
					if id == target {
						observed = append(observed, accountTestObservation(true))
					}
					data[key] = map[string]any{"viewerDropCampaigns": observed}
				}
				return gql.Response{Data: data}, nil
			}})
			if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": campaign}, RefreshOptions{}); err != nil {
				t.Fatal(err)
			}
			expectedCalls := 1
			if target == "11" {
				expectedCalls = 2
			}
			if calls != expectedCalls || !boolValue(optionalMap(campaign["self"]), "isAccountConnected") {
				t.Fatalf("late channel not resolved: calls=%d", calls)
			}
		})
	}
}

func accountTestPage(cursor string, hasNext bool, observations ...any) gql.Response {
	response := accountTestResponse(observations...)
	streams := optionalMap(optionalMap(optionalMap(response.Data)["game"])["streams"])
	streams["pageInfo"] = map[string]any{"hasNextPage": hasNext}
	optionalMap(streams["edges"].([]any)[0])["cursor"] = cursor
	return response
}

func TestCatalogAccountResolutionFindsCampaignOnLaterGamePage(t *testing.T) {
	campaign := accountTestCampaign()
	calls := 0
	r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, q gql.RawQuery) (gql.Response, error) {
		calls++
		if calls == 1 {
			if q.Variables["after"] != nil {
				t.Fatal("first page has a cursor")
			}
			return accountTestPage("page-one", true), nil
		}
		if q.Variables["after"] != "page-one" {
			t.Fatalf("cursor not advanced: %+v", q.Variables)
		}
		return accountTestPage("page-two", true, accountTestObservation(true)), nil
	}})
	if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"prime-time": campaign}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !boolValue(optionalMap(campaign["self"]), "isAccountConnected") {
		t.Fatalf("later page not resolved: calls=%d", calls)
	}
}

func TestCatalogAccountPaginationStopsAtExhaustionOrInvalidCursor(t *testing.T) {
	for _, test := range []struct {
		name    string
		next    bool
		cursor  string
		calls   int
		wantErr bool
	}{
		{"exhausted", false, "", 1, false},
		{"missing cursor", true, "", 1, true},
		{"repeated cursor", true, "same", 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			r := accountTestRefresher(&fakeGQLClient{rawFunc: func(context.Context, gql.RawQuery) (gql.Response, error) {
				calls++
				return accountTestPage(test.cursor, test.next), nil
			}})
			pending := map[string]map[string]any{"prime-time": accountTestCampaign()}
			client := r.gqlClient.(rawGQLClient)
			err := r.resolveCatalogGameAccounts(context.Background(), client, "1910103699", pending)
			if (err != nil) != test.wantErr || calls != test.calls || len(pending) != 1 {
				t.Fatalf("calls=%d pending=%d err=%v", calls, len(pending), err)
			}
		})
	}
}

func TestCatalogAccountResolutionChecksPriorityCampaignFirst(t *testing.T) {
	first, second := accountTestCampaign(), accountTestCampaign()
	first["id"] = "a-other"
	first["game"] = map[string]any{"id": "1", "displayName": "Other"}
	first["allow"] = map[string]any{"isEnabled": true, "channels": []any{map[string]any{"id": "1", "name": "other"}}}
	second["id"] = "z-priority"
	second["allow"] = map[string]any{"isEnabled": true, "channels": []any{map[string]any{"id": "2", "name": "priority"}}}
	calls := []string{}
	r := accountTestRefresher(&fakeGQLClient{rawFunc: func(_ context.Context, q gql.RawQuery) (gql.Response, error) {
		calls = append(calls, q.Variables["channelID"].(string))
		return gql.Response{}, nil
	}})
	if err := r.resolveCatalogAccounts(context.Background(), map[string]any{"a-other": first, "z-priority": second}, RefreshOptions{PriorityGames: []string{"THE FINALS"}}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "2" {
		t.Fatalf("priority lookup order=%v", calls)
	}
}
func TestCatalogAccountPaginationPreservesUnknownAfterLaterPageFailure(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		r := accountTestRefresher(&fakeGQLClient{rawFunc: func(context.Context, gql.RawQuery) (gql.Response, error) {
			calls++
			if calls == 1 {
				return accountTestPage("first", true), nil
			}
			if cancelRequest {
				cancel()
				return gql.Response{}, context.Canceled
			}
			return gql.Response{}, errors.New("next page unavailable")
		}})
		pending := map[string]map[string]any{"prime-time": accountTestCampaign()}
		err := r.resolveCatalogGameAccounts(ctx, r.gqlClient.(rawGQLClient), "1910103699", pending)
		cancel()
		if err == nil || calls != 2 || len(pending) != 1 {
			t.Fatalf("failed page changed eligibility: err=%v calls=%d pending=%d", err, calls, len(pending))
		}
	}
}
