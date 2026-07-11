package watch

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
)

type testTrackerOptions struct {
	gqlClient   GQLClient
	spadeClient SpadeClient
	authState   AuthState
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

	spadeClient := options.spadeClient
	if spadeClient == nil {
		spadeClient = &fakeSpadeClient{}
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
		GQLClient:    gqlClient,
		SpadeClient:  spadeClient,
		WatchHeaders: func(context.Context) (http.Header, error) { return http.Header{}, nil },
		AuthState:    authState,
		OnlineDelay:  options.onlineDelay,
		Clock:        testNow,
		Sleep:        options.sleep,
	})
	if err != nil {
		t.Fatalf("NewTracker 返回错误: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(context.Background()); err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	})

	return tracker
}

type fakeGQLClient struct {
	doFunc      func(context.Context, gql.Operation) (gql.Response, error)
	doBatchFunc func(context.Context, []gql.Operation) ([]gql.Response, error)
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, nil
	}
	return f.doFunc(ctx, operation)
}

func (f *fakeGQLClient) DoBatch(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
	if f.doBatchFunc == nil {
		return nil, nil
	}
	return f.doBatchFunc(ctx, operations)
}

type fakeSpadeClient struct {
	mu       sync.Mutex
	lastReq  httpclient.Request
	status   int
	err      error
	callFunc func(httpclient.Request) (httpclient.Response, error)
}

func (f *fakeSpadeClient) Do(_ context.Context, request httpclient.Request) (httpclient.Response, error) {
	f.mu.Lock()
	f.lastReq = request
	f.mu.Unlock()
	if f.callFunc != nil {
		return f.callFunc(request)
	}
	if f.err != nil {
		return httpclient.Response{}, f.err
	}
	status := f.status
	if status == 0 {
		status = 204
	}
	return httpclient.Response{StatusCode: status}, nil
}

func (f *fakeSpadeClient) lastRequest() httpclient.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq
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
