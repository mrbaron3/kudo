package reviewagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/agentpackage"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/livecontext"
)

const testHeadSHA = "89abcdef0123456789abcdef0123456789abcdef"

func TestExecutorRunsPackageAndBindsProviderOutput(t *testing.T) {
	t.Parallel()

	prepared := testPreparedReview(t)
	policy := testExecutionPolicy()
	provider := &fakeProvider{output: []byte(`{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","findings":[]}`)}
	now := time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)
	executor := Executor{Provider: provider, Clock: fakeClock{now: now}}
	result, err := executor.ExecuteTestValidity(t.Context(), prepared, policy, "/checkout/head", "review-01")
	if err != nil {
		t.Fatalf("ExecuteTestValidity() error = %v", err)
	}
	if provider.calls != 1 || !reflect.DeepEqual(provider.policy, policy.ReviewWorker) || provider.workDir != "/checkout/head" {
		t.Fatalf("provider call = %#v", provider)
	}
	if result.CreatedAt != now || result.Verdict != contract.VerdictApprove {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecutorRejectsExecutionPolicyBindingBeforeProvider(t *testing.T) {
	t.Parallel()

	prepared := testPreparedReview(t)
	policy := testExecutionPolicy()
	policy.ReviewWorker.Model = "changed"
	provider := &fakeProvider{}
	executor := Executor{Provider: provider, Clock: fakeClock{now: time.Now()}}
	if _, err := executor.ExecuteTestValidity(t.Context(), prepared, policy, "/checkout/head", "review-01"); err == nil {
		t.Fatal("ExecuteTestValidity() error = nil")
	}
	if provider.calls != 0 {
		t.Fatal("Execution Policy mismatch 後に provider を起動した")
	}
}

func TestBuildTestValidityRequestUsesOnlyVerifiedImmutableInput(t *testing.T) {
	t.Parallel()

	prepared := testPreparedReview(t)
	requestBytes, err := BuildTestValidityRequest(prepared)
	if err != nil {
		t.Fatalf("BuildTestValidityRequest() error = %v", err)
	}
	if err := agentpackage.ValidateJSON(prepared.Package.InputSchema, requestBytes); err != nil {
		t.Fatalf("request schema validation = %v", err)
	}
	var request TestValidityRequest
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := contract.ReviewRequestDigest(prepared.Request)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestDigest != wantDigest || request.AgentPackage != prepared.Package.Ref || request.HeadSHA != testHeadSHA {
		t.Fatalf("request binding = %#v", request)
	}
	if len(request.PolicyRefs) != 1 || request.PolicyRefs[0] != "agent-packages/test_validity/v1alpha1/instructions.md" {
		t.Fatalf("policy refs = %v", request.PolicyRefs)
	}
	if request.TaskContext.Digest != prepared.Resolution.Compiled.TaskContextRef.Digest {
		t.Fatalf("task context = %#v", request.TaskContext)
	}
	if len(request.Artifacts) != 4 || request.Artifacts[0].Name != "pull-request-observation" ||
		request.Artifacts[3].Name != "test-plan" {
		t.Fatalf("artifacts は logical name 順でない: %#v", request.Artifacts)
	}
	if strings.Contains(string(requestBytes), "raw Issue body must not leak") {
		t.Fatal("raw Issue body が provider request へ混入した")
	}

	before := append([]byte(nil), requestBytes...)
	prepared.Artifacts[0].Data[0] = 'X'
	if !bytes.Equal(before, requestBytes) {
		t.Fatal("immutable request が caller の slice と共有された")
	}
}

func TestBuildTestValidityRequestRejectsUnverifiedInput(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*PreparedReview){
		"package binding": func(input *PreparedReview) {
			input.Request.AgentPackage.Digest = contract.SHA256([]byte("other package"))
		},
		"package bytes": func(input *PreparedReview) {
			input.Package.Instructions[0] = 'X'
		},
		"context manifest": func(input *PreparedReview) {
			input.Request.ContextManifest.Digest = contract.SHA256([]byte("stale context"))
		},
		"artifact bytes": func(input *PreparedReview) {
			input.Artifacts[0].Data = []byte("tampered")
		},
		"artifact missing": func(input *PreparedReview) {
			input.Artifacts = input.Artifacts[1:]
		},
		"artifact extra": func(input *PreparedReview) {
			input.Artifacts = append(input.Artifacts, ArtifactMaterial{
				Name: "undeclared", MediaType: "text/plain", Data: []byte("extra"),
			})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := testPreparedReview(t)
			mutate(&input)
			if _, err := BuildTestValidityRequest(input); err == nil {
				t.Fatal("BuildTestValidityRequest() error = nil")
			}
		})
	}
}

