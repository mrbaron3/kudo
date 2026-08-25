package issueworker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/livecontext"
)

const (
	claimBaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claimHeadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var claimTime = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func TestClaimHandlerClaimsCandidateAndRecordsCheckpointBeforeLabels(t *testing.T) {
	t.Parallel()

	gateway := newFakeGitHub(claimTaskBody("ready", "[]", []string{"AGENTS.md"}))
	gateway.contents["AGENTS.md@"+claimBaseSHA] = []byte("rules\r\n")
	handler := newTestHandler(t, gateway)
	op := claimOperation(t)

	result := handler.Handle(t.Context(), op, "attempt-01")
	if result.Status != StatusClaimed || result.OperationResult == nil {
		t.Fatalf("result = %#v", result)
	}
	if err := contract.BindOperationResult(op, *result.OperationResult); err != nil {
		t.Fatalf("BindOperationResult() error = %v", err)
	}
	if result.PullRequest.String() != "github://acme/widgets/pull/44" ||
		!slices.Contains(result.OperationResult.ExternalRefs, result.PullRequest.String()) {
		t.Fatalf("pull request = %#v, refs = %v", result.PullRequest, result.OperationResult.ExternalRefs)
	}
	if gateway.branchCreates != 1 || gateway.bootstrapCreates != 1 || gateway.pullCreates != 1 {
		t.Fatalf("mutations: branch=%d bootstrap=%d pull=%d", gateway.branchCreates, gateway.bootstrapCreates, gateway.pullCreates)
	}
	if !slices.Equal(gateway.observation.Issue.Labels, []string{"ai-in-progress"}) {
		t.Fatalf("labels = %v", gateway.observation.Issue.Labels)
	}
	checkpoint := gateway.observation.PullRequests[0].Checkpoint
	if checkpoint == nil || checkpoint.Context != *result.OperationResult.ClaimContext ||
		checkpoint.ExecutionPolicy != op.ExecutionPolicy || checkpoint.EscalationPolicy != testEscalationRef() {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if gateway.lastContentRef != claimBaseSHA {
		t.Fatalf("authority ref = %q", gateway.lastContentRef)
	}
	if gateway.checkpointRecordedStep == 0 || gateway.labelsRecordedStep <= gateway.checkpointRecordedStep {
		t.Fatalf("recording order: checkpoint=%d labels=%d", gateway.checkpointRecordedStep, gateway.labelsRecordedStep)
	}
}

func TestClaimHandlerClassifiesCandidateDependencyAndRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeGitHub)
		body   string
		want   Status
		code   RejectionCode
	}{
		{name: "closed", body: claimTaskBody("ready", "[]", nil), mutate: func(g *fakeGitHub) { g.observation.Issue.State = "closed" }, want: StatusSkippedNotCandidate},
		{name: "pull request", body: claimTaskBody("ready", "[]", nil), mutate: func(g *fakeGitHub) { g.observation.Issue.IsPullRequest = true }, want: StatusSkippedNotCandidate},
		{name: "assignee", body: claimTaskBody("ready", "[]", nil), mutate: func(g *fakeGitHub) { g.observation.Issue.Assignees = []string{"someone"} }, want: StatusSkippedNotCandidate},
		{name: "label", body: claimTaskBody("ready", "[]", nil), mutate: func(g *fakeGitHub) { g.observation.Issue.Labels = []string{"bug"} }, want: StatusSkippedNotCandidate},
		{name: "merged", body: claimTaskBody("ready", "[]", nil), mutate: func(g *fakeGitHub) {
			g.observation.PullRequests = []ClaimPullRequest{{Number: 41, State: "closed", Merged: true}}
		}, want: StatusSkippedAlreadyMerged},
		{name: "draft", body: claimTaskBody("draft", "[]", nil), want: StatusClaimRejected, code: RejectionNotReady},
		{name: "dependency", body: claimTaskBody("ready", "[github://acme/widgets/issues/16]", nil), want: StatusWaitingDependency},
		{name: "invalid contract", body: "not a contract", want: StatusClaimRejected, code: RejectionContractInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := newFakeGitHub(tt.body)
			if tt.mutate != nil {
				tt.mutate(gateway)
			}
			result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "attempt-01")
			if result.Status != tt.want {
				t.Fatalf("status = %q, want %q (result=%#v)", result.Status, tt.want, result)
			}
			if tt.code != "" && (result.Rejection == nil || result.Rejection.Code != tt.code) {
				t.Fatalf("rejection = %#v, want code %q", result.Rejection, tt.code)
			}
			if gateway.branchCreates != 0 {
				t.Fatalf("branch created for %s", tt.name)
			}
		})
	}
}

