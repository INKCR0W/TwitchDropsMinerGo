package httpclient

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentJarConcurrentAccessAndClear(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path, nil)
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
	jar, err := NewPersistentJar(path, nil)
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

	reloadedJar, err := NewPersistentJar(path, nil)
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
	jar, err := NewPersistentJar(path, nil)
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

	reloadedJar, err := NewPersistentJar(path, nil)
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
	jar, err := NewPersistentJar(path, nil)
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

	reloadedJar, err := NewPersistentJar(path, nil)
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
	jar, err := NewPersistentJar(path, nil)
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

	reloadedJar, err := NewPersistentJar(path, nil)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 返回错误: %v", err)
	}

	if cookies := reloadedJar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("Clear 后不应恢复任何 Cookie: %#v", cookies)
	}
}

func TestNewPersistentJarQuarantinesCorruptFileAndStartsEmpty(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "cookies.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("写入损坏的 Cookie 文件失败: %v", err)
	}

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	jar, err := NewPersistentJar(path, logger)
	if err != nil {
		t.Fatalf("Cookie 文件损坏时不应返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	if cookies := jar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("损坏后应以空 jar 启动, got=%#v", cookies)
	}

	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("损坏的 Cookie 文件应被移走, err=%v", statErr)
	}
	data, err := os.ReadFile(path + ".corrupt")
	if err != nil {
		t.Fatalf("读取隔离文件失败: %v", err)
	}
	if string(data) != "{ not json" {
		t.Errorf("隔离文件应保留原始内容, got=%q", data)
	}
	if !strings.Contains(logs.String(), "Cookie 文件已损坏") {
		t.Errorf("应记录损坏告警, logs=%q", logs.String())
	}
}

func TestNewPersistentJarStartsEmptyWhenQuarantineFails(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("写入损坏的 Cookie 文件失败: %v", err)
	}
	// 隔离目标是非空目录，os.Remove 与 os.Rename 都会失败
	if err := os.MkdirAll(path+".corrupt", 0o700); err != nil {
		t.Fatalf("创建隔离目标目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path+".corrupt", "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatalf("占用隔离目标失败: %v", err)
	}

	var logs strings.Builder
	jar, err := NewPersistentJar(path, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("隔离失败时不应返回错误，否则重新退化成启动崩溃循环: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	if cookies := jar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("隔离失败后仍应以空 jar 启动, got=%#v", cookies)
	}
	if !strings.Contains(logs.String(), "隔离损坏的 Cookie 文件失败") {
		t.Errorf("应记录隔离失败告警, logs=%q", logs.String())
	}
}

func TestNewPersistentJarRecoversFromBackupWithoutQuarantine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	jar, err := NewPersistentJar(path, nil)
	if err != nil {
		t.Fatalf("NewPersistentJar 返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	jar.SetCookies(targetURL, []*http.Cookie{
		{Name: "auth-token", Value: "secret", Path: "/", Domain: "www.twitch.tv", Expires: time.Now().Add(time.Hour).UTC()},
	})
	if err := jar.Save(); err != nil {
		t.Fatalf("Save 返回错误: %v", err)
	}

	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取已保存的 Cookie 文件失败: %v", err)
	}
	if err := os.WriteFile(path+".bak", valid, 0o600); err != nil {
		t.Fatalf("写入备份文件失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("损坏主文件失败: %v", err)
	}

	var logs strings.Builder
	reloaded, err := NewPersistentJar(path, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("备份可用时应静默恢复: %v", err)
	}
	cookies := reloaded.Cookies(targetURL)
	if len(cookies) != 1 || cookies[0].Value != "secret" {
		t.Fatalf("应从备份恢复出 auth-token, got=%#v", cookies)
	}
	if _, statErr := os.Stat(path + ".corrupt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("备份可用时不应隔离任何文件, err=%v", statErr)
	}
	if strings.Contains(logs.String(), "损坏") {
		t.Errorf("备份可用时不应告警, logs=%q", logs.String())
	}
}

func TestNewPersistentJarStartsEmptyWhenCorruptFileIsPartiallyDecodable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	// 语法合法但字段类型错误：json 会边报错边把可解码的条目填进去
	content := `{"schema_version":1,"cookies":{"https://www.twitch.tv/":[` +
		`{"name":"auth-token","value":"stale","path":"/","domain":"www.twitch.tv"},` +
		`{"name":"broken","value":123}]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入损坏的 Cookie 文件失败: %v", err)
	}

	jar, err := NewPersistentJar(path, nil)
	if err != nil {
		t.Fatalf("Cookie 文件损坏时不应返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	if cookies := jar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("损坏文件的部分解码结果不应泄漏进空 jar, got=%#v", cookies)
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Errorf("损坏文件应被隔离: %v", statErr)
	}
}

func TestNewPersistentJarQuarantinesCorruptBackupWhenPrimaryMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path+".bak", []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("写入损坏的备份文件失败: %v", err)
	}

	jar, err := NewPersistentJar(path, nil)
	if err != nil {
		t.Fatalf("仅备份损坏时不应返回错误: %v", err)
	}

	targetURL, err := url.Parse("https://www.twitch.tv/")
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}
	if cookies := jar.Cookies(targetURL); len(cookies) != 0 {
		t.Fatalf("应以空 jar 启动, got=%#v", cookies)
	}
	if _, statErr := os.Stat(path + ".bak.corrupt"); statErr != nil {
		t.Errorf("损坏的备份文件应被隔离: %v", statErr)
	}
	if _, statErr := os.Stat(path + ".bak"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("原备份文件应已被移走, err=%v", statErr)
	}
}
