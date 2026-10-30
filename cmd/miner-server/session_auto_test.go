package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/runtime"
)

type sessionImportFunc func(context.Context, auth.Session) error

func (f sessionImportFunc) ImportSession(ctx context.Context, session auth.Session) error {
	return f(ctx, session)
}

func TestDefaultSessionMissingDoesNotTouchFilesOrImporter(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, runtime.SessionFileName)
	called := false
	imported, err := importDefaultSession(context.Background(), path, sessionImportFunc(func(context.Context, auth.Session) error {
		called = true
		return nil
	}))
	files, readErr := os.ReadDir(directory)
	if err != nil || readErr != nil || imported || called || len(files) != 0 {
		t.Fatalf("缺少默认会话不应创建文件或触发导入: imported=%v called=%v err=%v read=%v", imported, called, err, readErr)
	}
}

func TestDefaultSessionValidatesBeforeConsumingExport(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		client string
		valid  bool
	}{
		{"valid", 200, httpclient.AndroidAppClient.ClientID, true},
		{"expired", 401, httpclient.AndroidAppClient.ClientID, false},
		{"wrong client", 200, httpclient.TVAppClient.ClientID, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			layout, err := runtime.ResolveLayout(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := layout.Ensure(); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/validate" || r.Header.Get("Authorization") != "OAuth new-session-token" {
					t.Error("自动导入未使用校验接口或新会话")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"client_id":"` + test.client + `","user_id":"42"}`))
			}))
			defer server.Close()
			client, err := httpclient.New(httpclient.Options{Settings: config.DefaultSettings(), CookiesPath: layout.CookiesFile, MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			address, err := url.Parse(httpclient.AndroidAppClient.ClientURL)
			if err != nil {
				t.Fatal(err)
			}
			client.CookieJar().SetCookies(address, []*http.Cookie{{Name: "auth-token", Value: "old-session-token", Path: "/"}})
			if err := client.CookieJar().Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(layout.CookiesFile)
			if err != nil {
				t.Fatal(err)
			}
			session := auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "new-session-token"}
			if err := auth.WriteSession(layout.SessionFile, session); err != nil {
				t.Fatal(err)
			}
			state, err := auth.New(auth.Options{HTTPClient: client, ValidateEndpoint: server.URL + "/validate"})
			if err != nil {
				t.Fatal(err)
			}
			imported, err := importDefaultSession(context.Background(), layout.SessionFile, state)
			if (err == nil) != test.valid || imported != test.valid {
				t.Fatalf("导入结果不匹配: imported=%v err=%v", imported, err)
			}
			if test.valid {
				if _, err := os.Stat(layout.SessionFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("成功导入后没有消耗默认交换文件")
				}
				if state.Snapshot().UserID != 42 {
					t.Fatal("自动导入没有保留校验后的账号")
				}
			} else {
				after, readErr := os.ReadFile(layout.CookiesFile)
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("失败导入改动了原有 cookie")
				}
				if _, err := os.Stat(layout.SessionFile); err != nil {
					t.Fatal("失败导入丢失了授权文件")
				}
			}
		})
	}
}

func TestDefaultSessionPreservesExternalReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), runtime.SessionFileName)
	old := auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "old-session-token"}
	newer := old
	newer.AccessToken = "replacement-session-token"
	if err := auth.WriteSession(path, old); err != nil {
		t.Fatal(err)
	}
	imported, err := importDefaultSession(context.Background(), path, sessionImportFunc(func(_ context.Context, session auth.Session) error {
		if session != old {
			t.Error("没有读取原会话")
		}
		return auth.WriteSession(path, newer)
	}))
	if err != nil || !imported {
		t.Fatalf("导入失败: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("误删了外部替换的新会话")
	}
	actual, readErr := auth.ReadSession(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || actual != newer {
		t.Fatal("新上传的会话未保留")
	}
}

func TestDefaultSessionRespectsExportLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), runtime.SessionFileName)
	session := auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "token"}
	if err := auth.WriteSession(path, session); err != nil {
		t.Fatal(err)
	}
	lock, err := runtime.AcquireSessionLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = importDefaultSession(ctx, path, sessionImportFunc(func(context.Context, auth.Session) error { t.Error("绕过正在导出的会话锁"); return nil }))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("会话锁未遵守取消: %v", err)
	}
}

func TestServerInstanceLockPrecedesDefaultSessionImport(t *testing.T) {
	layout, err := runtime.ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	lock, err := runtime.AcquireInstanceLock(layout.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(layout.SessionFile, []byte("invalid session"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-runtime-dir", layout.RootDir}); code != 3 {
		t.Fatalf("已有服务运行时应先拒绝启动: %d", code)
	}
	if _, err := os.Stat(layout.SessionFile + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("持实例锁前触发了自动导入")
	}
}

type portableFixture struct {
	layout      runtime.Layout
	state       *portableAuthState
	client      *httpclient.Client
	address     *url.URL
	oldStatus   atomic.Int32
	newStatus   atomic.Int32
	oldCalls    atomic.Int32
	newCalls    atomic.Int32
	deviceCalls atomic.Int32
}

func newPortableFixture(t *testing.T, cookie string) *portableFixture {
	t.Helper()
	f := &portableFixture{}
	var err error
	f.layout, err = runtime.ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	f.oldStatus.Store(http.StatusOK)
	f.newStatus.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			f.deviceCalls.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device", Path: "/"})
			return
		}
		if r.URL.Path != "/validate" {
			t.Error("意外的认证请求")
			w.WriteHeader(404)
			return
		}
		status, user := int(f.oldStatus.Load()), "7"
		switch r.Header.Get("Authorization") {
		case "OAuth old-token":
			f.oldCalls.Add(1)
		case "OAuth new-token":
			f.newCalls.Add(1)
			status, user = int(f.newStatus.Load()), "42"
		default:
			t.Error("意外的令牌校验")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"client_id":%q,"user_id":%q}`, httpclient.AndroidAppClient.ClientID, user)
	}))
	t.Cleanup(server.Close)
	info := httpclient.AndroidAppClient
	info.ClientURL = server.URL
	f.client, err = httpclient.New(httpclient.Options{Settings: config.DefaultSettings(), CookiesPath: f.layout.CookiesFile, ClientInfo: info, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.client.Close(); err != nil {
			t.Error(err)
		}
	})
	f.address, err = url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		f.client.CookieJar().SetCookies(f.address, []*http.Cookie{{Name: "auth-token", Value: cookie, Path: "/"}})
	}
	if err := f.client.CookieJar().Save(); err != nil {
		t.Fatal(err)
	}
	base, err := auth.New(auth.Options{HTTPClient: f.client, ClientInfo: info, ValidateEndpoint: server.URL + "/validate"})
	if err != nil {
		t.Fatal(err)
	}
	f.state = &portableAuthState{State: base, sessionFile: f.layout.SessionFile}
	return f
}

