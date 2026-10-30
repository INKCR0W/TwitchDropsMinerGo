package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/browserlogin"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/runtime"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestRunStopsWhenLoginPromptCannotBeWritten(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"-browser", "missing-test-browser"}, failingWriter{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "输出登录提示失败") {
		t.Fatalf("unexpected output failure: code=%d stderr=%q", code, stderr.String())
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"-output", "session.json", "-timeout", "2m"})
	if err != nil || o.Timeout != 2*time.Minute || o.Output != "session.json" {
		t.Fatal("options lost")
	}
	for _, args := range [][]string{{"-timeout", "0s"}, {"-output", ""}, {"unexpected"}} {
		if _, err := parseArgs(args); err == nil {
			t.Fatal("invalid args accepted")
		}
	}
}

func TestLoginAndServerShareDefaultSessionPath(t *testing.T) {
	for _, args := range [][]string{nil, {"-runtime-dir", t.TempDir()}} {
		o, err := parseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := runtime.ResolveLayout(o.RuntimeDir)
		if err != nil {
			t.Fatal(err)
		}
		if o.Output != layout.SessionFile {
			t.Fatalf("登录导出与服务端默认导入路径不一致: %q != %q", o.Output, layout.SessionFile)
		}
	}
	path := filepath.Join(t.TempDir(), "custom-session.json")
	o, err := parseArgs([]string{"-runtime-dir", "another-runtime", "-output", path})
	if err != nil || o.Output != path {
		t.Fatalf("自定义输出路径失效: %q %v", o.Output, err)
	}
}

func TestRunExportsBrowserSessionWithoutLocalValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	var output, stderr bytes.Buffer
	called := false
	login := func(ctx context.Context, options browserlogin.Options) (string, error) {
		called = true
		if _, ok := ctx.Deadline(); !ok || options.BrowserPath != "test-browser" || options.Proxy != "http://127.0.0.1:12345" {
			t.Fatal("登录选项没有传入浏览器")
		}
		return "test-browser-token", nil
	}
	code := runWithLogin([]string{"-output", path, "-browser", "test-browser", "-proxy", "http://127.0.0.1:12345"}, &output, &stderr, login)
	if code != 0 || !called || stderr.Len() != 0 {
		t.Fatalf("浏览器授权未导出: code=%d error=%s", code, &stderr)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session, readErr := auth.ReadSession(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("无法读取会话: %v %v", readErr, closeErr)
	}
	if session.Version != auth.SessionVersion || session.ClientID != httpclient.AndroidAppClient.ClientID || session.AccessToken != "test-browser-token" {
		t.Fatal("导出会话与浏览器授权不一致")
	}
	if !strings.Contains(output.String(), "导入并校验") || strings.Contains(output.String(), "user_id") || strings.Contains(output.String(), session.AccessToken) {
		t.Fatal("导出提示误报校验成功或泄露令牌")
	}
}

func TestRunDoesNotExportFailedOrEmptyAuthorization(t *testing.T) {
	for _, test := range []struct {
		name, token string
		err         error
	}{
		{name: "cancelled", err: context.Canceled},
		{name: "empty token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			before := []byte("existing session")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			code := runWithLogin([]string{"-output", path}, &output, &stderr, func(context.Context, browserlogin.Options) (string, error) {
				return test.token, test.err
			})
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if code != 1 || stderr.Len() == 0 || !bytes.Equal(before, after) || strings.Contains(output.String(), "授权已完成") {
				t.Fatal("失败授权覆盖了已有会话或误报成功")
			}
		})
	}
}

func TestRunReportsSessionSaveFailure(t *testing.T) {
	var output, stderr bytes.Buffer
	code := runWithLogin([]string{"-output", t.TempDir()}, &output, &stderr, func(context.Context, browserlogin.Options) (string, error) {
		return "test-browser-token", nil
	})
	if code != 1 || !strings.Contains(stderr.String(), "保存授权会话失败") || strings.Contains(output.String(), "授权已完成") {
		t.Fatalf("会话保存错误未报告: code=%d error=%s", code, &stderr)
	}
}

type resultFailingWriter struct{ writes int }

func (w *resultFailingWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("output unavailable")
	}
	return len(data), nil
}

func TestRunReportsResultOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	var stderr bytes.Buffer
	code := runWithLogin([]string{"-output", path}, &resultFailingWriter{}, &stderr, func(context.Context, browserlogin.Options) (string, error) {
		return "test-browser-token", nil
	})
	if code != 1 || !strings.Contains(stderr.String(), "输出授权结果失败") {
		t.Fatalf("授权结果输出错误未报告: code=%d error=%s", code, &stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("输出失败删除了已导出的会话")
	}
}