func TestBindTestValidityOutputBuildsVersionedReviewResult(t *testing.T) {
	t.Parallel()

	prepared := testPreparedReview(t)
	requestBytes, err := BuildTestValidityRequest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","findings":[]}`)
	createdAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	result, err := BindTestValidityOutput(prepared, requestBytes, output, "review-01", createdAt)
	if err != nil {
		t.Fatalf("BindTestValidityOutput() error = %v", err)
	}
	requestDigest, _ := contract.ReviewRequestDigest(prepared.Request)
	if result.RequestDigest != requestDigest || result.Verdict != contract.VerdictApprove || result.ReviewRunID != "review-01" {
		t.Fatalf("result = %#v", result)
	}
	if err := contract.BindReviewResult(prepared.Request, result); err != nil {
		t.Fatalf("result binding = %v", err)
	}
}

func TestBindTestValidityOutputRejectsInvalidProviderResult(t *testing.T) {
	t.Parallel()

	prepared := testPreparedReview(t)
	requestBytes, err := BuildTestValidityRequest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	validEvidence := prepared.Manifest.Entries[0].Digest
	tests := map[string]string{
		"unknown field":            `{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","findings":[],"score":1}`,
		"missing blocking finding": `{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"request_changes","findings":[]}`,
		"unbound evidence":         `{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"request_changes","findings":[{"id":"F-1","severity":"blocking","summary":"不足","expected":"全AC","observed":"AC-1のみ","evidenceRefs":["sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"]}]}`,
		"duplicate field":          `{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","verdict":"approve","findings":[]}`,
		"approve with blocking":    `{"schema":"kudo.agent-output/test_validity/v1alpha1","verdict":"approve","findings":[{"id":"F-1","severity":"blocking","summary":"不足","expected":"全AC","observed":"AC-1のみ","evidenceRefs":["` + string(validEvidence) + `"]}]}`,
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := BindTestValidityOutput(prepared, requestBytes, []byte(output), "review-01", time.Now())
			if !errors.Is(err, ErrInvalidProviderOutput) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidProviderOutput)
			}
		})
	}
}

