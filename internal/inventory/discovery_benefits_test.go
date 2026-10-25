package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/gql"
)

func discoveryOffer(id, dropID, kind string) map[string]any {
	now := testNow()
	return map[string]any{
		"id": id, "name": id, "endAt": formatTime(now.Add(time.Hour)),
		"game": map[string]any{"id": "1", "name": "Game", "slug": "game"},
		"timeBasedDrops": []any{map[string]any{
			"id": dropID, "name": dropID, "startAt": formatTime(now.Add(-time.Hour)), "endAt": formatTime(now.Add(time.Hour)),
			"requiredMinutesWatched": 30, "benefitEdges": []any{testBenefitMap(dropID+"-benefit", "Reward", kind)},
		}},
	}
}

func TestDiscoveryCompletesBenefitTypesAndKeepsBadgePreference(t *testing.T) {
	for _, test := range []struct {
		name string
		kind string
		err  error
	}{
		{name: "badge", kind: "BADGE"},
		{name: "new server enum", kind: "FUTURE_REWARD"},
		{name: "missing type"},
		{name: "request failed", err: errors.New("temporary failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			rawCalls := 0
			client := &discoveryRawClient{
				fakeGQLClient: &fakeGQLClient{doBatchFunc: func(context.Context, []gql.Operation) ([]gql.Response, error) {
					return []gql.Response{{Data: map[string]any{"channel": map[string]any{"viewerDropCampaigns": []any{
						discoveryOffer("unknown", "unknown-drop", ""),
						discoveryOffer("known", "known-drop", "DIRECT_ENTITLEMENT"),
					}}}}}, nil
				}},
				raw: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
					rawCalls++
					if query.Variables["channel0"] != "123" || !strings.Contains(query.Query, "channel0: channel(id: $channel0)") {
						t.Fatalf("unexpected type query: %+v", query)
					}
					if test.err != nil {
						return gql.Response{}, test.err
					}
					return gql.Response{Data: map[string]any{"channel0": map[string]any{"viewerDropCampaigns": []any{discoveryOffer("unknown", "unknown-drop", test.kind)}}}}, nil
				},
			}
			refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := refresher.fetchChannelOffers(context.Background(), []discoveryChannel{{id: "123", login: "streamer"}})
			if err != nil || rawCalls != 1 {
				t.Fatalf("failed to fetch types: %v, calls=%d", err, rawCalls)
			}
			if test.kind == "" || test.err != nil {
				if len(got) != 1 || stringValue(got[0], "id") != "known" {
					t.Fatalf("incomplete campaign was retained or valid campaign was lost: %+v", got)
				}
				return
			}
			if len(got) != 2 {
				t.Fatalf("typed campaign missing: %+v", got)
			}
			campaign, err := buildCampaign(got[0], nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.kind == "BADGE" && (campaign.Eligible(false) || !campaign.Eligible(true)) {
				t.Fatal("channel discovery ignored enable_badges_emotes")
			}
		})
	}
}

func TestChannelDiscoveryMergesDropsFromDifferentChannels(t *testing.T) {
	now := testNow()
	batchCalls := 0
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName == "DirectoryGameRedirect" {
				return gql.Response{Data: map[string]any{"game": map[string]any{"slug": "game"}}}, nil
			}
			return gql.Response{Data: map[string]any{"game": map[string]any{"streams": map[string]any{"edges": []any{
				map[string]any{"node": map[string]any{"game": map[string]any{"id": "1", "name": "Game"}, "broadcaster": map[string]any{"id": "1", "login": "first"}}},
				map[string]any{"node": map[string]any{"game": map[string]any{"id": "1", "name": "Game"}, "broadcaster": map[string]any{"id": "2", "login": "second"}}},
			}}}}}, nil
		},
		doBatchFunc: func(context.Context, []gql.Operation) ([]gql.Response, error) {
			batchCalls++
			firstOffers := []any{discoveryOffer("shared", "drop-a", "DIRECT_ENTITLEMENT")}
			if batchCalls > 1 {
				firstOffers = []any{}
			}
			later := discoveryOffer("shared", "drop-b", "DIRECT_ENTITLEMENT")
			later["timeBasedDrops"].([]any)[0].(map[string]any)["startAt"] = formatTime(now.Add(30 * time.Minute))
			return []gql.Response{
				{Data: map[string]any{"channel": map[string]any{"viewerDropCampaigns": firstOffers}}},
				{Data: map[string]any{"channel": map[string]any{"viewerDropCampaigns": []any{later}}}},
			}, nil
		},
	}
	refresher, err := NewRefresher(Options{GQLClient: client, AuthState: &fakeAuthState{}, Clock: testNow})
	if err != nil {
		t.Fatal(err)
	}
	options := RefreshOptions{DiscoveryGames: []string{"Game"}}
	for range 2 {
		payload, err := refresher.fetchChannelCampaigns(context.Background(), nil, options)
		if err != nil {
			t.Fatal(err)
		}
		campaign, err := buildCampaign(optionalMap(payload["shared"]), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(campaign.TimedDrops) != 2 || campaign.TimedDrops["drop-a"] == nil || campaign.TimedDrops["drop-b"] == nil || len(campaign.AllowedChannels) != 2 {
			t.Fatalf("later channel replaced earlier drops: %+v", campaign)
		}
		if !campaign.StartsAt.Equal(now.Add(-time.Hour)) || !campaign.CanEarn(now, nil, false, false) {
			t.Fatalf("later drop postponed an active campaign: %+v", campaign)
		}
		if !campaign.TimedDrops["drop-b"].StartsAt.Equal(now.Add(30 * time.Minute)) {
			t.Fatal("merging the campaign changed the later drop's start time")
		}
	}
	known := cloneMap(optionalMap(refresher.discoveredCampaigns["shared"]))
	known["startAt"] = formatTime(now.Add(10 * time.Minute))
	known["endAt"] = formatTime(now.Add(2 * time.Hour))
	payload, err := refresher.fetchChannelCampaigns(context.Background(), map[string]any{"shared": known}, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := optionalMap(payload["shared"]); got["startAt"] != known["startAt"] || got["endAt"] != known["endAt"] {
		t.Fatal("discovery overwrote authoritative inventory times")
	}
}
