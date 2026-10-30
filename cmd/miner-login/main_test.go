package main

import (
	"testing"
	"time"
)

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