func testPreparedReview(t *testing.T) PreparedReview {
	t.Helper()
	pkg, err := agentpackage.Load(os.DirFS("../.."), "agent-packages/test_validity/v1alpha1")
	if err != nil {
		t.Fatal(err)
	}
	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	compiled, validation := contract.Compile(testTaskBody(), issue)
	if len(validation) > 0 {
		t.Fatalf("Compile() errors = %v", validation)
	}
	manifest := contract.ContextManifest{
		Schema:        contract.ContextManifestSchemaV1Alpha1,
		TaskContext:   compiled.TaskContextRef,
		BaseSHA:       testHeadSHA,
		Dependencies:  []contract.DependencyCompletion{},
		AuthorityRefs: []contract.AuthorityContent{},
	}
	manifestRef, manifestPayload, err := contract.EncodeContextManifest(compiled.ClaimRequirements, manifest)
	if err != nil {
		t.Fatal(err)
	}
	resolution := &livecontext.Resolution{
		Compiled: compiled, Manifest: manifest, ManifestRef: manifestRef,
		ManifestPayload: manifestPayload, Authorities: []livecontext.Authority{},
	}
	executionPolicy := testExecutionPolicy()
	executionRef, _, err := contract.EncodeExecutionPolicy(executionPolicy)
	if err != nil {
		t.Fatal(err)
	}
	prObservation := contract.PullRequestObservation{
		Schema:      contract.PullRequestObservationSchemaV1Alpha1,
		PullRequest: contract.PullRequestRef{Owner: "acme", Repository: "widgets", Number: 23},
		State:       contract.PullRequestOpen, Draft: true, HeadSHA: testHeadSHA, BaseRef: "main",
		BodyDigest: contract.SHA256([]byte("draft PR")),
	}
	prObservationRef, prObservationPayload, err := contract.EncodePullRequestObservation(prObservation)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []ArtifactMaterial{
		{Name: "test-plan", MediaType: contract.MediaTypeMarkdown, Data: []byte("- AC-1を検証する\n")},
		{Name: "red-evidence", MediaType: "text/plain; charset=utf-8", Data: []byte("FAIL expected\n")},
		{Name: "source-bundle", MediaType: "application/octet-stream", Data: []byte{0, 1, 2}},
		{Name: "pull-request-observation", Schema: prObservationPayload.Schema, MediaType: prObservationPayload.MediaType, Data: prObservationPayload.Data},
	}
	entries := make([]contract.ArtifactEntry, len(artifacts))
	for i, artifact := range artifacts {
		entries[i] = contract.ArtifactEntry{
			Name: artifact.Name, MediaType: artifact.MediaType, Length: int64(len(artifact.Data)), Digest: contract.SHA256(artifact.Data),
		}
	}
	artifactManifest := contract.ArtifactManifest{Schema: contract.ArtifactManifestSchemaV1Alpha1, Entries: entries}
	artifactManifestRef, _, err := contract.EncodeArtifactManifest(artifactManifest)
	if err != nil {
		t.Fatal(err)
	}
	request := contract.ReviewRequest{
		Schema: contract.ReviewRequestSchemaV1Alpha1, RequestID: "request-01", Kind: contract.ReviewTestValidity,
		ProducerRunID: "run-01", Issue: issue, PullRequest: prObservation.PullRequest,
		Observation: compiled.ObservationRef, PullRequestObservation: prObservationRef, HeadSHA: testHeadSHA,
		ContextManifest: manifestRef, ExecutionPolicy: executionRef, AgentPackage: pkg.Ref,
		ArtifactManifest: artifactManifestRef,
		PolicyRefs:       []string{"agent-packages/test_validity/v1alpha1/instructions.md"},
		CreatedAt:        time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC),
	}
	return PreparedReview{
		Request: request, Package: pkg, Resolution: resolution,
		Manifest: artifactManifest, Artifacts: artifacts,
	}
}

func testExecutionPolicy() contract.ExecutionPolicy {
	return contract.ExecutionPolicy{
		Schema: contract.ExecutionPolicySchemaV1Alpha1,
		IssueWorker: contract.WorkerExecutionPolicy{
			Provider: "codex", Model: "gpt", Adapter: "codex-cli", AdapterVersion: "1.0.0",
			ToolPermissions: []string{"repository:write"}, Timeout: time.Minute,
		},
		ReviewWorker: contract.WorkerExecutionPolicy{
			Provider: "codex", Model: "gpt", Adapter: "codex-cli", AdapterVersion: "1.0.0",
			ToolPermissions: []string{"repository:read"}, Timeout: time.Minute,
		},
	}
}

type fakeProvider struct {
	output  []byte
	err     error
	calls   int
	policy  contract.WorkerExecutionPolicy
	pkg     agentpackage.Package
	request []byte
	workDir string
}

func (f *fakeProvider) Run(
	_ context.Context,
	policy contract.WorkerExecutionPolicy,
	pkg agentpackage.Package,
	request []byte,
	workDir string,
) ([]byte, error) {
	f.calls++
	f.policy = policy
	f.pkg = pkg
	f.request = append([]byte(nil), request...)
	f.workDir = workDir
	return append([]byte(nil), f.output...), f.err
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func testTaskBody() string {
	return "## Contract\n\n```yaml\n" +
		"schema: kudo.issue/v1alpha1\nkind: task\nreadiness: ready\nparent: null\ndependsOn: []\n" +
		"acceptanceCriteriaIds:\n  - AC-1\nauthorityRefs: []\n```\n\n" +
		"## Outcome\n\n結果。\n\n## Scope\n\n### Included\n\n- 対象\n\n### Excluded\n\n- 対象外\n\n" +
		"## Deliverables\n\n- 成果物\n\n## Acceptance Criteria\n\n### AC-1\n\n- Given: 前提\n- When: 操作\n- Then: 結果\n\n" +
		"## Verification and Evidence\n\n- test\n\n## Constraints and Invariants\n\n- 制約\n\n" +
		"## Decision Authority\n\n- 判断\n\n## Stop and Escalation Conditions\n\n- 停止\n\n" +
		"<!-- raw Issue body must not leak -->\n"
}
