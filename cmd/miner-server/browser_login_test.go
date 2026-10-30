package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/runtime"
)

type authorizationInput struct {
	output *bytes.Buffer
	reader *strings.Reader
}

func (input *authorizationInput) Read(data []byte) (int, error) {
	if input.reader == nil {
		lines := strings.Split(input.output.String(), "\n")
		if len(lines) < 2 {
			return 0, errors.New("missing authorization prompt")
		}
		address, err := url.Parse(lines[1])
		if err != nil {
			return 0, err
		}
		redirect, err := url.ParseQuery(address.Query().Get("redirect_params"))
		if err != nil {
			return 0, err
		}
		input.reader = strings.NewReader("https://www.twitch.tv/#access_token=test-token&token_type=bearer&state=" + redirect.Get("state") + "\n")
	}
	return input.reader.Read(data)
}

func TestInteractiveLoginImportsAndroidSessionWithoutExchangeFile(t *testing.T) {
	var output bytes.Buffer
	input := &authorizationInput{output: &output}
	called := false
	err := interactiveLogin(context.Background(), input, &output, sessionImportFunc(func(ctx context.Context, session auth.Session) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute || time.Until(deadline) < 50*time.Second {
			t.Error("import validation did not receive its own timeout")
		}
		if session.Version != auth.SessionVersion || session.ClientID != httpclient.AndroidAppClient.ClientID || session.AccessToken != "test-token" {
			t.Error("authorization was not imported as the Android session")
		}
		return nil
	}))
	if err != nil || !called || !strings.Contains(output.String(), "Cookie 已保存") || strings.Contains(output.String(), "test-token") {
		t.Fatal("authorization failed or callback leaked")
	}
}

func TestInteractiveLoginDoesNotReportSuccessBeforeImport(t *testing.T) {
	var output bytes.Buffer
	err := interactiveLogin(context.Background(), &authorizationInput{output: &output}, &output, sessionImportFunc(func(context.Context, auth.Session) error {
		return errors.New("validation unavailable")
	}))
	if err == nil || strings.Contains(output.String(), "登录成功") || strings.Contains(output.String(), "test-token") {
		t.Fatal("failed import was reported as successful or callback leaked")
	}
}

func TestBrowserLoginRejectsInvalidCallbackWithoutChangingCookies(t *testing.T) {
	layout, err := runtime.ResolveLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"schema_version":1,"cookies":{}}`)
	if err := os.WriteFile(layout.CookiesFile, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var output, logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	code := runBrowserLogin(context.Background(), layout, config.DefaultSettings(), logger, strings.NewReader("https://evil.test/#access_token=secret\n"), &output)
	after, err := os.ReadFile(layout.CookiesFile)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !bytes.Equal(before, after) || strings.Contains(output.String()+logs.String(), "secret") {
		t.Fatal("invalid login changed cookies or leaked callback")
	}
}

func TestReadManualCallbackBoundsRedirectedInput(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		fails             bool
	}{
		{"newline", "https://www.twitch.tv/#test\r\nignored", "https://www.twitch.tv/#test", false},
		{"eof", "https://www.twitch.tv/#test", "https://www.twitch.tv/#test", false},
		{"too large", strings.Repeat("x", maxManualCallbackBytes+1), "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := readManualCallback(context.Background(), strings.NewReader(test.input))
			if (err != nil) != test.fails || got != test.want {
				t.Fatal("redirected input bounds or line handling failed")
			}
		})
	}
}

func TestReadManualCallbackCancelsBlockedInput(t *testing.T) {
	input, output := io.Pipe()
	defer func() { _ = input.Close(); _ = output.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if callback, err := readManualCallback(ctx, input); !errors.Is(err, context.DeadlineExceeded) || callback != "" {
		t.Fatal("blocked input did not stop on timeout")
	}
}

func TestHiddenCallbackHandlesPasteAndControlC(t *testing.T) {
	callback, err := readHiddenCallback(strings.NewReader("https://www.twitch.tv/#access_token=test-token\r"))
	if err != nil || callback != "https://www.twitch.tv/#access_token=test-token" {
		t.Fatal("hidden terminal input lost pasted callback")
	}
	if callback, err := readHiddenCallback(strings.NewReader("partial-secret\x03")); !errors.Is(err, context.Canceled) || callback != "" {
		t.Fatal("Control+C did not discard the terminal input")
	}
}

func TestStartupLoginDoesNotContinueAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := ensureStartupLogin(ctx, true, func(context.Context) error {
		cancel()
		return auth.ErrLoginRequired
	}, func(context.Context) error {
		t.Fatal("cancelled startup asked for browser login")
		return nil
	}, logger)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("startup cancellation lost")
	}
}

func TestStartupLoginPreservesBackgroundAndTransientErrorBehavior(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		name        string
		interactive bool
		validateErr error
		validates   bool
		logins      bool
	}{
		{"background", false, auth.ErrLoginRequired, false, false},
		{"valid cookie", true, nil, true, false},
		{"missing login", true, auth.ErrLoginRequired, true, true},
		{"transient network", true, errors.New("network unavailable"), true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			validated, loggedIn := false, false
			err := ensureStartupLogin(context.Background(), test.interactive, func(context.Context) error {
				validated = true
				return test.validateErr
			}, func(context.Context) error {
				loggedIn = true
				return nil
			}, logger)
			if err != nil || validated != test.validates || loggedIn != test.logins {
				t.Fatal("startup login changed background or retry behavior")
			}
		})
	}
}

func TestLoginArgsAreExclusive(t *testing.T) {
	options, err := parseArgs([]string{"-login", "-runtime-dir", "test-runtime"})
	if err != nil || !options.Login || options.RuntimeDir != "test-runtime" {
		t.Fatal("login options lost")
	}
	for _, args := range [][]string{{"-login", "-healthcheck"}, {"-login", "-import-session", "-"}} {
		if _, err := parseArgs(args); err == nil {
			t.Fatal("conflicting modes accepted")
		}
	}
}
