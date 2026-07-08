package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

type testTrackerOptions struct {
	gqlClient   GQLClient
	authState   AuthState
	settings    config.Settings
	inventory   inventory.Snapshot
	sleep       func(context.Context, time.Duration) error
	onlineDelay time.Duration
}

func newTestTracker(t *testing.T, options testTrackerOptions) *Tracker {
	t.Helper()

	gqlClient := options.gqlClient
	if gqlClient == nil {
		gqlClient = &fakeGQLClient{
			doFunc: func(context.Context, gql.Operation) (gql.Response, error) {
				return gql.Response{}, nil
			},
			doBatchFunc: func(context.Context, []gql.Operation) ([]gql.Response, error) {
				return nil, nil
			},
		}
	}

	authState := options.authState
	if authState == nil {
		authState = &fakeAuthState{
			snapshot: auth.Snapshot{
				UserID:    1,
				SessionID: "session",
				DeviceID:  "device",
			},
		}
	}

	tracker, err := NewTracker(Options{
		GQLClient:   gqlClient,
		AuthState:   authState,
		OnlineDelay: options.onlineDelay,
		Clock:       testNow,
		Sleep:       options.sleep,
	})
	if err != nil {
		t.Fatalf("NewTracker 返回错误: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(context.Background()); err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	})

	tracker.Configure(options.settings, options.inventory)
	return tracker
}

type fakeGQLClient struct {
	doFunc      func(context.Context, gql.Operation) (gql.Response, error)
	doRawFunc   func(context.Context, gql.RawQuery) (gql.Response, error)
	doBatchFunc func(context.Context, []gql.Operation) ([]gql.Response, error)
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, nil
	}
	return f.doFunc(ctx, operation)
}

func (f *fakeGQLClient) DoRaw(ctx context.Context, query gql.RawQuery) (gql.Response, error) {
	if f.doRawFunc == nil {
		return gql.Response{}, nil
	}
	return f.doRawFunc(ctx, query)
}

func (f *fakeGQLClient) DoBatch(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
	if f.doBatchFunc == nil {
		return nil, nil
	}
	return f.doBatchFunc(ctx, operations)
}

type fakeAuthState struct {
	mu       sync.Mutex
	snapshot auth.Snapshot
}

func (f *fakeAuthState) Validate(context.Context) error {
	return nil
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func mustCampaign(t *testing.T, spec domain.CampaignSpec) *domain.DropsCampaign {
	t.Helper()

	campaign, err := domain.NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func testNow() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
