package workflow

import "testing"

func TestIssueNumberFromBranchRoundTripsClaimBranchNames(t *testing.T) {
	t.Parallel()

	for _, issue := range []int64{1, 19, 1234567} {
		number, ok := IssueNumberFromBranch(IssueBranchName(issue))
		if !ok || number != issue {
			t.Fatalf("IssueNumberFromBranch(%q) = %d, %v, want %d, true",
				IssueBranchName(issue), number, ok, issue)
		}
	}
}

func TestIssueNumberFromBranchRejectsNonClaimBranches(t *testing.T) {
	t.Parallel()

	for name, branch := range map[string]string{
		"別 namespace":      "feature/issue-19",
		"prefix のみ":        "kudo/issue-",
		"非数値":              "kudo/issue-abc",
		"leading zero":     "kudo/issue-019",
		"符号付き":             "kudo/issue-+19",
		"負数":               "kudo/issue--19",
		"ゼロ":               "kudo/issue-0",
		"余分な path segment": "kudo/issue-19/head",
		"大文字":              "KUDO/ISSUE-19",
		"前後の空白":            " kudo/issue-19",
		"int64 を超える桁":      "kudo/issue-99999999999999999999",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if number, ok := IssueNumberFromBranch(branch); ok {
				t.Fatalf("IssueNumberFromBranch(%q) = %d, true, want false", branch, number)
			}
		})
	}
}
