package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEnsureCommentFindsMarkerAcrossPages(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	existingBody, err := RenderComment("既存の記録", marker, nil)
	if err != nil {
		t.Fatal(err)
	}
	var creates atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/issues/7/comments":
			if r.URL.Query().Get("page") == "2" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 2, "body": existingBody}})
				return
			}
			w.Header().Set("Link", pageLink(server.URL, r.URL.Path, 2))
			fmt.Fprint(w, `[{"id":1,"body":"human comment"}]`)
		case "POST /repos/acme/widgets/issues/7/comments":
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":3,"body":"created"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)

	comment, created, err := testGateway(server.Client(), server.URL).EnsureComment(t.Context(), 7, CommentRecord{
		Marker: marker,
		Body:   "新しい表示内容",
	})
	if err != nil {
		t.Fatalf("EnsureComment() error = %v", err)
	}
	if created || creates.Load() != 0 || comment.ID != 2 || string(comment.Body) != existingBody {
		t.Fatalf("comment = %#v, created = %v, POST count = %d", comment, created, creates.Load())
	}
}

func TestEnsureCommentCreatesRenderedRecordWhenMissing(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	block := validMachineBlock()
	var posted struct {
		Body string `json:"body"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[]`)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "body": posted.Body})
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	comment, created, err := testGateway(server.Client(), server.URL).EnsureComment(t.Context(), 7, CommentRecord{
		Marker:       marker,
		Body:         "新しい記録",
		MachineBlock: &block,
	})
	if err != nil {
		t.Fatalf("EnsureComment() error = %v", err)
	}
	if !created || comment.ID != 3 {
		t.Fatalf("comment = %#v, created = %v", comment, created)
	}
	parsed, err := ParseRecordSurface(posted.Body)
	if err != nil || parsed.Marker != marker || parsed.MachineBlock == nil {
		t.Fatalf("posted surface = %q, parsed = %#v, error = %v", posted.Body, parsed, err)
	}
}

func TestEnsureCheckRunFindsMarkerBeforeCreate(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	encoded, err := EncodeMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	var creates atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs":
			if r.URL.Query().Get("page") == "2" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"total_count": 1,
					"check_runs": []map[string]any{{
						"id": 9, "name": "kudo/test-validity", "head_sha": marker.Head,
						"status": "completed", "conclusion": "success",
						"output": map[string]any{"title": "done", "summary": "done", "text": encoded},
					}},
				})
				return
			}
			w.Header().Set("Link", pageLink(server.URL, r.URL.Path, 2))
			fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
		case "POST /repos/acme/widgets/check-runs":
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":10}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)

	run, created, err := testGateway(server.Client(), server.URL).EnsureCheckRun(t.Context(), CheckRunRecord{
		Marker:     marker,
		Name:       "kudo/test-validity",
		HeadSHA:    marker.Head,
		Conclusion: "success",
		Title:      "review complete",
		Summary:    "approved",
	})
	if err != nil {
		t.Fatalf("EnsureCheckRun() error = %v", err)
	}
	if created || creates.Load() != 0 || run.ID != 9 {
		t.Fatalf("check run = %#v, created = %v, POST count = %d", run, created, creates.Load())
	}
}

func TestEnsureCheckRunCreatesCompletedRecordWhenMissing(t *testing.T) {
	t.Parallel()

	marker := validMarker()
	block := validMachineBlock()
	var posted struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Output     struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":10,"name":"kudo/test-validity","head_sha":"`+marker.Head+`","status":"completed","conclusion":"success"}`)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	run, created, err := testGateway(server.Client(), server.URL).EnsureCheckRun(t.Context(), CheckRunRecord{
		Marker:       marker,
		Name:         "kudo/test-validity",
		HeadSHA:      marker.Head,
		Conclusion:   "success",
		Title:        "review complete",
		Summary:      "approved",
		MachineBlock: &block,
	})
	if err != nil {
		t.Fatalf("EnsureCheckRun() error = %v", err)
	}
	if !created || run.ID != 10 || posted.Status != "completed" || posted.HeadSHA != marker.Head {
		t.Fatalf("check run = %#v, created = %v, posted = %#v", run, created, posted)
	}
	parsed, err := ParseRecordSurface(posted.Output.Text)
	if err != nil || parsed.Marker != marker || parsed.MachineBlock == nil {
		t.Fatalf("posted output = %q, parsed = %#v, error = %v", posted.Output.Text, parsed, err)
	}
}

func TestEnsureLabelSearchesAllPages(t *testing.T) {
	t.Parallel()

	var creates atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/widgets/issues/16/labels":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"name":"AI-READY","color":"1d76db"}]`)
				return
			}
			w.Header().Set("Link", pageLink(server.URL, r.URL.Path, 2))
			fmt.Fprint(w, `[{"name":"bug","color":"ff0000"}]`)
		case "POST /repos/acme/widgets/issues/16/labels":
			creates.Add(1)
			fmt.Fprint(w, `[]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).EnsureLabel(t.Context(), 16, "ai-ready")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if changed || creates.Load() != 0 {
		t.Fatalf("changed = %v, POST count = %d", changed, creates.Load())
	}
}

func TestEnsureLabelAddsOnlyRequestedLabelWhenMissing(t *testing.T) {
	t.Parallel()

	var posted struct {
		Labels []string `json:"labels"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[]`)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(w, `[{"name":"ai-in-progress"}]`)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	changed, err := testGateway(server.Client(), server.URL).EnsureLabel(t.Context(), 16, "ai-in-progress")
	if err != nil {
		t.Fatalf("EnsureLabel() error = %v", err)
	}
	if !changed || len(posted.Labels) != 1 || posted.Labels[0] != "ai-in-progress" {
		t.Fatalf("changed = %v, posted = %#v", changed, posted)
	}
}

func TestEnsureCheckRunRejectsHeadMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	_, _, err := testGateway(server.Client(), server.URL).EnsureCheckRun(t.Context(), CheckRunRecord{
		Marker:     validMarker(),
		Name:       "kudo/test-validity",
		HeadSHA:    strings.Repeat("c", 40),
		Conclusion: "success",
		Title:      "review complete",
		Summary:    "approved",
	})
	if err == nil {
		t.Fatal("EnsureCheckRun() error = nil, want head mismatch")
	}
}
