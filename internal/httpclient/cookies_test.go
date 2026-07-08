package httpclient

import (
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPersistentJarConcurrentAccessAndClear(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				jar.SetCookies(targetURL, []*http.Cookie{{
					Name: "auth-token", Value: "v", Path: "/", Domain: "www.twitch.tv",
				}})
				_ = jar.Cookies(targetURL)
			}
		}()
	}

	clearerDone := make(chan struct{})
	go func() {
		defer close(clearerDone)
		for i := 0; i < 300; i++ {
			_ = jar.ClearDomain("www.twitch.tv")
			_ = jar.Clear()
		}
	}()

	<-clearerDone
	close(stop)
	wg.Wait()
}

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

func TestPersistentJarClearDomainRemovesMatchingCookies(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	twitchURL, err := url.Parse("https://www.twitch.tv")
	if err != nil {
		t.Fatalf("解析 Twitch URL 失败: %v", err)
	}
	otherURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatalf("解析其他 URL 失败: %v", err)
	}

	jar.SetCookies(twitchURL, []*http.Cookie{
		{Name: "auth-token", Value: "token", Domain: "www.twitch.tv", Path: "/"},
		{Name: "unique_id", Value: "device", Domain: ".twitch.tv", Path: "/"},
		{Name: "host-only", Value: "host", Path: "/"},
	})
	jar.SetCookies(otherURL, []*http.Cookie{
		{Name: "session", Value: "keep", Domain: "example.com", Path: "/"},
	})

	if err := jar.ClearDomain("twitch.tv"); err != nil {
		t.Fatalf("ClearDomain 返回错误: %v", err)
	}
	if err := jar.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	reloadedJar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 返回错误: %v", err)
	}

	if cookies := reloadedJar.Cookies(twitchURL); len(cookies) != 0 {
		t.Fatalf("twitch 域名 Cookie 应已清空: %#v", cookies)
	}

	cookies := reloadedJar.Cookies(otherURL)
	if len(cookies) != 1 || cookies[0].Name != "session" {
		t.Fatalf("非目标域名 Cookie 不应被删除: %#v", cookies)
	}
}

func TestPersistentJarClearRemovesAllCookies(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}

	jar.SetCookies(targetURL, []*http.Cookie{
		{Name: "auth-token", Value: "token", Domain: "www.twitch.tv", Path: "/"},
	})

	if err := jar.Clear(); err != nil {
		t.Fatalf("Clear 返回错误: %v", err)
	}
	if err := jar.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	reloadedJar, err := NewPersistentJar(path)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 返回错误: %v", err)
	}

	if cookies := reloadedJar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("Clear 后不应恢复任何 Cookie: %#v", cookies)
	}
}
