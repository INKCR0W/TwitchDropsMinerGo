package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
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
