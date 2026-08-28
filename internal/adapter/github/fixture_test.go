package github

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	fixtureBootstrapSHA = "dddddddddddddddddddddddddddddddddddddddd"
	fixtureTestHeadSHA  = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fixtureTestTreeSHA  = "ffffffffffffffffffffffffffffffffffffffff"
	fixtureBlobSHA      = "1111111111111111111111111111111111111111"
)

func TestEnsureDevelopmentFixtureHeadCreatesTestOnlyCommitAndConverges(t *testing.T) {
	t.Parallel()

	file := DevelopmentFixtureFile{
		Path: "internal/reviewerfixtureseed/fixture_test.go",
		Data: []byte("package reviewerfixtureseed\n"),
	}
	branchSHA := adapterBaseSHA
	var bootstrapCreates atomic.Int32
	var testCreates atomic.Int32
	var blobCreates atomic.Int32
	var treeCreates atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/branches/main":
			fmt.Fprint(w, `{"name":"main","commit":{"sha":"`+adapterBaseSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/branches/kudo/issue-71":
			fmt.Fprint(w, `{"name":"kudo/issue-71","commit":{"sha":"`+branchSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/commits/"+adapterBaseSHA:
			fmt.Fprint(w, `{"sha":"`+adapterBaseSHA+`","tree":{"sha":"`+adapterTreeSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/commits/"+fixtureBootstrapSHA:
			fmt.Fprint(w, `{"sha":"`+fixtureBootstrapSHA+`","message":"claim: #71","tree":{"sha":"`+adapterTreeSHA+`"},"parents":[{"sha":"`+adapterBaseSHA+`"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/commits/"+fixtureTestHeadSHA:
			fmt.Fprint(w, `{"sha":"`+fixtureTestHeadSHA+`","message":"fixture: reviewer test-only #71","tree":{"sha":"`+fixtureTestTreeSHA+`"},"parents":[{"sha":"`+fixtureBootstrapSHA+`"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/blobs":
			blobCreates.Add(1)
			var input struct {
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Encoding != "base64" || input.Content != base64.StdEncoding.EncodeToString(file.Data) {
				t.Fatalf("blob input = %#v", input)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"sha":"`+fixtureBlobSHA+`"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/trees":
			treeCreates.Add(1)
			var input struct {
				BaseTree string `json:"base_tree"`
				Tree     []struct {
					Path, Mode, Type, SHA string
				} `json:"tree"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.BaseTree != adapterTreeSHA || len(input.Tree) != 1 || input.Tree[0].Path != file.Path ||
				input.Tree[0].Mode != "100644" || input.Tree[0].Type != "blob" || input.Tree[0].SHA != fixtureBlobSHA {
				t.Fatalf("tree input = %#v", input)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"sha":"`+fixtureTestTreeSHA+`"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/commits":
			var input struct {
				Message string   `json:"message"`
				Tree    string   `json:"tree"`
				Parents []string `json:"parents"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			switch input.Message {
			case "claim: #71":
				bootstrapCreates.Add(1)
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"sha":"`+fixtureBootstrapSHA+`","tree":{"sha":"`+adapterTreeSHA+`"}}`)
			case "fixture: reviewer test-only #71":
				testCreates.Add(1)
				if input.Tree != fixtureTestTreeSHA || len(input.Parents) != 1 || input.Parents[0] != fixtureBootstrapSHA {
					t.Fatalf("test commit input = %#v", input)
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"sha":"`+fixtureTestHeadSHA+`","tree":{"sha":"`+fixtureTestTreeSHA+`"}}`)
			default:
				t.Fatalf("commit input = %#v", input)
			}
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/git/refs/heads/kudo/issue-71":
			var input struct {
				SHA   string `json:"sha"`
				Force bool   `json:"force"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Force {
				t.Fatal("fixture ref update forced")
			}
			branchSHA = input.SHA
			fmt.Fprint(w, `{"ref":"refs/heads/kudo/issue-71","object":{"type":"commit","sha":"`+input.SHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/"+fixtureTestHeadSHA:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": fixtureTestHeadSHA,
				"files": []map[string]any{{
					"filename": file.Path, "status": "added", "sha": fixtureBlobSHA,
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/contents/"+file.Path:
			if got := r.URL.Query().Get("ref"); got != fixtureTestHeadSHA {
				t.Fatalf("content ref = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "path": file.Path, "sha": fixtureBlobSHA,
				"encoding": "base64", "content": base64.StdEncoding.EncodeToString(file.Data),
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	gateway := testGateway(server.Client(), server.URL)
	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 71}

	first, err := gateway.EnsureDevelopmentFixtureHead(t.Context(), issue, file)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gateway.EnsureDevelopmentFixtureHead(t.Context(), issue, file)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Base.SHA != adapterBaseSHA || first.BootstrapSHA != fixtureBootstrapSHA ||
		first.HeadSHA != fixtureTestHeadSHA || first.BranchName != "kudo/issue-71" {
		t.Fatalf("heads = %#v, %#v", first, second)
	}
	if bootstrapCreates.Load() != 1 || testCreates.Load() != 1 || blobCreates.Load() != 1 || treeCreates.Load() != 1 {
		t.Fatalf("creates: bootstrap=%d test=%d blob=%d tree=%d", bootstrapCreates.Load(), testCreates.Load(), blobCreates.Load(), treeCreates.Load())
	}
}

func TestEnsureUnmarkedCommentConvergesByExactAuthoredBody(t *testing.T) {
	t.Parallel()

	body := "test plan without marker\n<!-- kudo-machine payload -->"
	var stored string
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if stored == "" {
				fmt.Fprint(w, `[]`)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": 9, "body": stored, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			}})
		case http.MethodPost:
			creates.Add(1)
			var input struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			stored = input.Body
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 9, "body": stored, "user": map[string]any{"id": 101, "login": "kudo-actor[bot]"},
			})
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	gateway := testGateway(server.Client(), server.URL)
	first, created, err := gateway.EnsureUnmarkedComment(t.Context(), 44, body)
	if err != nil || !created {
		t.Fatalf("first = %#v, %v, %v", first, created, err)
	}
	second, created, err := gateway.EnsureUnmarkedComment(t.Context(), 44, body)
	if err != nil || created || first.ID != second.ID || creates.Load() != 1 || !strings.Contains(stored, "kudo-machine") {
		t.Fatalf("second = %#v, %v, %v, creates=%d", second, created, err, creates.Load())
	}
}

func TestEnsureDevelopmentFixtureHeadRejectsResidueThatChangesAnotherFile(t *testing.T) {
	t.Parallel()

	file := DevelopmentFixtureFile{
		Path: "internal/reviewerfixtureseed/fixture_test.go",
		Data: []byte("package reviewerfixtureseed\n"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case "/repos/acme/widgets/branches/main":
			fmt.Fprint(w, `{"name":"main","commit":{"sha":"`+adapterBaseSHA+`"}}`)
		case "/repos/acme/widgets/branches/kudo/issue-71":
			fmt.Fprint(w, `{"name":"kudo/issue-71","commit":{"sha":"`+fixtureTestHeadSHA+`"}}`)
		case "/repos/acme/widgets/git/commits/" + fixtureTestHeadSHA:
			fmt.Fprint(w, `{"sha":"`+fixtureTestHeadSHA+`","message":"fixture: reviewer test-only #71","tree":{"sha":"`+fixtureTestTreeSHA+`"},"parents":[{"sha":"`+fixtureBootstrapSHA+`"}]}`)
		case "/repos/acme/widgets/git/commits/" + fixtureBootstrapSHA:
			fmt.Fprint(w, `{"sha":"`+fixtureBootstrapSHA+`","message":"claim: #71","tree":{"sha":"`+adapterTreeSHA+`"},"parents":[{"sha":"`+adapterBaseSHA+`"}]}`)
		case "/repos/acme/widgets/git/commits/" + adapterBaseSHA:
			fmt.Fprint(w, `{"sha":"`+adapterBaseSHA+`","tree":{"sha":"`+adapterTreeSHA+`"}}`)
		case "/repos/acme/widgets/commits/" + fixtureTestHeadSHA:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": fixtureTestHeadSHA,
				"files": []map[string]any{
					{"filename": file.Path, "status": "added", "sha": fixtureBlobSHA},
					{"filename": "internal/production.go", "status": "modified", "sha": adapterTreeSHA},
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)

	_, err := testGateway(server.Client(), server.URL).EnsureDevelopmentFixtureHead(t.Context(),
		contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 71}, file)
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureInvalidResponse {
		t.Fatalf("error = %v, want invalid-response residue rejection", err)
	}
}
