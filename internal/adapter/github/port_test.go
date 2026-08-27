package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/controller"
)

// gateway が Controller 所有の port を満たすことを、composition root ではなくここで固定する。
// production code で adapter から controller を import すると依存方向が逆転するため、
// 「配線できること」の検査だけを test に置く（docs/spec/05_design/01_architecture.md の
// Go package layout）。
var (
	_ controller.LabelSurface = (*github.Gateway)(nil)
	_ controller.Discovery    = (*github.Gateway)(nil)
)

// tokenSource は external test package 用の最小の TokenSource である。
type tokenSource string

func (t tokenSource) Token(context.Context) (string, error) { return string(t), nil }

func TestGatewayEnsureIssueCommentConvergesTheGuidanceBody(t *testing.T) {
	t.Parallel()

	var bodies []string
	var comments []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(comments)
		case http.MethodPost, http.MethodPatch:
			var posted struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, posted.Body)
			comments = []map[string]any{{
				"id": 11, "body": posted.Body, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			}}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			_ = json.NewEncoder(w).Encode(comments[0])
		default:
			t.Errorf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	gateway, err := github.NewGateway(server.Client(), tokenSource("actor-token"), github.Config{
		BaseURL:    server.URL,
		Repository: github.Repository{Owner: "acme", Name: "widgets"},
		RecorderIdentity: &github.RecorderIdentity{
			CommentAuthor: github.Actor{ID: 101, Login: "kudo-actor[bot]"},
			CheckRunApp:   github.AppIdentity{ID: 202, Slug: "kudo-actor", Name: "Kudo Actor"},
		},
	})
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}

	created, err := gateway.EnsureIssueComment(t.Context(), 19, controller.AlreadyMergedCommentKind, "案内 v1")
	if err != nil {
		t.Fatalf("EnsureIssueComment() error = %v", err)
	}
	unchanged, err := gateway.EnsureIssueComment(t.Context(), 19, controller.AlreadyMergedCommentKind, "案内 v1")
	if err != nil {
		t.Fatalf("2 回目の EnsureIssueComment() error = %v", err)
	}
	updated, err := gateway.EnsureIssueComment(t.Context(), 19, controller.AlreadyMergedCommentKind, "案内 v2")
	if err != nil {
		t.Fatalf("3 回目の EnsureIssueComment() error = %v", err)
	}
	if !created || unchanged || !updated {
		t.Fatalf("created = %v, unchanged = %v, updated = %v", created, unchanged, updated)
	}
	if len(comments) != 1 || len(bodies) != 2 {
		t.Fatalf("comment 件数 = %d, 書き込み = %d, want 1 件を 2 回書き込み", len(comments), len(bodies))
	}
	if !strings.Contains(bodies[1], "案内 v2") {
		t.Fatalf("更新後の本文 = %q", bodies[1])
	}
	for _, body := range bodies {
		if !strings.Contains(body, fmt.Sprintf("%q", controller.AlreadyMergedCommentKind)) {
			t.Fatalf("marker に record kind が入っていない: %q", body)
		}
	}
}
