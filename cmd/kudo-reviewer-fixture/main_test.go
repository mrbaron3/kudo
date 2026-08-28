package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	for _, value := range []string{"Usage:", "valid", "digest-mismatch", "missing-required-input", "missing-marker"} {
		if !strings.Contains(stdout.String(), value) {
			t.Fatalf("help = %q, want %q", stdout.String(), value)
		}
	}
}

func TestRunRejectsMissingDevelopmentCredentialBeforeGitHubIO(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"--repository", "acme/reviewer-fixtures",
		"--issue", "71",
		"--comment-author-id", "101",
		"--check-run-app-id", "202",
	}, func(string) (string, bool) { return "", false }, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "KUDO_FIXTURE_GITHUB_TOKEN") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsAmbiguousRepositoryAndFixtureCase(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"repository": {
			"--repository", "acme/widgets/extra", "--issue", "71",
			"--comment-author-id", "101", "--check-run-app-id", "202",
		},
		"fixture": {
			"--repository", "acme/widgets", "--issue", "71", "--case", "unknown",
			"--comment-author-id", "101", "--check-run-app-id", "202",
		},
	} {
		name, args := name, args
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(args, func(string) (string, bool) { return "secret", true }, &stdout, &stderr)
			if code != 2 || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