func writePortableSession(t *testing.T, path string) {
	t.Helper()
	if err := auth.WriteSession(path, auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "new-token"}); err != nil {
		t.Fatal(err)
	}
}

func TestPortableAuthUsesValidCookieWithoutReadingExport(t *testing.T) {
	f := newPortableFixture(t, "old-token")
	invalid := []byte("unreadable session format")
	if err := os.WriteFile(f.layout.SessionFile, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.layout.SessionFile)
	if err != nil || !bytes.Equal(data, invalid) {
		t.Fatal("有效cookie时触碰了交换文件")
	}
	if _, err := os.Stat(f.layout.SessionFile + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("有效cookie时进入了默认会话读取流程")
	}
	if f.oldCalls.Load() != 1 || f.newCalls.Load() != 0 || f.state.Snapshot().UserID != 7 {
		t.Fatal("有效cookie未优先保留")
	}
}

func TestPortableAuthFallsBackOnlyWhenCookieRequiresLogin(t *testing.T) {
	for _, cookie := range []string{"", "old-token"} {
		t.Run(cookie, func(t *testing.T) {
			f := newPortableFixture(t, cookie)
			f.oldStatus.Store(http.StatusUnauthorized)
			writePortableSession(t, f.layout.SessionFile)
			headers, err := f.state.HeadersProvider(auth.HeadersOptions{GQL: true})(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if headers.Get("Authorization") != "OAuth new-token" || headers.Get("Client-Id") != httpclient.AndroidAppClient.ClientID || headers.Get("X-Device-Id") != "device" {
				t.Fatal("回退导入后请求头未使用已校验的新会话")
			}
			if _, err := os.Stat(f.layout.SessionFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("成功回退后未消耗交换文件")
			}
			if f.newCalls.Load() != 1 || f.deviceCalls.Load() != 1 || f.state.Snapshot().UserID != 42 {
				t.Fatal("回退导入重复校验或未初始化设备")
			}
		})
	}
}

func TestPortableAuthNetworkFailureDoesNotReadExport(t *testing.T) {
	f := newPortableFixture(t, "old-token")
	f.oldStatus.Store(http.StatusServiceUnavailable)
	writePortableSession(t, f.layout.SessionFile)
	before, err := os.ReadFile(f.layout.CookiesFile)
	if err != nil {
		t.Fatal(err)
	}
	err = f.state.Validate(context.Background())
	if err == nil || errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("网络失败误判为登录失效: %v", err)
	}
	after, readErr := os.ReadFile(f.layout.CookiesFile)
	if readErr != nil || !bytes.Equal(before, after) || f.newCalls.Load() != 0 {
		t.Fatal("网络失败覆盖了cookie或读取新会话")
	}
	if _, err := os.Stat(f.layout.SessionFile + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("网络失败进入了默认会话导入")
	}
	f.oldStatus.Store(http.StatusOK)
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state.Snapshot().UserID != 7 {
		t.Fatal("网络恢复后未继续使用旧cookie")
	}
	if _, err := os.Stat(f.layout.SessionFile); err != nil {
		t.Fatal("网络恢复后误消费新会话")
	}
}

