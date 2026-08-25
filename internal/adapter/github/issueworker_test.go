package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
)

const (
	adapterBaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	adapterHeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	adapterTreeSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestIssueWorkerGatewayObservesTypedClaimCheckpoint(t *testing.T) {
	t.Parallel()

	checkpoint := adapterCheckpoint(t)
	body := renderClaimBody(t, checkpoint, 44)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/17":
			fmt.Fprintf(w, `{"id":17,"number":17,"state":"open","title":"claim","body":"task","repository_url":%q,"assignees":[{"id":1,"login":"worker"}],"labels":[{"name":"ai-ready"}]}`, server.URL+"/repos/acme/widgets")
		case "/repos/acme/widgets/issues/17/parent":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Issue does not have a parent"}`)
		case "/repos/acme/widgets/issues/17/sub_issues", "/repos/acme/widgets/issues/17/comments", "/repos/acme/widgets/issues/44/comments":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/branches/kudo/issue-17":
			fmt.Fprint(w, `{"name":"kudo/issue-17","commit":{"sha":"`+adapterHeadSHA+`"}}`)
		case "/repos/acme/widgets/pulls":
			fmt.Fprintf(w, `[{"id":44,"number":44,"state":"open","draft":true,"title":"claim","body":%q,"user":{"id":2,"login":"worker"},"head":{"ref":"kudo/issue-17","sha":"%s"},"base":{"ref":"main","sha":"%s"}}]`, body, adapterHeadSHA, adapterBaseSHA)
		case "/repos/acme/widgets/commits/" + adapterHeadSHA + "/check-runs":
			fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)

	observation, err := testGateway(server.Client(), server.URL).ObserveClaimIssue(t.Context(), contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17})
	if err != nil {
		t.Fatalf("ObserveClaimIssue() error = %v", err)
	}
	if observation.Issue.Ref.String() != "github://acme/widgets/issues/17" || string(observation.Issue.RawBody) != "task" {
		t.Fatalf("issue = %#v", observation.Issue)
	}
	if len(observation.PullRequests) != 1 || observation.PullRequests[0].Checkpoint == nil || *observation.PullRequests[0].Checkpoint != checkpoint {
		t.Fatalf("pull requests = %#v", observation.PullRequests)
	}
}

