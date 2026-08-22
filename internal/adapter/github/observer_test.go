package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestGetIssueRejectsConfiguredRepositoryMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":160,"number":16,"state":"open","title":"wrong repository","body":"body","repository_url":"https://api.github.com/repos/other/widgets"}`)
	}))
	t.Cleanup(server.Close)

	_, err := testGateway(server.Client(), server.URL).getIssue(t.Context(), 16)
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureInvalidResponse {
		t.Fatalf("error = %v, want invalid response", err)
	}
}

func TestGetIssueRejectsMissingRawBodyField(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":160,"number":16,"state":"open","title":"missing body"}`)
	}))
	t.Cleanup(server.Close)

	_, err := testGateway(server.Client(), server.URL).getIssue(t.Context(), 16)
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureInvalidResponse {
		t.Fatalf("error = %v, want invalid response", err)
	}
}

func TestObserveIssueSeparatesExactBodyAndBuildsSnapshot(t *testing.T) {
	t.Parallel()

	exactBody := "first line\r\nsecond line  \n"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertRequestHeaders(t, r)
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/16":
			fmt.Fprintf(w, `{"id":160,"node_id":"issue-node","number":16,"state":"open","title":"Gateway","body":%q,"repository_url":%q,"user":{"id":1,"login":"author"},"assignees":[{"id":2,"login":"worker"}],"labels":[{"name":"ai-ready","color":"1d76db"}],"created_at":"2026-08-20T00:00:00Z","updated_at":"2026-08-21T00:00:00Z"}`, exactBody, server.URL+"/repos/acme/widgets")
		case "/repos/acme/widgets/issues/16/parent":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Issue does not have a parent"}`)
		case "/repos/acme/widgets/issues/16/sub_issues":
			if page == "2" {
				fmt.Fprint(w, `[{"id":162,"number":18,"state":"closed","title":"sub two","repository_url":"`+server.URL+`/repos/acme/widgets"}]`)
				return
			}
			w.Header().Set("Link", `<`+server.URL+`/repos/acme/widgets/issues/16/sub_issues?per_page=100&page=2>; rel="next"`)
			fmt.Fprint(w, `[{"id":161,"number":17,"state":"open","title":"sub one","repository_url":"`+server.URL+`/repos/acme/widgets"}]`)
		case "/repos/acme/widgets/issues/16/comments":
			fmt.Fprint(w, `[{"id":1001,"node_id":"comment-node","body":"issue comment","user":{"id":3,"login":"coordinator"},"created_at":"2026-08-21T01:00:00Z","updated_at":"2026-08-21T01:00:00Z"}]`)
		case "/repos/acme/widgets/branches/kudo/issue-16":
			fmt.Fprint(w, `{"name":"kudo/issue-16","protected":true,"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		case "/repos/acme/widgets/pulls":
			if r.URL.Query().Get("head") != "acme:kudo/issue-16" || r.URL.Query().Get("state") != "all" {
				t.Errorf("pull query = %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"id":700,"node_id":"pr-node","number":7,"state":"open","draft":true,"title":"issue 16","body":"draft body","user":{"id":2,"login":"worker"},"head":{"ref":"kudo/issue-16","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"cccccccccccccccccccccccccccccccccccccccc"},"created_at":"2026-08-21T02:00:00Z","updated_at":"2026-08-21T03:00:00Z"}]`)
		case "/repos/acme/widgets/issues/7/comments":
			fmt.Fprint(w, `[{"id":1002,"body":"review comment","user":{"id":4,"login":"reviewer"},"created_at":"2026-08-21T04:00:00Z","updated_at":"2026-08-21T04:00:00Z"}]`)
		case "/repos/acme/widgets/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs":
			fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":900,"node_id":"check-node","name":"kudo/test-validity","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"success","app":{"id":44,"slug":"kudo-reviewer","name":"Kudo Reviewer"},"output":{"title":"approved","summary":"summary","text":"machine"},"started_at":"2026-08-21T04:00:00Z","completed_at":"2026-08-21T04:01:00Z"}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	snapshot, err := testGateway(server.Client(), server.URL).ObserveIssue(t.Context(), 16)
	if err != nil {
		t.Fatalf("ObserveIssue() error = %v", err)
	}
	if string(snapshot.RawBody) != exactBody {
		t.Fatalf("RawBody = %q, want exact %q", snapshot.RawBody, exactBody)
	}
	if snapshot.Issue.Number != 16 || snapshot.Issue.Title != "Gateway" || snapshot.Issue.IsPullRequest {
		t.Fatalf("Issue = %#v", snapshot.Issue)
	}
	if got := snapshot.Issue.Assignees; len(got) != 1 || got[0].Login != "worker" {
		t.Fatalf("Assignees = %#v", got)
	}
	if got := snapshot.Issue.Labels; len(got) != 1 || got[0].Name != "ai-ready" {
		t.Fatalf("Labels = %#v", got)
	}
	if snapshot.Parent != nil {
		t.Fatalf("Parent = %#v, want nil", snapshot.Parent)
	}
	if got := []int64{snapshot.SubIssues[0].Number, snapshot.SubIssues[1].Number}; !slices.Equal(got, []int64{17, 18}) {
		t.Fatalf("SubIssues = %v", got)
	}
	if snapshot.Branch == nil || snapshot.Branch.Name != "kudo/issue-16" || !snapshot.Branch.Protected {
		t.Fatalf("Branch = %#v", snapshot.Branch)
	}
	if len(snapshot.IssueComments) != 1 || string(snapshot.IssueComments[0].Body) != "issue comment" {
		t.Fatalf("IssueComments = %#v", snapshot.IssueComments)
	}
	if len(snapshot.PullRequests) != 1 {
		t.Fatalf("PullRequests = %#v", snapshot.PullRequests)
	}
	pull := snapshot.PullRequests[0]
	if pull.Number != 7 || pull.Head.SHA != strings.Repeat("a", 40) || len(pull.Comments) != 1 || len(pull.CheckRuns) != 1 {
		t.Fatalf("PullRequest = %#v", pull)
	}
	if pull.CheckRuns[0].App.Slug != "kudo-reviewer" {
		t.Fatalf("CheckRun app = %#v", pull.CheckRuns[0].App)
	}
}

