package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, []string{"help"}, []string{"--help"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) code = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("run(%q) stdout = %q, want usage", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%q) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(version) code = %d, want 0", code)
	}
	if got, want := stdout.String(), version+"\n"; got != want {
		t.Errorf("run(version) stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("run(version) stderr = %q, want empty", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(unknown) code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(unknown) stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command "unknown"`) {
		t.Errorf("run(unknown) stderr = %q, want command error", stderr.String())
	}
}
