package httpclient

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentJarRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/settings/profile")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}

	jar.SetCookies(targetURL, []*http.Cookie{
		{
			Name:     "auth-token",
			Value:    "secret",
			Path:     "/",
			Domain:   "www.twitch.tv",
			Expires:  time.Now().Add(time.Hour).UTC(),
			HttpOnly: true,
			Secure:   true,
		},
	})
	if err := jar.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	reloadedJar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 返回错误: %v", err)
	}

	cookies := reloadedJar.Cookies(targetURL)
	if len(cookies) != 1 {
		t.Fatalf("期望恢复 1 个 cookie，实际为 %d", len(cookies))
	}
	if cookies[0].Name != "auth-token" || cookies[0].Value != "secret" {
		t.Fatalf("恢复的 cookie 不匹配: %#v", cookies[0])
	}
}

func TestPersistentJarDropsExpiredCookies(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://id.twitch.tv/oauth2")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}

	jar.SetCookies(targetURL, []*http.Cookie{
		{
			Name:    "expired",
			Value:   "gone",
			Domain:  "id.twitch.tv",
			Path:    "/",
			Expires: time.Now().Add(-time.Minute).UTC(),
		},
	})

	if err := jar.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	reloadedJar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 返回错误: %v", err)
	}

	if cookies := reloadedJar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("过期 cookie 不应被恢复: %#v", cookies)
	}
}
