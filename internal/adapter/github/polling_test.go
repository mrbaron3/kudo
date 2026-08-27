package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

// nextPageLink は GitHub と同じく、現在の query を保ったまま page だけを進める
// Link header を返す。query を落とすと、pagination 中に filter が外れる server を
// 模してしまい、adapter の検査が本物より緩くなる。
func nextPageLink(base string, current *url.URL, page int) string {
	values := current.Query()
	values.Set("page", fmt.Sprint(page))
	return fmt.Sprintf(`<%s%s?%s>; rel="next"`, base, current.Path, values.Encode())
}

func TestListOpenRunIssueRefsPaginatesAndKeepsOnlyClaimBranches(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "GET /repos/acme/widgets/pulls" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state = %q, want open", got)
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"number": 52, "state": "open", "head": map[string]any{"ref": "kudo/issue-19", "sha": strings.Repeat("d", 40)}},
				{"number": 53, "state": "open", "head": map[string]any{"ref": "kudo/issue-041", "sha": strings.Repeat("e", 40)}},
			})
			return
		}
		w.Header().Set("Link", nextPageLink(server.URL, r.URL, 2))
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 50, "state": "open", "head": map[string]any{"ref": "kudo/issue-7", "sha": strings.Repeat("a", 40)}},
			{"number": 51, "state": "open", "head": map[string]any{"ref": "feature/manual", "sha": strings.Repeat("b", 40)}},
		})
	}))
	t.Cleanup(server.Close)

	refs, err := testGateway(server.Client(), server.URL).ListOpenRunIssueRefs(t.Context())
	if err != nil {
		t.Fatalf("ListOpenRunIssueRefs() error = %v", err)
	}
	want := []contract.IssueRef{
		{Owner: "acme", Repository: "widgets", Number: 7},
		{Owner: "acme", Repository: "widgets", Number: 19},
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
	for index := range want {
		if refs[index] != want[index] {
			t.Fatalf("refs = %#v, want %#v", refs, want)
		}
	}
}

func TestListOpenRunIssueRefsDeduplicatesRepeatedIssues(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 50, "state": "open", "head": map[string]any{"ref": "kudo/issue-19"}},
			{"number": 51, "state": "open", "head": map[string]any{"ref": "kudo/issue-19"}},
		})
	}))
	t.Cleanup(server.Close)

	refs, err := testGateway(server.Client(), server.URL).ListOpenRunIssueRefs(t.Context())
	if err != nil {
		t.Fatalf("ListOpenRunIssueRefs() error = %v", err)
	}
	if len(refs) != 1 || refs[0].Number != 19 {
		t.Fatalf("refs = %#v, want 単一の Issue 19", refs)
	}
}

func TestListCandidateIssueRefsReturnsIdentityWithoutBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != "open" || query.Get("assignee") != "mrbaron3" || query.Get("labels") != "ai-ready" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 19, "state": "open", "body": "契約本文", "repository_url": "https://api.github.com/repos/acme/widgets"},
			{"number": 20, "state": "open", "body": "契約本文", "repository_url": "https://api.github.com/repos/acme/widgets",
				"pull_request": map[string]any{"url": "https://example.test/pull/20"}},
		})
	}))
	t.Cleanup(server.Close)

	refs, err := testGateway(server.Client(), server.URL).ListCandidateIssueRefs(t.Context(), CandidateFilter{
		Assignee: "mrbaron3", Label: "ai-ready",
	})
	if err != nil {
		t.Fatalf("ListCandidateIssueRefs() error = %v", err)
	}
	want := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 19}
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("refs = %#v, want %#v のみ", refs, want)
	}
}

func TestRemoveLabelSkipsDeleteWhenLabelIsAbsent(t *testing.T) {
	t.Parallel()

	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"name":"ai-merged"}]`)
		case http.MethodDelete:
			deletes.Add(1)
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).RemoveLabel(t.Context(), 19, "ai-ready")
	if err != nil {
		t.Fatalf("RemoveLabel() error = %v", err)
	}
	if changed || deletes.Load() != 0 {
		t.Fatalf("changed = %v, DELETE count = %d", changed, deletes.Load())
	}
}

func TestRemoveLabelDeletesOnlyTheNamedLabel(t *testing.T) {
	t.Parallel()

	var deleted atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"name":"AI-Ready"},{"name":"ai-in-progress"}]`)
		case http.MethodDelete:
			deleted.Store(r.URL.Path)
			fmt.Fprint(w, `[{"name":"ai-in-progress"}]`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).RemoveLabel(t.Context(), 19, "ai-ready")
	if err != nil {
		t.Fatalf("RemoveLabel() error = %v", err)
	}
	if !changed || deleted.Load() != "/repos/acme/widgets/issues/19/labels/ai-ready" {
		t.Fatalf("changed = %v, deleted path = %v", changed, deleted.Load())
	}
}

// 同じ label の削除が競合しても、次の reconcile は「既に無い」へ収束しなければならない。
// 404 を失敗にすると、収束済みの状態が transport failure として記録される。
func TestRemoveLabelTreatsConcurrentRemovalAsConverged(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[{"name":"ai-ready"}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Label does not exist"}`)
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).RemoveLabel(t.Context(), 19, "ai-ready")
	if err != nil {
		t.Fatalf("RemoveLabel() error = %v", err)
	}
	if changed {
		t.Fatalf("changed = %v, want false", changed)
	}
}

