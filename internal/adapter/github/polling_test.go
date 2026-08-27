package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
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
				{"number": 52, "state": "open", "head": map[string]any{
					"ref": "kudo/issue-19", "sha": strings.Repeat("d", 40), "repo": ownedRepository()}},
				{"number": 53, "state": "open", "head": map[string]any{
					"ref": "kudo/issue-041", "sha": strings.Repeat("e", 40), "repo": ownedRepository()}},
			})
			return
		}
		w.Header().Set("Link", nextPageLink(server.URL, r.URL, 2))
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 50, "state": "open", "head": map[string]any{
				"ref": "kudo/issue-7", "sha": strings.Repeat("a", 40), "repo": ownedRepository()}},
			{"number": 51, "state": "open", "head": map[string]any{
				"ref": "feature/manual", "sha": strings.Repeat("b", 40), "repo": ownedRepository()}},
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
			{"number": 50, "state": "open", "head": map[string]any{"ref": "kudo/issue-19", "repo": ownedRepository()}},
			{"number": 51, "state": "open", "head": map[string]any{"ref": "kudo/issue-19", "repo": ownedRepository()}},
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

	refs, err := testGateway(server.Client(), server.URL).ListCandidateIssueRefs(t.Context(),
		workflow.CandidateFilter{Assignee: "mrbaron3", ReadyLabel: "ai-ready"})
	if err != nil {
		t.Fatalf("ListCandidateIssueRefs() error = %v", err)
	}
	want := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 19}
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("refs = %#v, want %#v のみ", refs, want)
	}
}

// 収束済みの label set では、読み取り 1 回だけで mutation を出さない。
func TestConvergeLabelsReadsOnceAndSkipsSettledMutations(t *testing.T) {
	t.Parallel()

	var gets, mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			t.Errorf("収束済みで mutation を発行した: %s %s", r.Method, r.URL.Path)
			return
		}
		gets.Add(1)
		fmt.Fprint(w, `[{"name":"ai-merged"}]`)
	}))
	t.Cleanup(server.Close)

	added, removed, err := testGateway(server.Client(), server.URL).ConvergeLabels(t.Context(), 19,
		"ai-merged", []string{"ai-ready", "ai-in-progress", "ai-needs-human"})
	if err != nil {
		t.Fatalf("ConvergeLabels() error = %v", err)
	}
	if added || len(removed) != 0 || mutations.Load() != 0 {
		t.Fatalf("added = %v, removed = %v, mutation = %d", added, removed, mutations.Load())
	}
	if gets.Load() != 1 {
		t.Fatalf("label 一覧の読み取り = %d 回, want 1", gets.Load())
	}
}

// GitHub の label identity は case-insensitive だが、DELETE の path は表記が一致しないと
// 対象を解決できない。設定値で消しにいくと、外れていないのに収束済みとして報告される。
func TestConvergeLabelsDeletesTheObservedLabelName(t *testing.T) {
	t.Parallel()

	var deleted []string
	var added []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"name":"AI-Ready"},{"name":"ai-in-progress"}]`)
		case http.MethodPost:
			var posted struct {
				Labels []string `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			added = append(added, posted.Labels...)
			fmt.Fprint(w, `[]`)
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, removed, err := testGateway(server.Client(), server.URL).ConvergeLabels(t.Context(), 19,
		"ai-merged", []string{"ai-ready", "ai-in-progress"})
	if err != nil {
		t.Fatalf("ConvergeLabels() error = %v", err)
	}
	if !changed || len(added) != 1 || added[0] != "ai-merged" {
		t.Fatalf("changed = %v, added = %v", changed, added)
	}
	want := []string{
		"/repos/acme/widgets/issues/19/labels/AI-Ready",
		"/repos/acme/widgets/issues/19/labels/ai-in-progress",
	}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	if !slices.Equal(removed, []string{"AI-Ready", "ai-in-progress"}) {
		t.Fatalf("removed = %v", removed)
	}
}

// 同じ label の削除が競合しても、次の reconcile は「既に無い」へ収束しなければならない。
// 404 を失敗にすると、収束済みの状態が transport failure として記録される。
func TestConvergeLabelsTreatsConcurrentRemovalAsConverged(t *testing.T) {
	t.Parallel()

	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if deleted.Load() {
				fmt.Fprint(w, `[{"name":"ai-merged"}]`)
				return
			}
			fmt.Fprint(w, `[{"name":"ai-ready"},{"name":"ai-merged"}]`)
		case http.MethodDelete:
			// 並行 reconcile が先に外した状態を模す。再観測では label が消えている。
			deleted.Store(true)
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Label does not exist"}`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	added, removed, err := testGateway(server.Client(), server.URL).ConvergeLabels(t.Context(), 19,
		"ai-merged", []string{"ai-ready"})
	if err != nil {
		t.Fatalf("ConvergeLabels() error = %v", err)
	}
	if added || len(removed) != 0 {
		t.Fatalf("added = %v, removed = %v, want 変化なし", added, removed)
	}
}

