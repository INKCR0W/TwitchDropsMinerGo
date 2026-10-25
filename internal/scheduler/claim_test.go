package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"twitchdropsminergo/internal/gql"
)

func TestClaimDropRequestChecksClaimResult(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		data        any
		wantClaimed bool
		wantError   bool
	}{
		{"success", map[string]any{"status": "ELIGIBLE_FOR_ALL"}, true, false},
		{"already claimed", map[string]any{"status": "DROP_INSTANCE_ALREADY_CLAIMED"}, true, false},
		{"null result", nil, false, true},
		{"missing status", map[string]any{}, false, true},
		{"failed", map[string]any{"status": "NOT_ELIGIBLE"}, false, true},
		{"unknown status", map[string]any{"status": "NEW_STATUS"}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Scheduler{gqlClient: &fakeGQLClient{doFunc: func(_ context.Context, op gql.Operation) (gql.Response, error) {
				if op.OperationName != gql.MustLookup(gql.OperationClaimDrop).OperationName {
					t.Fatalf("错误的领取操作: %s", op.OperationName)
				}
				input, ok := op.Variables["input"].(map[string]any)
				if !ok || input["dropInstanceID"] != "claim-1" {
					t.Fatalf("领取变量不匹配: %#v", op.Variables)
				}
				return gql.Response{Data: map[string]any{"claimDropRewards": test.data}}, nil
			}}}
			claimed, err := s.claimDropRequest(context.Background(), " claim-1 ")
			if claimed != test.wantClaimed || (err != nil) != test.wantError {
				t.Fatalf("claimed=%v err=%v", claimed, err)
			}
			if test.wantError && !strings.Contains(err.Error(), "ClaimDrop") {
				t.Fatalf("错误缺少领取上下文: %v", err)
			}
		})
	}
}

func TestClaimDropRequestPropagatesRequestError(t *testing.T) {
	t.Parallel()
	want := errors.New("request failed")
	s := &Scheduler{gqlClient: &fakeGQLClient{doFunc: func(context.Context, gql.Operation) (gql.Response, error) {
		return gql.Response{}, want
	}}}
	claimed, err := s.claimDropRequest(context.Background(), "claim-1")
	if claimed || !errors.Is(err, want) {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
}
