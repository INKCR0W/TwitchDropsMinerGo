package browserlogin

import (
	"net/url"
	"strings"
	"testing"
)

func TestLoginURLPreservesAndroidRedirectAndState(t *testing.T) {
	raw, state, err := loginURL()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	v, _ := url.ParseQuery(u.Query().Get("redirect_params"))
	if u.Host != "www.twitch.tv" || v.Get("redirect_uri") != "https://www.twitch.tv/" || v.Get("state") != state || v.Get("response_type") != "token" || len(state) != 64 {
		t.Fatal("invalid Android authorization URL")
	}
}
func TestCallbackRequiresTrustedOriginAndState(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		err   bool
		token string
	}{
		{"https://www.twitch.tv/#access_token=secret&token_type=bearer&state=valid", false, "secret"},
		{"https://evil.test/#access_token=secret&token_type=bearer&state=valid", false, ""},
		{"https://www.twitch.tv/#access_token=secret&token_type=bearer&state=wrong", true, ""},
		{"https://www.twitch.tv/#error=access_denied&state=valid", true, ""},
		{"https://www.twitch.tv/#access_token=secret&state=valid", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=valid", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=wrong", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=valid&error_description=secret", true, ""},
		{"https://www.twitch.tv/?other=value", false, ""},
		{"https://www.twitch.tv/?access_token=secret&token_type=bearer&state=valid", false, ""},
		{"https://evil.test/?error=access_denied&state=valid", false, ""},
	} {
		token, err := callbackToken(tt.raw, "valid")
		if (err != nil) != tt.err || token != tt.token {
			t.Fatal("unexpected callback result")
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Fatal("token leaked")
		}
	}
}
