package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/runtime"
)

func TestSessionImportRejectsMalformedInputWithoutChangingCookies(t *testing.T) {
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
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if code := runSessionImport(layout, config.DefaultSettings(), logger, "-", strings.NewReader(`{"access_token":"not-a-valid-session"}`), &output); code != 1 {
		t.Fatalf("导入无效会话返回 %d", code)
	}
	after, err := os.ReadFile(layout.CookiesFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || output.Len() != 0 {
		t.Fatal("无效导入更改了已有会话或输出了凭据")
	}
}

func TestSessionImportArgs(t *testing.T) {
	options, err := parseArgs([]string{"-import-session", "-"})
	if err != nil || options.ImportSession != "-" {
		t.Fatalf("导入参数解析失败: %v", err)
	}
	if _, err := parseArgs([]string{"-healthcheck", "-import-session", "-"}); err == nil {
		t.Fatal("应拒绝混用导入和健康检查")
	}
}
