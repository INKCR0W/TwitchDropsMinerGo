package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
)

func catalogTestCampaign() map[string]any {
	now := testNow()
	drop := testDropMap(testDropInput{
		id: "bebe-sticker", name: "Meet Bebe Sticker", startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour), required: 60,
		benefits: []map[string]any{testBenefitMap("bebe-benefit", "Meet Bebe Sticker", "DIRECT_ENTITLEMENT")},
	})
	campaign := testCampaignMap(testCampaignInput{
		id: "prime-time", name: "PRIME TIME", status: "ACTIVE", startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour),
		game:  map[string]any{"id": "1910103699", "displayName": "THE FINALS", "slug": "the-finals"},
		allow: map[string]any{"isEnabled": false},
	})
	campaign["timeBasedDrops"] = []any{drop}
	delete(campaign, "self")
	return campaign
}

func catalogTestDocument(campaigns ...map[string]any) map[string]any {
	return map[string]any{
		"lastUpdatedAt": testNow().Add(-time.Minute), "refreshIntervalSeconds": 60,
		"data": []any{map[string]any{
			"gameId": "1910103699", "gameDisplayName": "THE FINALS", "gameBoxArtURL": "https://example.com/game.jpg",
			"startAt": formatTime(testNow().Add(-time.Hour)), "endAt": formatTime(testNow().Add(time.Hour)), "rewards": campaigns,
		}},
	}
}