func TestIssueWorkerGatewayCreatesBranchAndConvergesBootstrapCommit(t *testing.T) {
	t.Parallel()

	branchSHA := ""
	commitCreates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/branches/main":
			fmt.Fprint(w, `{"name":"main","commit":{"sha":"`+adapterBaseSHA+`"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/refs":
			var input struct{ Ref, SHA string }
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if branchSHA != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"Reference already exists"}`)
				return
			}
			if input.Ref != "refs/heads/kudo/issue-17" || input.SHA != adapterBaseSHA {
				t.Fatalf("create ref input = %#v", input)
			}
			branchSHA = input.SHA
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"ref":"refs/heads/kudo/issue-17","object":{"type":"commit","sha":"`+adapterBaseSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/branches/kudo/issue-17":
			fmt.Fprint(w, `{"name":"kudo/issue-17","commit":{"sha":"`+branchSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/commits/"+adapterBaseSHA:
			fmt.Fprint(w, `{"sha":"`+adapterBaseSHA+`","tree":{"sha":"`+adapterTreeSHA+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/commits/"+adapterHeadSHA:
			fmt.Fprint(w, `{"sha":"`+adapterHeadSHA+`","message":"claim: #17","tree":{"sha":"`+adapterTreeSHA+`"},"parents":[{"sha":"`+adapterBaseSHA+`"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/git/commits":
			commitCreates++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"sha":"`+adapterHeadSHA+`","tree":{"sha":"`+adapterTreeSHA+`"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/git/refs/heads/kudo/issue-17":
			branchSHA = adapterHeadSHA
			fmt.Fprint(w, `{"ref":"refs/heads/kudo/issue-17","object":{"type":"commit","sha":"`+adapterHeadSHA+`"}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	gateway := testGateway(server.Client(), server.URL)
	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}

	base, err := gateway.ResolveClaimBase(t.Context(), issue, nil)
	if err != nil || base.Name != "main" || base.SHA != adapterBaseSHA {
		t.Fatalf("ResolveClaimBase() = %#v, %v", base, err)
	}
	created, err := gateway.CreateClaimBranch(t.Context(), issue, "kudo/issue-17", base.SHA)
	if err != nil || !created {
		t.Fatalf("CreateClaimBranch() = %v, %v", created, err)
	}
	created, err = gateway.CreateClaimBranch(t.Context(), issue, "kudo/issue-17", base.SHA)
	if err != nil || created {
		t.Fatalf("second CreateClaimBranch() = %v, %v", created, err)
	}
	head, err := gateway.EnsureBootstrapCommit(t.Context(), issue, "kudo/issue-17", base.SHA)
	if err != nil || head != adapterHeadSHA {
		t.Fatalf("EnsureBootstrapCommit() = %q, %v", head, err)
	}
	head, err = gateway.EnsureBootstrapCommit(t.Context(), issue, "kudo/issue-17", base.SHA)
	if err != nil || head != adapterHeadSHA || commitCreates != 1 {
		t.Fatalf("second EnsureBootstrapCommit() = %q, %v, commits=%d", head, err, commitCreates)
	}
	base, err = gateway.ResolveClaimBase(t.Context(), issue, &issueworker.ClaimBranch{Name: "kudo/issue-17", SHA: adapterHeadSHA})
	if err != nil || base.SHA != adapterBaseSHA {
		t.Fatalf("residue ResolveClaimBase() = %#v, %v", base, err)
	}
}

func TestIssueWorkerGatewayEnsuresDraftCheckpointAndClaimLabels(t *testing.T) {
	t.Parallel()

	checkpoint := adapterCheckpoint(t)
	pullBody := ""
	labels := []string{"ai-ready", "ai-needs-human", "bug"}
	pullCreates := 0
	labelAdds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls":
			if pullCreates == 0 {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprintf(w, `[{"id":44,"number":44,"state":"open","draft":true,"title":"claim","body":%q,"user":{"id":2,"login":"worker"},"head":{"ref":"kudo/issue-17","sha":"%s"},"base":{"ref":"main","sha":"%s"}}]`, pullBody, adapterHeadSHA, adapterBaseSHA)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/pulls":
			pullCreates++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":44,"number":44,"state":"open","draft":true,"title":"claim","body":"checkpoint pending","user":{"id":2,"login":"worker"},"head":{"ref":"kudo/issue-17","sha":"%s"},"base":{"ref":"main","sha":"%s"}}`, adapterHeadSHA, adapterBaseSHA)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/pulls/44":
			var input struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			pullBody = input.Body
			fmt.Fprintf(w, `{"id":44,"number":44,"state":"open","draft":true,"title":"claim","body":%q,"user":{"id":2,"login":"worker"},"head":{"ref":"kudo/issue-17","sha":"%s"},"base":{"ref":"main","sha":"%s"}}`, pullBody, adapterHeadSHA, adapterBaseSHA)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/17/labels":
			encoded, _ := json.Marshal(slicesToLabels(labels))
			w.Write(encoded)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/17/labels/"):
			name := strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/issues/17/labels/")
			labels = slices.DeleteFunc(labels, func(label string) bool { return strings.EqualFold(label, name) })
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/17/labels":
			labelAdds++
			labels = append(labels, "ai-in-progress")
			fmt.Fprint(w, `[]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	gateway := testGateway(server.Client(), server.URL)
	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	input := issueworker.DraftPullRequestInput{
		Issue: issue, Title: "claim", BranchName: "kudo/issue-17", HeadSHA: adapterHeadSHA,
		Base: issueworker.ClaimBase{Name: "main", SHA: adapterBaseSHA}, Checkpoint: checkpoint,
	}

	pull, err := gateway.EnsureDraftPullRequest(t.Context(), input)
	if err != nil || pull.Number != 44 || pull.Checkpoint == nil || *pull.Checkpoint != checkpoint {
		t.Fatalf("EnsureDraftPullRequest() = %#v, %v", pull, err)
	}
	if !strings.Contains(pullBody, "kudo-marker") || !strings.Contains(pullBody, "kudo-machine") {
		t.Fatalf("pull body = %s", pullBody)
	}
	if err := gateway.ReconcileClaimLabels(t.Context(), issue, []string{"ai-ready", "ai-needs-human"}, "ai-in-progress"); err != nil {
		t.Fatalf("ReconcileClaimLabels() error = %v", err)
	}
	if !slices.Equal(labels, []string{"bug", "ai-in-progress"}) {
		t.Fatalf("labels = %v", labels)
	}
	if err := gateway.ReconcileClaimLabels(t.Context(), issue, []string{"ai-ready", "ai-needs-human"}, "ai-in-progress"); err != nil {
		t.Fatalf("second ReconcileClaimLabels() error = %v", err)
	}
	if labelAdds != 1 {
		t.Fatalf("label add count = %d", labelAdds)
	}

	pull, err = gateway.EnsureDraftPullRequest(t.Context(), input)
	if err != nil || pullCreates != 1 || pull.Checkpoint == nil {
		t.Fatalf("idempotent EnsureDraftPullRequest() = %#v, %v, creates=%d", pull, err, pullCreates)
	}
}

func adapterCheckpoint(t *testing.T) contract.ClaimCheckpoint {
	t.Helper()
	context := contract.ClaimContext{
		Compiler:        contract.IssueCompilerVersionV1Alpha1,
		Observation:     contract.IssueObservationRef{Schema: contract.IssueObservationSchemaV1Alpha1, Digest: contract.SHA256([]byte("observation"))},
		BodyDigest:      contract.SHA256([]byte("body")),
		TaskContext:     contract.TaskContextRef{Schema: contract.TaskContextSchemaV1Alpha1, Digest: contract.SHA256([]byte("task"))},
		ContextManifest: contract.ContextManifestRef{Schema: contract.ContextManifestSchemaV1Alpha1, Digest: contract.SHA256([]byte("manifest"))},
		BaseSHA:         adapterBaseSHA,
	}
	return contract.ClaimCheckpoint{
		Schema: contract.ClaimCheckpointSchemaV1Alpha1, Context: context,
		ExecutionPolicy:  contract.ExecutionPolicyRef{Schema: contract.ExecutionPolicySchemaV1Alpha1, Digest: contract.SHA256([]byte("execution"))},
		EscalationPolicy: contract.EscalationPolicyRef{Schema: contract.EscalationPolicySchemaV1Alpha1, Digest: contract.SHA256([]byte("escalation"))},
	}
}

func renderClaimBody(t *testing.T, checkpoint contract.ClaimCheckpoint, pullNumber int) string {
	t.Helper()
	ref, payload, err := contract.EncodeClaimCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderComment("claim", Marker{
		Repository: Repository{Owner: "acme", Name: "widgets"}, Issue: 17, Run: fmt.Sprint(pullNumber), Kind: "claim-checkpoint", Digest: string(ref.Digest),
	}, &MachineBlock{Kind: "claim-checkpoint", MediaType: contract.MediaTypeJSON, Digest: string(ref.Digest), Payload: payload.Data})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func slicesToLabels(values []string) []map[string]string {
	result := make([]map[string]string, len(values))
	for index, value := range values {
		result[index] = map[string]string{"name": value}
	}
	return result
}