func TestClaimHandlerSeparatesAuthorityAndTransportFailures(t *testing.T) {
	t.Parallel()

	t.Run("authority missing", func(t *testing.T) {
		gateway := newFakeGitHub(claimTaskBody("ready", "[]", []string{"missing.md"}))
		gateway.contentErr = livecontext.ErrSourceNotFound
		result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "attempt-01")
		if result.Status != StatusClaimRejected || result.Rejection == nil || result.Rejection.Code != RejectionAuthorityInvalid {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("GitHub transport", func(t *testing.T) {
		gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
		gateway.observeErr = errors.New("timeout")
		before := slices.Clone(gateway.observation.Issue.Labels)
		result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "attempt-01")
		if result.Status != StatusFailedTransport || result.Err == nil {
			t.Fatalf("result = %#v", result)
		}
		if !slices.Equal(before, gateway.observation.Issue.Labels) {
			t.Fatalf("transport failure changed labels: %v", gateway.observation.Issue.Labels)
		}
	})
}

func TestClaimHandlerRereadsExactBodyBeforeCreatingRef(t *testing.T) {
	t.Parallel()

	body := claimTaskBody("ready", "[]", nil)
	gateway := newFakeGitHub(body)
	gateway.bodyOnRead = []byte(body + "\n<!-- edited -->\n")
	result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "attempt-01")
	if result.Status != StatusClaimRejected || result.Rejection == nil || result.Rejection.Code != RejectionIssueChanged {
		t.Fatalf("result = %#v", result)
	}
	if gateway.branchCreates != 0 {
		t.Fatal("body changed after compile but branch was created")
	}
}

func TestClaimHandlerRejectsInvalidAttemptBeforeMutation(t *testing.T) {
	t.Parallel()

	gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
	result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "bad attempt")
	if result.Status != StatusClaimRejected || result.Rejection == nil || result.Rejection.Code != RejectionOperationInvalid {
		t.Fatalf("result = %#v", result)
	}
	if gateway.branchCreates != 0 {
		t.Fatal("invalid attempt mutated GitHub")
	}
}

func TestClaimHandlerRecoversEveryIncompleteCheckpointState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeGitHub)
	}{
		{name: "branch only", mutate: func(g *fakeGitHub) { g.observation.Branch = &ClaimBranch{Name: "kudo/issue-17", SHA: claimBaseSHA} }},
		{name: "pull request only", mutate: func(g *fakeGitHub) {
			g.observation.PullRequests = []ClaimPullRequest{{Number: 44, State: "open", Draft: true, HeadSHA: claimHeadSHA, BaseSHA: claimBaseSHA}}
		}},
		{name: "checkpoint missing", mutate: func(g *fakeGitHub) {
			g.observation.Branch = &ClaimBranch{Name: "kudo/issue-17", SHA: claimHeadSHA}
			g.observation.PullRequests = []ClaimPullRequest{{Number: 44, State: "open", Draft: true, HeadSHA: claimHeadSHA, BaseSHA: claimBaseSHA}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
			tt.mutate(gateway)
			result := newTestHandler(t, gateway).Handle(t.Context(), claimOperation(t), "attempt-01")
			if result.Status != StatusClaimed || gateway.observation.PullRequests[0].Checkpoint == nil {
				t.Fatalf("result = %#v, observation = %#v", result, gateway.observation)
			}
			if len(gateway.observation.PullRequests) != 1 {
				t.Fatalf("pull requests = %d", len(gateway.observation.PullRequests))
			}
		})
	}
}