func catalogTestRefresher(t *testing.T, handler http.Handler, gqlClient *fakeGQLClient) *Refresher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := httpclient.New(httpclient.Options{
		Settings: config.Settings{ConnectionQuality: 1}, MaxAttempts: 1,
		ClientInfo: httpclient.ClientInfo{UserAgent: "TwitchDropsMinerGo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	// 即使另一个域名存在登录 cookie，公共目录也不能收到它
	twitch, err := url.Parse("https://www.twitch.tv")
	if err != nil {
		t.Fatal(err)
	}
	client.CookieJar().SetCookies(twitch, []*http.Cookie{{Name: "auth-token", Value: "private-token"}})
	if gqlClient == nil {
		gqlClient = &fakeGQLClient{}
	}
	r, err := NewRefresher(Options{
		GQLClient: gqlClient, AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 1}},
		DiscoveryMode: DiscoveryCatalog, CatalogHTTPClient: client, CatalogEndpoint: server.URL, Clock: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func catalogJSONHandler(t *testing.T, document any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "TwitchDropsMinerGo" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected catalog request: %s %+v", request.Method, request.Header)
		}
		for _, name := range []string{"Authorization", "Cookie", "Client-ID", "Client-Session-ID", "X-Device-ID", "Origin"} {
			if request.Header.Get(name) != "" {
				t.Errorf("catalog received private Twitch header %s", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(document); err != nil {
			t.Error(err)
		}
	})
}

func TestCatalogFindsFinalsBeyondFirstTwentyWithoutAccountData(t *testing.T) {
	campaign := catalogTestCampaign()
	campaign["self"] = map[string]any{"isAccountConnected": true}
	drop := campaign["timeBasedDrops"].([]any)[0].(map[string]any)
	drop["self"] = map[string]any{"currentMinutesWatched": 999, "dropInstanceID": "source-claim", "isClaimed": true}
	optionalMap(campaign["game"])["self"] = map[string]any{"private": "source-value"}
	document := catalogTestDocument(campaign)
	groups := make([]any, 0, 26)
	for index := range 25 {
		groups = append(groups, map[string]any{"gameId": fmt.Sprint(index + 1), "rewards": []any{}})
	}
	document["data"] = append(groups, document["data"].([]any)...)
	r := catalogTestRefresher(t, catalogJSONHandler(t, document), nil)
	payload, err := r.fetchCatalogCampaigns(context.Background())
	if err != nil || len(payload) != 1 {
		t.Fatalf("catalog missed later game: %v, %v", payload, err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"self":`)) || bytes.Contains(encoded, []byte("source-claim")) {
		t.Fatal("source account state survived catalog sanitization")
	}
	r.filterCatalogCampaigns(payload)
	result, err := buildCampaign(optionalMap(payload["prime-time"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	resultDrop := result.TimedDrops["bebe-sticker"]
	if result.Game.Name != "THE FINALS" || result.Linked || !result.AccountConnectionUnknown || !result.CanEarn(testNow(), nil, false, false) {
		t.Fatalf("unknown account state was treated as linked or ineligible: %+v", result)
	}
	if resultDrop.IsClaimed || resultDrop.RealCurrentMinutes != 0 || resultDrop.ClaimID != "" {
		t.Fatalf("source progress leaked into user state: %+v", resultDrop)
	}
}

func TestCatalogInventoryAccountAndProgressRemainAuthoritative(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			client := &fakeGQLClient{doFunc: func(_ context.Context, op gql.Operation) (gql.Response, error) {
				switch op.OperationName {
				case "Inventory":
					known := catalogTestCampaign()
					known["self"] = map[string]any{"isAccountConnected": linked}
					known["timeBasedDrops"].([]any)[0].(map[string]any)["self"] = map[string]any{
						"currentMinutesWatched": 17, "dropInstanceID": "real-claim", "isClaimed": false,
					}
					encoded, err := json.Marshal(known)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(encoded, &known); err != nil {
						t.Fatal(err)
					}
					return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{
						"dropCampaignsInProgress": []any{known}, "gameEventDrops": []any{},
					}}}}, nil
				case "ViewerDropsDashboard":
					if op.Variables["fetchRewardCampaigns"] != true {
						t.Fatal("catalog discovery queried the empty TV dashboard")
					}
					return gql.Response{Data: map[string]any{}}, nil
				default:
					return gql.Response{}, fmt.Errorf("unexpected operation: %s", op.OperationName)
				}
			}}
			r := catalogTestRefresher(t, catalogJSONHandler(t, catalogTestDocument(catalogTestCampaign())), client)
			snapshot, err := r.Refresh(context.Background(), RefreshOptions{})
			if err != nil {
				t.Fatal(err)
			}
			campaign := snapshot.Campaigns["prime-time"]
			if campaign == nil || campaign.Linked != linked || campaign.AccountConnectionUnknown || campaign.Eligible(false) != linked {
				t.Fatalf("catalog replaced validated account state: %+v", campaign)
			}
			drop := campaign.TimedDrops["bebe-sticker"]
			if drop.RealCurrentMinutes != 17 || drop.ClaimID != "real-claim" || drop.IsClaimed {
				t.Fatalf("catalog replaced user progress: %+v", drop)
			}
		})
	}
}

func TestCatalogRejectsInvalidOrStaleResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"stale", func(d map[string]any) { d["lastUpdatedAt"] = testNow().Add(-catalogMaxAge - time.Second) }},
		{"future clock", func(d map[string]any) { d["lastUpdatedAt"] = testNow().Add(catalogClockTolerance + time.Second) }},
		{"missing timestamp", func(d map[string]any) { delete(d, "lastUpdatedAt") }},
		{"missing data", func(d map[string]any) { delete(d, "data") }},
		{"wrong data type", func(d map[string]any) { d["data"] = map[string]any{} }},
		{"missing interval", func(d map[string]any) { delete(d, "refreshIntervalSeconds") }},
		{"missing rewards", func(d map[string]any) { d["data"] = []any{map[string]any{"gameId": "1"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := catalogTestDocument(catalogTestCampaign())
			test.change(document)
			r := catalogTestRefresher(t, catalogJSONHandler(t, document), nil)
			if payload, err := r.fetchCatalogCampaigns(context.Background()); err == nil || payload != nil {
				t.Fatalf("bad catalog became an empty success: %v, %v", payload, err)
			}
		})
	}
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := catalogTestRefresher(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }), nil)
			if payload, err := r.fetchCatalogCampaigns(context.Background()); err == nil || payload != nil {
				t.Fatalf("failed request became an empty success: %v, %v", payload, err)
			}
		})
	}
}

func TestCatalogFiltersStatusAndTimeAndSupportsCancellation(t *testing.T) {
	active := catalogTestCampaign()
	upcoming := catalogTestCampaign()
	upcoming["id"], upcoming["status"] = "upcoming", "UPCOMING"
	expired := catalogTestCampaign()
	expired["id"], expired["endAt"] = "expired", formatTime(testNow())
	r := catalogTestRefresher(t, catalogJSONHandler(t, catalogTestDocument(active, upcoming, expired)), nil)
	payload, err := r.fetchCatalogCampaigns(context.Background())
	if err != nil || len(payload) != 1 || payload["prime-time"] == nil {
		t.Fatalf("inactive campaigns were not filtered: %v, %v", payload, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.fetchCatalogCampaigns(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed: %v", err)
	}
}
