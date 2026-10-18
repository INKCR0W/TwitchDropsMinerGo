package watch

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
)

type testTrackerOptions struct {
	gqlClient   GQLClient
	httpClient  HTTPClient
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

	httpClient := options.httpClient
	if httpClient == nil {
		httpClient = &fakeHTTPClient{}
	}

	authState := options.authState
	if authState == nil {
		authState = &fakeAuthState{}
	}

	tracker, err := NewTracker(Options{
		GQLClient:   gqlClient,
		HTTPClient:  httpClient,
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

type fakeHTTPClient struct {
	callFunc func(httpclient.Request) (httpclient.Response, error)
}

func (f *fakeHTTPClient) Do(_ context.Context, request httpclient.Request) (httpclient.Response, error) {
	if f.callFunc != nil {
		return f.callFunc(request)
	}
	return httpclient.Response{StatusCode: 200}, nil
}

type fakeAuthState struct{}

func (f *fakeAuthState) Validate(context.Context) error { return nil }

func testNow() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