func TestClaimHandlerRecoversCompletedCheckpointAndProjection(t *testing.T) {
	t.Parallel()

	gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
	handler := newTestHandler(t, gateway)
	op := claimOperation(t)
	gateway.labelErr = errors.New("label timeout")
	first := handler.Handle(t.Context(), op, "attempt-01")
	if first.Status != StatusFailedTransport || gateway.observation.PullRequests[0].Checkpoint == nil {
		t.Fatalf("first result = %#v, observation = %#v", first, gateway.observation)
	}
	branchCreates, bootstrapCreates, pullCreates := gateway.branchCreates, gateway.bootstrapCreates, gateway.pullCreates

	gateway.labelErr = nil
	// claim 完了後の routing label は candidate 条件ではない。checkpoint があれば
	// ai-ready の有無にかかわらず同じ Run の投影を完了する。
	gateway.observation.Issue.Labels = []string{"ai-in-progress"}
	second := handler.Handle(t.Context(), op, "attempt-02")
	if second.Status != StatusClaimed || second.OperationResult == nil {
		t.Fatalf("second result = %#v", second)
	}
	if gateway.branchCreates != branchCreates || gateway.bootstrapCreates != bootstrapCreates || gateway.pullCreates != pullCreates {
		t.Fatalf("completed checkpoint repeated mutations: branch=%d bootstrap=%d pull=%d", gateway.branchCreates, gateway.bootstrapCreates, gateway.pullCreates)
	}
}

func TestClaimHandlerUsesRoutingDefaults(t *testing.T) {
	t.Parallel()

	gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
	gateway.observation.Issue.Assignees = []string{"mrbaron3"}
	handler, err := NewClaimHandler(gateway, contract.NewCompiler(), ClaimConfig{
		EscalationPolicy: testEscalationRef(),
	}, fixedClock{now: claimTime})
	if err != nil {
		t.Fatalf("NewClaimHandler() error = %v", err)
	}
	result := handler.Handle(t.Context(), claimOperation(t), "attempt-defaults")
	if result.Status != StatusClaimed {
		t.Fatalf("result = %#v", result)
	}
}

func TestConcurrentClaimCreatesOneRun(t *testing.T) {
	t.Parallel()

	gateway := newFakeGitHub(claimTaskBody("ready", "[]", nil))
	handler := newTestHandler(t, gateway)
	op := claimOperation(t)
	const attempts = 16
	results := make(chan Result, attempts)
	var group sync.WaitGroup
	for index := range attempts {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results <- handler.Handle(t.Context(), op, "attempt-"+string(rune('a'+index)))
		}(index)
	}
	group.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.Status == StatusClaimed {
			claimed++
			continue
		}
		if result.Status != StatusSkippedNotCandidate {
			t.Fatalf("concurrent result = %#v", result)
		}
	}
	if claimed == 0 {
		t.Fatal("no reconcile observed the completed claim")
	}
	if gateway.branchCreates != 1 || gateway.bootstrapCreates != 1 || gateway.pullCreates != 1 || len(gateway.observation.PullRequests) != 1 {
		t.Fatalf("mutations: branch=%d bootstrap=%d pull=%d prs=%d", gateway.branchCreates, gateway.bootstrapCreates, gateway.pullCreates, len(gateway.observation.PullRequests))
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func newTestHandler(t *testing.T, gateway *fakeGitHub) *ClaimHandler {
	t.Helper()
	handler, err := NewClaimHandler(gateway, contract.NewCompiler(), ClaimConfig{
		TargetAssignee:   "worker",
		ReadyLabel:       "ai-ready",
		InProgressLabel:  "ai-in-progress",
		NeedsHumanLabel:  "ai-needs-human",
		EscalationPolicy: testEscalationRef(),
	}, fixedClock{now: claimTime})
	if err != nil {
		t.Fatalf("NewClaimHandler() error = %v", err)
	}
	return handler
}

func claimOperation(t *testing.T) contract.WorkerOperation {
	t.Helper()
	return contract.WorkerOperation{
		Schema:          contract.WorkerOperationSchemaV1Alpha1,
		OperationID:     "claim-17",
		RunID:           "claim-17",
		Kind:            contract.OperationClaim,
		Issue:           contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17},
		ExecutionPolicy: contract.ExecutionPolicyRef{Schema: contract.ExecutionPolicySchemaV1Alpha1, Digest: contract.SHA256([]byte("execution"))},
		CausationID:     "reconcile-17",
		CreatedAt:       claimTime.Add(-time.Minute),
	}
}