func TestEnsureIssueClosedIsNoOpForAlreadyClosedIssue(t *testing.T) {
	t.Parallel()

	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"number":19,"state":"closed","body":"","repository_url":"https://api.github.com/repos/acme/widgets"}`)
		case http.MethodPatch:
			patches.Add(1)
			fmt.Fprint(w, `{"number":19,"state":"closed","body":""}`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).EnsureIssueClosed(t.Context(), 19)
	if err != nil {
		t.Fatalf("EnsureIssueClosed() error = %v", err)
	}
	if changed || patches.Load() != 0 {
		t.Fatalf("changed = %v, PATCH count = %d", changed, patches.Load())
	}
}

func TestEnsureIssueClosedRecordsCompletedStateReason(t *testing.T) {
	t.Parallel()

	var posted struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"number":19,"state":"open","body":"","repository_url":"https://api.github.com/repos/acme/widgets"}`)
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"number":19,"state":"closed","body":""}`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).EnsureIssueClosed(t.Context(), 19)
	if err != nil {
		t.Fatalf("EnsureIssueClosed() error = %v", err)
	}
	if !changed || posted.State != "closed" || posted.StateReason != "completed" {
		t.Fatalf("changed = %v, posted = %#v", changed, posted)
	}
}

func TestEnsureCommentContentUpdatesTheMarkedCommentInPlace(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	stale, err := RenderComment("古い案内", marker, nil)
	if err != nil {
		t.Fatal(err)
	}
	var patched struct {
		Body string `json:"body"`
	}
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": 8, "body": stale, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/issues/comments/8":
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 8, "body": patched.Body, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			})
		case r.Method == http.MethodPost:
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":9,"body":"created"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	comment, change, err := testGateway(server.Client(), server.URL).EnsureCommentContent(t.Context(), 19, CommentRecord{
		Marker: marker,
		Body:   "新しい案内",
	})
	if err != nil {
		t.Fatalf("EnsureCommentContent() error = %v", err)
	}
	if change != CommentUpdated || comment.ID != 8 || creates.Load() != 0 {
		t.Fatalf("change = %q, comment = %#v, POST count = %d", change, comment, creates.Load())
	}
	if !strings.Contains(patched.Body, "新しい案内") {
		t.Fatalf("patched body = %q", patched.Body)
	}
}

func TestEnsureCommentContentLeavesIdenticalBodyUntouched(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	current, err := RenderComment("同じ案内", marker, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 8, "body": current, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
		}})
	}))
	t.Cleanup(server.Close)

	comment, change, err := testGateway(server.Client(), server.URL).EnsureCommentContent(t.Context(), 19, CommentRecord{
		Marker: marker,
		Body:   "同じ案内",
	})
	if err != nil {
		t.Fatalf("EnsureCommentContent() error = %v", err)
	}
	if change != CommentUnchanged || comment.ID != 8 || mutations.Load() != 0 {
		t.Fatalf("change = %q, comment = %#v, mutation count = %d", change, comment, mutations.Load())
	}
}

func TestEnsureCommentContentCreatesOnceWhenMarkerIsAbsent(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[]`)
		case http.MethodPost:
			creates.Add(1)
			var posted struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 9, "body": posted.Body, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			})
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	comment, change, err := testGateway(server.Client(), server.URL).EnsureCommentContent(t.Context(), 19, CommentRecord{
		Marker: marker,
		Body:   "案内",
	})
	if err != nil {
		t.Fatalf("EnsureCommentContent() error = %v", err)
	}
	if change != CommentCreated || comment.ID != 9 || creates.Load() != 1 {
		t.Fatalf("change = %q, comment = %#v, POST count = %d", change, comment, creates.Load())
	}
}

func TestRateLimitSnapshotFollowsResponseHeaders(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(30 * time.Minute).Truncate(time.Second).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4987")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(server.Close)

	gateway := testGateway(server.Client(), server.URL)
	if _, observed := gateway.RateLimit(); observed {
		t.Fatal("request 前に rate limit を観測済みとして返した")
	}
	if _, err := gateway.ListOpenRunIssueRefs(t.Context()); err != nil {
		t.Fatalf("ListOpenRunIssueRefs() error = %v", err)
	}
	snapshot, observed := gateway.RateLimit()
	if !observed || snapshot.Limit != 5000 || snapshot.Remaining != 4987 || !snapshot.Reset.Equal(reset) {
		t.Fatalf("snapshot = %#v, observed = %v", snapshot, observed)
	}
}

func TestRetryAfterHintPrefersRetryAfterOverRateLimitReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	failure := &TransportFailure{
		Class:          FailureSecondaryRateLimit,
		RetryAfter:     45 * time.Second,
		RateLimitReset: now.Add(20 * time.Minute),
	}
	if hint, ok := failure.RetryAfterHint(now); !ok || hint != 45*time.Second {
		t.Fatalf("RetryAfterHint() = %v, %v, want 45s, true", hint, ok)
	}
}

func TestRetryAfterHintUsesRateLimitResetWhenItIsStillAhead(t *testing.T) {
	t.Parallel()

	now := time.Now()
	failure := &TransportFailure{Class: FailureRateLimit, RateLimitReset: now.Add(90 * time.Second)}
	hint, ok := failure.RetryAfterHint(now)
	if !ok || hint < 89*time.Second || hint > 90*time.Second {
		t.Fatalf("RetryAfterHint() = %v, %v, want 約 90s, true", hint, ok)
	}
	past := &TransportFailure{Class: FailureRateLimit, RateLimitReset: now.Add(-time.Second)}
	if hint, ok := past.RetryAfterHint(now); ok {
		t.Fatalf("経過済み reset で hint = %v, %v, want false", hint, ok)
	}
}
