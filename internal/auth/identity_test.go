package auth

import (
	"context"
	"os"
	"testing"

	"twitchdropsminergo/internal/httpclient"
)

func TestBindUserRequiresValidatedStateAndKeepsIdentity(t *testing.T) {
	state, _ := sessionTestState(t, 200, httpclient.AndroidAppClient.ClientID, "7")
	if _, err := state.BindUser(); err == nil {
		t.Fatal("未认证状态不应绑定账号")
	}
	if err := state.ImportSession(context.Background(), Session{Version: SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if id, err := state.BindUser(); err != nil || id != 7 {
			t.Fatalf("重复绑定不匹配: id=%d err=%v", id, err)
		}
	}
	state.Clear()
	if _, err := state.BindUser(); err == nil {
		t.Fatal("清空后的状态需重新校验")
	}
	if state.boundUserID != 7 {
		t.Fatal("Clear解除了服务账号绑定")
	}
}

func TestBoundUserRejectsDifferentValidatedIdentity(t *testing.T) {
	for _, mode := range []string{"import", "browser", "TV"} {
		t.Run(mode, func(t *testing.T) {
			clientID := httpclient.AndroidAppClient.ClientID
			if mode == "TV" {
				clientID = httpclient.TVAppClient.ClientID
			}
			state, path := sessionTestState(t, 200, clientID, "42")
			state.clientInfo.ClientID = clientID
			state.setAccessToken("old-token")
			state.setValidatedAccessState(7)
			state.updatePersistentCookies("old-token", "7")
			if err := state.httpClient.CookieJar().Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if id, err := state.BindUser(); err != nil || id != 7 {
				t.Fatalf("bind: id=%d err=%v", id, err)
			}
			if mode == "import" {
				err = state.ImportSession(context.Background(), Session{Version: SessionVersion, ClientID: clientID, AccessToken: "new-token"})
			} else {
				state.lastValidatedAt = state.lastValidatedAt.Add(-2 * state.revalidateInterval)
				err = state.Validate(context.Background())
			}
			if err == nil {
				t.Fatal("绑定账号后不应接受其他用户")
			}
			if snapshot := state.Snapshot(); snapshot.UserID != 7 || snapshot.AccessToken != "old-token" {
				t.Fatalf("拒绝前改变了状态: user=%d", snapshot.UserID)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || state.cookieValue("auth-token") != "old-token" {
				t.Fatal("拒绝前改变了cookie")
			}
		})
	}
}