func testEscalationRef() contract.EscalationPolicyRef {
	return contract.EscalationPolicyRef{Schema: contract.EscalationPolicySchemaV1Alpha1, Digest: contract.SHA256([]byte("escalation"))}
}

type fakeGitHub struct {
	mu sync.Mutex

	observation ClaimObservation
	contents    map[string][]byte
	bodyOnRead  []byte

	observeErr error
	contentErr error
	labelErr   error

	branchCreates          int
	bootstrapCreates       int
	pullCreates            int
	lastContentRef         string
	mutationStep           int
	checkpointRecordedStep int
	labelsRecordedStep     int
}

func newFakeGitHub(body string) *fakeGitHub {
	return &fakeGitHub{
		observation: ClaimObservation{Issue: ClaimIssue{
			Ref:   contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17},
			State: "open", Title: "claim task", Assignees: []string{"worker"}, Labels: []string{"ai-ready", "ai-needs-human"}, RawBody: []byte(body),
		}},
		contents: make(map[string][]byte),
	}
}

func (f *fakeGitHub) ObserveClaimIssue(context.Context, contract.IssueRef) (ClaimObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observeErr != nil {
		return ClaimObservation{}, f.observeErr
	}
	return cloneClaimObservation(f.observation), nil
}

func (f *fakeGitHub) ReadIssue(_ context.Context, issue contract.IssueRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if issue.String() == f.observation.Issue.Ref.String() {
		if f.bodyOnRead != nil {
			return append([]byte(nil), f.bodyOnRead...), nil
		}
		return append([]byte(nil), f.observation.Issue.RawBody...), nil
	}
	return nil, livecontext.ErrSourceNotFound
}

