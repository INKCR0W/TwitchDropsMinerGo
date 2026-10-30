package browserlogin

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestLoginChecksCallbackAgainstCurrentAuthorization(t *testing.T) {
	for _, test := range []struct {
		name     string
		callback func(string) string
		valid    bool
	}{
		{"valid", func(state string) string {
			return " https://www.twitch.tv/#access_token=test-token&token_type=bearer&state=" + state + "\r\n"
		}, true},
		{"wrong origin", func(state string) string {
			return "https://evil.test/#access_token=test-token&token_type=bearer&state=" + state
		}, false},
		{"wrong state", func(string) string {
			return "https://www.twitch.tv/#access_token=test-token&token_type=bearer&state=wrong"
		}, false},
		{"duplicate state", func(state string) string {
			return "https://www.twitch.tv/#access_token=test-token&token_type=bearer&state=" + state + "&state=" + state
		}, false},
		{"duplicate token", func(state string) string {
			return "https://www.twitch.tv/#access_token=test-token&access_token=other&token_type=bearer&state=" + state
		}, false},
		{"denied", func(state string) string { return "https://www.twitch.tv/?error=access_denied&state=" + state }, false},
		{"empty", func(string) string { return "" }, false},
		{"homepage", func(string) string { return "https://www.twitch.tv/" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, err := Login(context.Background(), func(ctx context.Context, address string) (string, error) {
				if ctx.Err() != nil {
					t.Fatal("unexpected cancelled context")
				}
				u, parseErr := url.Parse(address)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				redirect, parseErr := url.ParseQuery(u.Query().Get("redirect_params"))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				return test.callback(redirect.Get("state")), nil
			})
			if test.valid {
				if err != nil || token != "test-token" {
					t.Fatal("valid authorization rejected")
				}
				return
			}
			if err == nil || token != "" || strings.Contains(err.Error(), "test-token") {
				t.Fatal("invalid authorization accepted or leaked")
			}
		})
	}
}

func TestLoginHonorsCancellationAndInputFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Login(ctx, func(context.Context, string) (string, error) {
		t.Fatal("cancelled login requested user input")
		return "", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if _, err := Login(ctx, func(context.Context, string) (string, error) {
		cancel()
		return "https://www.twitch.tv/", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled result accepted")
	}
	inputErr := errors.New("input failed")
	if _, err := Login(context.Background(), func(context.Context, string) (string, error) {
		return "", inputErr
	}); !errors.Is(err, inputErr) {
		t.Fatal("input error lost")
	}
	if _, err := Login(context.Background(), nil); err == nil {
		t.Fatal("missing input callback accepted")
	}
}
