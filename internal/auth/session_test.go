package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
)

func sessionTestState(t *testing.T, status int, returnedClient, userID string) (*State, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device-1", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case "/validate":
			writeJSON(t, w, status, map[string]any{"client_id": returnedClient, "user_id": userID})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	info := httpclient.AndroidAppClient
	info.ClientURL = server.URL
	path := filepath.Join(t.TempDir(), "cookies.json")
	client, err := httpclient.New(httpclient.Options{Settings: config.Settings{ConnectionQuality: 1}, CookiesPath: path, ClientInfo: info, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	state, err := New(Options{HTTPClient: client, ClientInfo: info, ValidateEndpoint: server.URL + "/validate"})
	if err != nil {
		t.Fatal(err)
	}
	return state, path
}

func TestImportSessionValidatesBeforeReplacingCookies(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		client, user string
		valid        bool
	}{
		{"valid", 200, httpclient.AndroidAppClient.ClientID, "42", true},
		{"expired", 401, "", "", false},
		{"server error", 503, "", "", false},
		{"wrong client", 200, httpclient.TVAppClient.ClientID, "42", false},
		{"invalid user", 200, httpclient.AndroidAppClient.ClientID, "not-a-user", false},
		{"zero user", 200, httpclient.AndroidAppClient.ClientID, "0", false},
		{"negative user", 200, httpclient.AndroidAppClient.ClientID, "-42", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, path := sessionTestState(t, test.status, test.client, test.user)
			state.updatePersistentCookies("original-token", "7")
			if err := state.httpClient.CookieJar().Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			session := Session{Version: SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "import-secret"}
			err = state.ImportSession(context.Background(), session)
			if (err == nil) != test.valid {
				t.Fatalf("import result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), session.AccessToken) {
				t.Fatal("error exposed access token")
			}
			if test.valid {
				if state.Snapshot().UserID != 42 || state.Snapshot().AccessToken != session.AccessToken {
					t.Fatal("validated identity not stored")
				}
				headers, err := state.HeadersProvider(HeadersOptions{GQL: true})(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if headers.Get("Client-Id") != session.ClientID || headers.Get("Authorization") != "OAuth "+session.AccessToken {
					t.Fatal("GQL identity differs from imported identity")
				}
			} else {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) || state.cookieValue("auth-token") != "original-token" {
					t.Fatal("failed import changed original cookie")
				}
			}
		})
	}
}

func TestAndroidMissingOrInvalidSessionRequiresBrowserWithoutDeletingCookies(t *testing.T) {
	for _, test := range []struct {
		name, token, client string
		status              int
	}{
		{"missing", "", httpclient.AndroidAppClient.ClientID, 200},
		{"legacy TV", "legacy-token", httpclient.TVAppClient.ClientID, 200},
		{"expired", "expired-token", "", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, path := sessionTestState(t, test.status, test.client, "42")
			if test.token != "" {
				state.updatePersistentCookies(test.token, "7")
				if err := state.httpClient.CookieJar().Save(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			if err := state.Validate(context.Background()); !errors.Is(err, ErrLoginRequired) {
				t.Fatalf("expected browser instruction: %v", err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || state.cookieValue("auth-token") != test.token {
				t.Fatal("login requirement changed original cookie")
			}
		})
	}
}

func TestSessionFileRoundTripAndRedaction(t *testing.T) {
	session := Session{Version: SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "private-session-token"}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := WriteSession(path, session); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadSession(file)
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if decoded != session {
		t.Fatal("session did not round trip")
	}
	for _, text := range []string{fmt.Sprint(session), fmt.Sprintf("%+v", session), fmt.Sprintf("%#v", session)} {
		if strings.Contains(text, session.AccessToken) {
			t.Fatal("formatted session exposed token")
		}
	}
	if err := WriteSession(path, session); err != nil {
		t.Fatalf("replace protected session: %v", err)
	}
	for _, input := range []string{
		`{"version":1,"client_id":"client","access_token":"private-session-token","unexpected":"secret"}`,
		`{"version":1,"client_id":"client","access_token":"private-session-token"} {}`,
		`{"version":1,"client_id":"client","access_token":"private-session-token"`,
		`{"version":2,"client_id":"client","access_token":"private-session-token"}`,
		`{"version":1,"client_id":"client","access_token":"private-session-token\r\n"}`,
		strings.Repeat(" ", maxSessionBytes+1),
	} {
		if _, err := ReadSession(strings.NewReader(input)); err == nil {
			t.Fatal("invalid session accepted")
		} else if strings.Contains(err.Error(), session.AccessToken) {
			t.Fatal("malformed input appeared in error")
		}
	}
}