// label 削除の 404 は「付いていない」と「repository / Issue が見えない」を区別しない。
// 再観測で label が残っていれば、収束ではなく transport failure である。
func TestConvergeLabelsReportsUnresolved404AsTransportFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"name":"ai-ready"},{"name":"ai-merged"}]`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	_, _, err := testGateway(server.Client(), server.URL).ConvergeLabels(t.Context(), 19,
		"ai-merged", []string{"ai-ready"})
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureNotFound {
		t.Fatalf("error = %v, want 分類済みの not_found", err)
	}
}

// fork に同名 branch を作った PR は kudo PR ではない。claim の排他は configured
// repository 上の ref create で成立しており、外部 branch はその identity を満たさない。
func TestListOpenRunIssueRefsExcludesForkHeadBranches(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 60, "state": "open", "head": map[string]any{
				"ref": "kudo/issue-19", "repo": map[string]any{"full_name": "outsider/widgets"}}},
			{"number": 61, "state": "open", "head": map[string]any{
				"ref": "kudo/issue-24", "repo": ownedRepository()}},
			{"number": 62, "state": "open", "head": map[string]any{"ref": "kudo/issue-31"}},
		})
	}))
	t.Cleanup(server.Close)

	refs, err := testGateway(server.Client(), server.URL).ListOpenRunIssueRefs(t.Context())
	if err != nil {
		t.Fatalf("ListOpenRunIssueRefs() error = %v", err)
	}
	if len(refs) != 1 || refs[0].Number != 24 {
		t.Fatalf("refs = %#v, want configured repository の head だけ", refs)
	}
}

// 複数 label は AND 条件で送る。組合せで絞れないと、完了済み Issue 全体のような
// 増え続ける集合を毎 cycle 列挙することになる。
func TestListLabeledIssueRefsSendsMultipleLabelsAsAConjunction(t *testing.T) {
	t.Parallel()

	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("labels")
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(server.Close)

	if _, err := testGateway(server.Client(), server.URL).ListLabeledIssueRefs(t.Context(),
		[]string{"ai-merged", "ai-ready"}); err != nil {
		t.Fatalf("ListLabeledIssueRefs() error = %v", err)
	}
	if query != "ai-merged,ai-ready" {
		t.Fatalf("labels = %q, want ai-merged,ai-ready", query)
	}
}

func TestListLabeledIssueRefsRejectsAmbiguousLabelInput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("検証前に request を送った: %s", r.URL.String())
	}))
	t.Cleanup(server.Close)

	gateway := testGateway(server.Client(), server.URL)
	for name, labels := range map[string][]string{
		"空":       nil,
		"空 label": {"ai-merged", " "},
		"区切り文字混入": {"ai-merged,ai-ready"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gateway.ListLabeledIssueRefs(t.Context(), labels); err == nil {
				t.Fatalf("不正な label 条件 %v を受理した", labels)
			}
		})
	}
}

// 現在の label set では復元できない事実（誰がいつ付けたか）を運ぶ。
func TestListIssueLabelEventsKeepsOnlyLabelChangesWithTheirActor(t *testing.T) {
	t.Parallel()

	occurred := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/issues/19/events" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "event": "labeled", "created_at": occurred,
				"actor": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
				"label": map[string]any{"name": "ai-merged"}},
			{"id": 2, "event": "closed", "created_at": occurred,
				"actor": map[string]any{"id": 101, "login": "kudo-actor[bot]"}},
			{"id": 3, "event": "unlabeled", "created_at": occurred,
				"actor": map[string]any{"id": 7, "login": "mrbaron3"},
				"label": map[string]any{"name": "ai-merged"}},
		})
	}))
	t.Cleanup(server.Close)

	events, err := testGateway(server.Client(), server.URL).ListIssueLabelEvents(t.Context(), 19)
	if err != nil {
		t.Fatalf("ListIssueLabelEvents() error = %v", err)
	}
	want := []IssueLabelEvent{
		{ID: 1, Label: "ai-merged", Added: true, Actor: Actor{ID: 101, Login: "kudo-actor[bot]"}, OccurredAt: occurred},
		{ID: 3, Label: "ai-merged", Actor: Actor{ID: 7, Login: "mrbaron3"}, OccurredAt: occurred},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func ownedRepository() map[string]any {
	return map[string]any{"full_name": "acme/widgets"}
}

// 追加と削除に同じ label を渡す呼び出しは収束先が定義できない。
func TestConvergeLabelsRejectsContradictoryRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("検証前に request を送った: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)

	if _, _, err := testGateway(server.Client(), server.URL).ConvergeLabels(t.Context(), 19,
		"ai-merged", []string{"AI-Merged"}); err == nil {
		t.Fatal("同じ label の追加と削除を受理した")
	}
}

// close 済みの Issue も列挙する。merge completion の投影が close の後で失敗した Issue は、
// closed・merged PR・進行中 label という組合せでしか観測できない。
func TestListLabeledIssueRefsCoversClosedIssuesAndExcludesPullRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != "all" || query.Get("labels") != "ai-in-progress" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		if query.Has("assignee") {
			t.Errorf("Kudo 所有 label の列挙で assignee を絞った: %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 19, "state": "closed", "body": "", "repository_url": "https://api.github.com/repos/acme/widgets"},
			{"number": 52, "state": "open", "body": "", "repository_url": "https://api.github.com/repos/acme/widgets",
				"pull_request": map[string]any{"url": "https://example.test/pull/52"}},
		})
	}))
	t.Cleanup(server.Close)

	refs, err := testGateway(server.Client(), server.URL).ListLabeledIssueRefs(t.Context(), []string{"ai-in-progress"})
	if err != nil {
		t.Fatalf("ListLabeledIssueRefs() error = %v", err)
	}
	want := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 19}
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("refs = %#v, want %#v のみ", refs, want)
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