func TestPortableAuthRetriesDefaultImportAfterNetworkRecovery(t *testing.T) {
	f := newPortableFixture(t, "")
	f.newStatus.Store(http.StatusServiceUnavailable)
	writePortableSession(t, f.layout.SessionFile)
	if err := f.state.Validate(context.Background()); err == nil {
		t.Fatal("导入网络错误未传播给调度")
	}
	if _, err := os.Stat(f.layout.SessionFile); err != nil {
		t.Fatal("导入网络失败丢失了交换文件")
	}
	f.newStatus.Store(http.StatusOK)
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.newCalls.Load() != 2 || f.state.Snapshot().UserID != 42 {
		t.Fatal("正常认证重试未再次尝试默认会话")
	}
}

func TestPortableAuthMissingExportKeepsOriginalLoginBehavior(t *testing.T) {
	for _, cookie := range []string{"", "old-token"} {
		t.Run(cookie, func(t *testing.T) {
			f := newPortableFixture(t, cookie)
			f.oldStatus.Store(http.StatusUnauthorized)
			if err := f.state.Validate(context.Background()); !errors.Is(err, auth.ErrLoginRequired) {
				t.Fatalf("缺少导出文件时未保留原登录错误: %v", err)
			}
			want := int32(0)
			if cookie != "" {
				want = 1
			}
			if f.oldCalls.Load() != want || f.newCalls.Load() != 0 || f.deviceCalls.Load() != 0 {
				t.Fatal("缺少导出文件造成了多余网络请求")
			}
		})
	}
}

func TestPortableAuthExplicitImportCanReplaceValidCookie(t *testing.T) {
	f := newPortableFixture(t, "old-token")
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.state.ImportSession(context.Background(), auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: "new-token"}); err != nil {
		t.Fatal(err)
	}
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state.Snapshot().UserID != 42 || f.state.Snapshot().AccessToken != "new-token" {
		t.Fatal("显式导入未能切换已登录账号")
	}
}