func TestListCandidateIssuesFollowsLinkAndExcludesPullRequestsAndDuplicates(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertRequestHeaders(t, r)
		if r.URL.Path != "/repos/acme/widgets/issues" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"number":2,"state":"open","title":"second","body":"body two"},{"id":3,"number":3,"state":"open","title":"pull","body":"ignored","pull_request":{"url":"pull"}},{"id":1,"number":1,"state":"open","title":"duplicate","body":"body one"}]`)
			return
		}
		if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("assignee") != "worker" || r.URL.Query().Get("labels") != "ai-ready" {
			t.Errorf("candidate query = %q", r.URL.RawQuery)
		}
		w.Header().Set("Link", `<`+server.URL+`/repos/acme/widgets/issues?state=open&assignee=worker&labels=ai-ready&per_page=100&page=2>; rel="next"`)
		fmt.Fprint(w, `[{"id":1,"number":1,"state":"open","title":"first","body":"body one"}]`)
	}))
	t.Cleanup(server.Close)

	issues, err := testGateway(server.Client(), server.URL).ListCandidateIssues(t.Context(), CandidateFilter{
		Assignee: "worker",
		Label:    "ai-ready",
	})
	if err != nil {
		t.Fatalf("ListCandidateIssues() error = %v", err)
	}
	if len(issues) != 2 || issues[0].Issue.Number != 1 || issues[1].Issue.Number != 2 {
		t.Fatalf("issues = %#v", issues)
	}
	if string(issues[1].RawBody) != "body two" {
		t.Fatalf("second RawBody = %q", issues[1].RawBody)
	}
}

func TestReadContentPreservesDecodedBytes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/contents/docs/spec.md" || r.URL.Query().Get("ref") != "deadbeef" {
			t.Fatalf("request URL = %s", r.URL.String())
		}
		fmt.Fprint(w, `{"type":"file","path":"docs/spec.md","sha":"blob-sha","encoding":"base64","content":"YQ0KYgo="}`)
	}))
	t.Cleanup(server.Close)

	content, err := testGateway(server.Client(), server.URL).ReadContent(t.Context(), "docs/spec.md", "deadbeef")
	if err != nil {
		t.Fatalf("ReadContent() error = %v", err)
	}
	if string(content.Data) != "a\r\nb\n" || content.Path != "docs/spec.md" || content.SHA != "blob-sha" {
		t.Fatalf("content = %#v", content)
	}
}

func assertRequestHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer actor-token" {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q", r.Header.Get("Accept"))
	}
	if r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
		t.Errorf("X-GitHub-Api-Version = %q", r.Header.Get("X-GitHub-Api-Version"))
	}
}

func pageLink(base string, path string, page int) string {
	values := url.Values{"page": {fmt.Sprint(page)}, "per_page": {"100"}}
	return fmt.Sprintf(`<%s%s?%s>; rel="next"`, base, path, values.Encode())
}
