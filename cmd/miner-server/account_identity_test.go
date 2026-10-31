package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestNoninteractiveStartupValidatesBeforeOpeningAccountProgress(t *testing.T) {
	for _, cookie := range []string{"old-token", ""} {
		t.Run(cookie, func(t *testing.T) {
			f := newPortableFixture(t, cookie)
			wantUser := int64(7)
			if cookie == "" {
				writePortableSession(t, f.layout.SessionFile)
				wantUser = 42
			}
			if err := ensureStartupLogin(context.Background(), false, f.state.Validate, nil, nil); err != nil {
				t.Fatal(err)
			}
			if f.state.Snapshot().UserID != 0 {
				t.Fatal("此边界应尚未执行认证")
			}
			stores, err := initializeAccountProgress(context.Background(), f.layout, f.state)
			if err != nil {
				t.Fatalf("非交互启动无法初始化账号存储: %v", err)
			}
			if f.state.Snapshot().UserID != wantUser {
				t.Fatal("未使用验证后的身份")
			}
			now := time.Now().UTC()
			if err := stores.watch.Record("campaign", "drop", 1, now.Add(time.Hour), now); err != nil {
				t.Fatal(err)
			}
			files, err := f.layout.ProgressFilesForUser(wantUser)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(files.ProgressFile); err != nil {
				t.Fatal("记录没有写入对应账号文件")
			}
		})
	}
}

func TestPortableBoundUserRejectsAccountSwitchBeforeConsumingExport(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "invalidate"
		if clear {
			name = "clear"
		}
		t.Run(name, func(t *testing.T) { testPortableBoundUserRejectsAccountSwitch(t, clear) })
	}
}

func testPortableBoundUserRejectsAccountSwitch(t *testing.T, clear bool) {
	t.Helper()
	f := newPortableFixture(t, "old-token")
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.BindUser(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.layout.CookiesFile)
	if err != nil {
		t.Fatal(err)
	}
	if clear {
		f.state.Clear()
	} else {
		f.state.Invalidate()
	}
	f.oldStatus.Store(http.StatusUnauthorized)
	writePortableSession(t, f.layout.SessionFile)
	if err := f.state.Validate(context.Background()); err == nil {
		t.Fatal("运行期自动导入切换了账号")
	}
	if f.state.Snapshot().UserID == 42 {
		t.Fatal("拒绝前已切换到B")
	}
	after, err := os.ReadFile(f.layout.CookiesFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("拒绝跨账号导入前已改cookie")
	}
	if _, err := os.Stat(f.layout.SessionFile); err != nil {
		t.Fatal("跨账号交换文件未保留")
	}
}

func TestPortableBoundUserAllowsSameAccountRenewal(t *testing.T) {
	f := newPortableFixture(t, "old-token")
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.BindUser(); err != nil {
		t.Fatal(err)
	}
	f.state.Invalidate()
	f.oldStatus.Store(http.StatusUnauthorized)
	f.newUserID.Store(7)
	writePortableSession(t, f.layout.SessionFile)
	if err := f.state.Validate(context.Background()); err != nil {
		t.Fatalf("同账号续期失败: %v", err)
	}
	if snapshot := f.state.Snapshot(); snapshot.UserID != 7 || snapshot.AccessToken != "new-token" {
		t.Fatal("续期没有保留账号并更新凭据")
	}
	if _, err := os.Stat(f.layout.SessionFile); !os.IsNotExist(err) {
		t.Fatal("成功续期未消耗交换文件")
	}
}

func TestAccountProgressStartupAuthenticationFailureDoesNotCreateAccountFiles(t *testing.T) {
	for _, mode := range []string{"network", "login-required", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := newPortableFixture(t, "old-token")
			if mode == "network" {
				f.oldStatus.Store(http.StatusServiceUnavailable)
			} else {
				f.oldStatus.Store(http.StatusUnauthorized)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if _, err := initializeAccountProgress(ctx, f.layout, f.state); err == nil {
				t.Fatal("认证失败应阻止初始化账号存储")
			}
			entries, err := os.ReadDir(f.layout.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() == "accounts" || entry.Name() == "progress.json" || entry.Name() == "rewards.json" {
					t.Fatalf("认证失败创建了进度路径: %s", entry.Name())
				}
			}
		})
	}
}
