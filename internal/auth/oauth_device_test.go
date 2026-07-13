package auth

import (
	"testing"

	"twitchdropsminergo/internal/httpclient"
)

func TestStateValidatePerformsDeviceCodeLoginAndPersistsSession(t *testing.T) {
	t.Parallel()

	fixture := newValidatedDeviceCodeState(t)

	snapshot := fixture.state.Snapshot()
	if snapshot.DeviceID != "device-1" {
		t.Fatalf("device_id 不匹配: %q", snapshot.DeviceID)
	}
	if snapshot.SessionID != "0123456789abcdef" {
		t.Fatalf("session_id 不匹配: %q", snapshot.SessionID)
	}
	if snapshot.AccessToken != "token-1" {
		t.Fatalf("access_token 不匹配: %q", snapshot.AccessToken)
	}
	if snapshot.UserID != 42 {
		t.Fatalf("user_id 不匹配: %d", snapshot.UserID)
	}

	if fixture.announcedCode.UserCode != "ABCD-EFGH" || fixture.announcedCode.VerificationURI == "" {
		t.Fatalf("device code 回调信息不完整: %#v", fixture.announcedCode)
	}
	if fixture.homeHits != 1 || fixture.deviceHits != 1 || fixture.tokenHits != 2 || fixture.validateHits != 1 {
		t.Fatalf("调用次数不匹配: home=%d device=%d token=%d validate=%d", fixture.homeHits, fixture.deviceHits, fixture.tokenHits, fixture.validateHits)
	}
	if len(fixture.sleeps) != 2 {
		t.Fatalf("轮询次数不匹配: %#v", fixture.sleeps)
	}

	reloadedJar, err := httpclient.NewPersistentJar(fixture.cookiesPath, nil)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 失败: %v", err)
	}

	cookies := cookieMap(reloadedJar.Cookies(mustParseURL(t, fixture.serverURL)))
	if cookies["auth-token"] != "token-1" {
		t.Fatalf("auth-token 未持久化: %#v", cookies)
	}
	if cookies["persistent"] != "42" {
		t.Fatalf("persistent 未持久化: %#v", cookies)
	}
}

func TestValidateDeviceFlowInvokesAuthenticatedHandler(t *testing.T) {
	t.Parallel()

	var calls int
	newValidatedDeviceCodeState(t, func(options *Options) {
		options.AuthenticatedHandler = func() { calls++ }
	})

	if calls != 1 {
		t.Fatalf("AuthenticatedHandler 调用次数不匹配: %d", calls)
	}
}