func (f *fakeGitHub) ReadRepositoryContent(_ context.Context, _ contract.IssueRef, path, ref string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastContentRef = ref
	if f.contentErr != nil {
		return nil, f.contentErr
	}
	data, ok := f.contents[path+"@"+ref]
	if !ok {
		return nil, livecontext.ErrSourceNotFound
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeGitHub) ResolveClaimBase(context.Context, contract.IssueRef, *ClaimBranch) (ClaimBase, error) {
	return ClaimBase{Name: "main", SHA: claimBaseSHA}, nil
}

func (f *fakeGitHub) CreateClaimBranch(_ context.Context, _ contract.IssueRef, name, baseSHA string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observation.Branch != nil {
		return false, nil
	}
	f.branchCreates++
	f.observation.Branch = &ClaimBranch{Name: name, SHA: baseSHA}
	return true, nil
}

func (f *fakeGitHub) EnsureBootstrapCommit(_ context.Context, _ contract.IssueRef, branchName, baseSHA string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observation.Branch == nil {
		return "", errors.New("branch missing")
	}
	if f.observation.Branch.SHA == baseSHA {
		f.bootstrapCreates++
		f.observation.Branch = &ClaimBranch{Name: branchName, SHA: claimHeadSHA}
	}
	return f.observation.Branch.SHA, nil
}

func (f *fakeGitHub) EnsureDraftPullRequest(_ context.Context, input DraftPullRequestInput) (ClaimPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.observation.PullRequests {
		if f.observation.PullRequests[index].State == "open" {
			f.observation.PullRequests[index].Checkpoint = cloneCheckpoint(&input.Checkpoint)
			f.mutationStep++
			f.checkpointRecordedStep = f.mutationStep
			return f.observation.PullRequests[index], nil
		}
	}
	f.pullCreates++
	pull := ClaimPullRequest{Number: 44, State: "open", Draft: true, HeadSHA: input.HeadSHA, BaseSHA: input.Base.SHA, Checkpoint: cloneCheckpoint(&input.Checkpoint)}
	f.observation.PullRequests = append(f.observation.PullRequests, pull)
	f.mutationStep++
	f.checkpointRecordedStep = f.mutationStep
	return pull, nil
}

func (f *fakeGitHub) ReconcileClaimLabels(_ context.Context, _ contract.IssueRef, remove []string, add string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.labelErr != nil {
		return f.labelErr
	}
	labels := f.observation.Issue.Labels[:0]
	for _, label := range f.observation.Issue.Labels {
		if !containsFold(remove, label) && !strings.EqualFold(label, add) {
			labels = append(labels, label)
		}
	}
	labels = append(labels, add)
	f.observation.Issue.Labels = labels
	f.mutationStep++
	f.labelsRecordedStep = f.mutationStep
	return nil
}

func cloneClaimObservation(value ClaimObservation) ClaimObservation {
	result := value
	result.Issue.Assignees = slices.Clone(value.Issue.Assignees)
	result.Issue.Labels = slices.Clone(value.Issue.Labels)
	result.Issue.RawBody = append([]byte(nil), value.Issue.RawBody...)
	if value.Branch != nil {
		branch := *value.Branch
		result.Branch = &branch
	}
	result.PullRequests = slices.Clone(value.PullRequests)
	for index := range result.PullRequests {
		result.PullRequests[index].Checkpoint = cloneCheckpoint(value.PullRequests[index].Checkpoint)
	}
	return result
}

func cloneCheckpoint(value *contract.ClaimCheckpoint) *contract.ClaimCheckpoint {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func claimTaskBody(readiness, dependencies string, authority []string) string {
	authorityYAML := "authorityRefs: []"
	if len(authority) > 0 {
		authorityYAML = "authorityRefs:\n"
		for _, ref := range authority {
			authorityYAML += "  - " + ref + "\n"
		}
		authorityYAML = strings.TrimSuffix(authorityYAML, "\n")
	}
	dependsYAML := "dependsOn: " + dependencies
	if dependencies != "[]" {
		dependsYAML = "dependsOn:\n  - " + strings.Trim(dependencies, "[]")
	}
	return "## Contract\n\n```yaml\n" +
		"schema: kudo.issue/v1alpha1\nkind: task\nreadiness: " + readiness + "\nparent: null\n" + dependsYAML + "\n" +
		"acceptanceCriteriaIds:\n  - AC-1\n" + authorityYAML + "\n```\n\n" +
		"## Outcome\n\n結果。\n\n## Scope\n\n### Included\n\n- 対象\n\n### Excluded\n\n- 対象外\n\n" +
		"## Deliverables\n\n- 成果物\n\n## Acceptance Criteria\n\n### AC-1\n\n- Given: 前提\n- When: 操作\n- Then: 結果\n\n" +
		"## Verification and Evidence\n\n- test\n\n## Constraints and Invariants\n\n- 制約\n\n" +
		"## Decision Authority\n\n- 判断\n\n## Stop and Escalation Conditions\n\n- 停止\n"
}
