package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// canonical YAML encoder は golden fixture と自分自身でしか比較されないため、
// 「fixture と一致する」ことは「YAML として正しい」ことを意味しない。
// 独立実装（Ruby psych）を差分オラクルにして、encoder の出力が実際に YAML として
// parse でき、string scalar が escape 前の値へ戻ることを検証する。
//
// KUDO_YAML_ORACLE=required を設定すると parser 不在時に skip せず失敗する。CI で設定する。

// yamlOracleCandidates は YAML を JSON へ変換する独立実装の候補である。
// 1 つの runtime へ依存しないよう複数を順に試す。
var yamlOracleCandidates = []struct {
	name string
	args []string
}{
	{"ruby", []string{"-e", `require "yaml"; require "json"; print JSON.generate(YAML.safe_load(STDIN.read))`}},
	{"python3", []string{"-c", `import sys,yaml,json; print(json.dumps(yaml.safe_load(sys.stdin.read())),end="")`}},
}

// lookupYAMLOracle は利用できる oracle を返す。probe まで行い、runtime はあるが
// YAML library が無い候補を除外する。
func lookupYAMLOracle() (string, []string, bool) {
	for _, candidate := range yamlOracleCandidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		probe := exec.Command(path, candidate.args...)
		probe.Stdin = strings.NewReader("probe: \"ok\"\n")
		if err := probe.Run(); err == nil {
			return path, candidate.args, true
		}
	}
	return "", nil, false
}

// yamlOracle は canonical YAML bytes を独立実装で parse し、JSON へ変換して返す。
func yamlOracle(t *testing.T, data []byte) []byte {
	t.Helper()
	path, args, ok := lookupYAMLOracle()
	if !ok {
		if os.Getenv("KUDO_YAML_ORACLE") == "required" {
			t.Fatal("KUDO_YAML_ORACLE=required だが YAML oracle (ruby / python3+PyYAML) が無い")
		}
		t.Skip("YAML oracle (ruby / python3+PyYAML) が無いため skip")
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("canonical bytes が YAML として parse できない: %v\n%s\n--- input ---\n%s", err, stderr.String(), data)
	}
	return stdout.Bytes()
}

// oracleTaskContext は oracle が復元した Task Context を表す。
// encoder と別経路で key 名・入れ子・null/空 list の区別を固定する。
type oracleTaskContext struct {
	Schema   string `json:"schema"`
	Compiler string `json:"compiler"`
	Issue    string `json:"issue"`
	Contract struct {
		Schema                string   `json:"schema"`
		Kind                  string   `json:"kind"`
		Readiness             string   `json:"readiness"`
		Parent                *string  `json:"parent"`
		DependsOn             []string `json:"dependsOn"`
		AcceptanceCriteriaIDs []string `json:"acceptanceCriteriaIds"`
		AuthorityRefs         []string `json:"authorityRefs"`
	} `json:"contract"`
	Outcome            string `json:"outcome"`
	Scope              string `json:"scope"`
	Deliverables       string `json:"deliverables"`
	AcceptanceCriteria struct {
		Preamble string `json:"preamble"`
		Criteria []struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"criteria"`
	} `json:"acceptanceCriteria"`
	VerificationAndEvidence     string  `json:"verificationAndEvidence"`
	ConstraintsAndInvariants    string  `json:"constraintsAndInvariants"`
	DecisionAuthority           string  `json:"decisionAuthority"`
	StopAndEscalationConditions string  `json:"stopAndEscalationConditions"`
	AdvisoryHints               *string `json:"advisoryHints"`
}

func decodeOracle[T any](t *testing.T, data []byte) T {
	t.Helper()
	var got T
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("oracle 出力の decode に失敗: %v\n%s", err, data)
	}
	return got
}

func TestTaskContextRoundTripsThroughExternalYAMLParser(t *testing.T) {
	// escape が必要な文字を本文へ混ぜ、encoder の quote 規則を実装非依存に検証する
	body := strings.Replace(
		readFixture(t, "valid/full.md"),
		"- 停止条件",
		"- 停止条件: \"引用\" と \\ backslash と ` code span と 絵文字 😀\n- `key: value` に見える行\n\t- tab 始まりの行",
		1,
	)
	compiled := requireCompiled(t, body)
	got := decodeOracle[oracleTaskContext](t, yamlOracle(t, compiled.TaskContextPayload.Data))
	want := compiled.TaskContext

	if got.Schema != want.Schema || got.Compiler != want.Compiler || got.Issue != want.Issue.String() {
		t.Fatalf("identity が復元されない: %+v", got)
	}
	if got.Contract.Schema != want.ContractSchema ||
		got.Contract.Kind != string(want.Kind) ||
		got.Contract.Readiness != string(want.Readiness) {
		t.Fatalf("contract scalar が復元されない: %+v", got.Contract)
	}
	if want.Parent == nil {
		if got.Contract.Parent != nil {
			t.Fatal("parent: null が null として復元されない")
		}
	} else if got.Contract.Parent == nil || *got.Contract.Parent != want.Parent.String() {
		t.Fatalf("parent = %v, want %q", got.Contract.Parent, want.Parent.String())
	}

	wantDependsOn := make([]string, len(want.DependsOn))
	for i, ref := range want.DependsOn {
		wantDependsOn[i] = ref.String()
	}
	wantAuthority := make([]string, len(want.AuthorityRefs))
	for i, ref := range want.AuthorityRefs {
		wantAuthority[i] = ref.String()
	}
	if !reflect.DeepEqual(got.Contract.DependsOn, wantDependsOn) ||
		!reflect.DeepEqual(got.Contract.AcceptanceCriteriaIDs, want.AcceptanceCriteriaIDs) ||
		!reflect.DeepEqual(got.Contract.AuthorityRefs, wantAuthority) {
		t.Fatalf("list が復元されない: %+v", got.Contract)
	}

	sections := []struct {
		name string
		got  string
		want string
	}{
		{"outcome", got.Outcome, want.Outcome},
		{"scope", got.Scope, want.Scope},
		{"deliverables", got.Deliverables, want.Deliverables},
		{"preamble", got.AcceptanceCriteria.Preamble, want.AcceptanceCriteriaPreamble},
		{"verificationAndEvidence", got.VerificationAndEvidence, want.VerificationAndEvidence},
		{"constraintsAndInvariants", got.ConstraintsAndInvariants, want.ConstraintsAndInvariants},
		{"decisionAuthority", got.DecisionAuthority, want.DecisionAuthority},
		{"stopAndEscalationConditions", got.StopAndEscalationConditions, want.StopAndEscalationConditions},
	}
	for _, section := range sections {
		if section.got != section.want {
			t.Fatalf("%s が escape 前の値へ戻らない\n got: %q\nwant: %q", section.name, section.got, section.want)
		}
	}

	if len(got.AcceptanceCriteria.Criteria) != len(want.AcceptanceCriteria) {
		t.Fatalf("criteria 件数 = %d, want %d", len(got.AcceptanceCriteria.Criteria), len(want.AcceptanceCriteria))
	}
	for i, criterion := range got.AcceptanceCriteria.Criteria {
		if criterion.ID != want.AcceptanceCriteria[i].ID || criterion.Body != want.AcceptanceCriteria[i].Body {
			t.Fatalf("criteria[%d] が復元されない: %+v", i, criterion)
		}
	}

	if want.AdvisoryHints == nil {
		if got.AdvisoryHints != nil {
			t.Fatal("advisoryHints: null が null として復元されない")
		}
	} else if got.AdvisoryHints == nil || *got.AdvisoryHints != *want.AdvisoryHints {
		t.Fatalf("advisoryHints = %v", got.AdvisoryHints)
	}
}

// 存在する空 Advisory Hints は空 string、欠落は null として区別されなければならない。
// 実装内の比較では両者が同じ Go zero value になりうるため oracle 側で確認する。
func TestAdvisoryHintsNullAndEmptyAreDistinctToExternalParser(t *testing.T) {
	empty := decodeOracle[oracleTaskContext](t,
		yamlOracle(t, requireCompiled(t, readFixture(t, "valid/advisory-empty.md")).TaskContextPayload.Data))
	missing := decodeOracle[oracleTaskContext](t,
		yamlOracle(t, requireCompiled(t, readFixture(t, "valid/minimal.md")).TaskContextPayload.Data))

	if missing.AdvisoryHints != nil {
		t.Fatalf("欠落 section が null にならない: %q", *missing.AdvisoryHints)
	}
	if empty.AdvisoryHints == nil {
		t.Fatal("存在する空 section が null になった")
	}
	if *empty.AdvisoryHints != "" {
		t.Fatalf("空 section = %q, want 空 string", *empty.AdvisoryHints)
	}
}

func TestManifestAndPolicyParseWithExternalYAMLParser(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	_, manifestPayload, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatal(err)
	}
	_, policyPayload, err := EncodeExecutionPolicy(sampleExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}

	manifest := decodeOracle[struct {
		Schema      string  `json:"schema"`
		BaseSHA     string  `json:"baseSha"`
		Parent      *string `json:"parent"`
		TaskContext struct {
			Schema string `json:"schema"`
			Digest string `json:"digest"`
		} `json:"taskContext"`
		Dependencies []struct {
			Issue            string `json:"issue"`
			CompletionDigest string `json:"completionDigest"`
		} `json:"dependencies"`
		AuthorityRefs []struct {
			Ref           string `json:"ref"`
			ContentDigest string `json:"contentDigest"`
		} `json:"authorityRefs"`
	}](t, yamlOracle(t, manifestPayload.Data))

	if manifest.Schema != ContextManifestSchemaV1Alpha1 ||
		manifest.TaskContext.Digest != string(compiled.TaskContextRef.Digest) {
		t.Fatalf("manifest が復元されない: %+v", manifest)
	}
	if len(manifest.Dependencies) != len(compiled.ClaimRequirements.DependsOn) {
		t.Fatalf("dependencies 件数 = %d", len(manifest.Dependencies))
	}
	for i, dependency := range manifest.Dependencies {
		if dependency.Issue != compiled.ClaimRequirements.DependsOn[i].String() {
			t.Fatalf("dependencies[%d] = %q", i, dependency.Issue)
		}
	}
	for i, authority := range manifest.AuthorityRefs {
		if authority.Ref != compiled.ClaimRequirements.AuthorityRefs[i].String() {
			t.Fatalf("authorityRefs[%d] = %q", i, authority.Ref)
		}
	}

	policy := decodeOracle[struct {
		Schema      string `json:"schema"`
		IssueWorker struct {
			Provider        string   `json:"provider"`
			ToolPermissions []string `json:"toolPermissions"`
			Timeout         string   `json:"timeout"`
		} `json:"issueWorker"`
	}](t, yamlOracle(t, policyPayload.Data))

	if policy.Schema != ExecutionPolicySchemaV1Alpha1 || policy.IssueWorker.Provider != "codex" {
		t.Fatalf("policy が復元されない: %+v", policy)
	}
	if !reflect.DeepEqual(policy.IssueWorker.ToolPermissions, []string{"github:write", "repository:write"}) {
		t.Fatalf("toolPermissions が lexicographic 順で復元されない: %v", policy.IssueWorker.ToolPermissions)
	}
	if policy.IssueWorker.Timeout != "45m0s" {
		t.Fatalf("timeout = %q", policy.IssueWorker.Timeout)
	}
}

// Escalation Policy の round 上限は decimal string として encode する。
// implicit int にすると YAML 実装ごとの数値表現の差が canonical bytes へ漏れるため、
// 独立実装が string として読み戻せることを差分オラクルで確認する。
func TestEscalationPolicyParsesWithExternalYAMLParser(t *testing.T) {
	_, payload, err := EncodeEscalationPolicy(sampleEscalationPolicy())
	if err != nil {
		t.Fatal(err)
	}

	policy := decodeOracle[struct {
		Schema       string `json:"schema"`
		ReviewRounds struct {
			TestValidity        string `json:"testValidity"`
			FinalImplementation string `json:"finalImplementation"`
		} `json:"reviewRounds"`
	}](t, yamlOracle(t, payload.Data))

	if policy.Schema != EscalationPolicySchemaV1Alpha1 {
		t.Fatalf("schema = %q", policy.Schema)
	}
	if policy.ReviewRounds.TestValidity != "3" || policy.ReviewRounds.FinalImplementation != "3" {
		t.Fatalf("review round 上限が復元されない: %+v", policy.ReviewRounds)
	}
}

// oracleRef は versioned ref の入れ子 mapping を表す。
type oracleRef struct {
	Schema string `json:"schema"`
	Digest string `json:"digest"`
}

// Operation / Review の identity bytes も digest 入力として保存・監査されるため、
// golden fixture だけでなく独立実装で parse できることを確認する。
func TestProtocolIdentityParsesWithExternalYAMLParser(t *testing.T) {
	op := sampleWorkerOperation(t)
	operation := decodeOracle[struct {
		Schema          string     `json:"schema"`
		Kind            string     `json:"kind"`
		Run             string     `json:"run"`
		Repository      string     `json:"repository"`
		Issue           string     `json:"issue"`
		ContextManifest *oracleRef `json:"contextManifest"`
		ExecutionPolicy oracleRef  `json:"executionPolicy"`
		HeadSHA         *string    `json:"headSha"`
		InputArtifacts  []string   `json:"inputArtifacts"`
		PolicyRefs      []string   `json:"policyRefs"`
		Causation       string     `json:"causation"`
	}](t, yamlOracle(t, encodeOperationIdentity(op)))

	if operation.Schema != WorkerOperationSchemaV1Alpha1 || operation.Kind != string(OperationAuthorTests) {
		t.Fatalf("Operation identity が復元されない: %+v", operation)
	}
	if operation.Repository != op.Issue.repositoryURL() || operation.Issue != op.Issue.String() {
		t.Fatalf("repository/issue = %q / %q", operation.Repository, operation.Issue)
	}
	if operation.ContextManifest == nil || *operation.ContextManifest != (oracleRef{
		Schema: op.ContextManifest.Schema,
		Digest: string(op.ContextManifest.Digest),
	}) {
		t.Fatalf("contextManifest = %+v", operation.ContextManifest)
	}
	if operation.HeadSHA == nil || *operation.HeadSHA != op.HeadSHA {
		t.Fatalf("head = %v", operation.HeadSHA)
	}
	if operation.Causation != op.CausationID || len(operation.PolicyRefs) != len(op.PolicyRefs) {
		t.Fatalf("causation/policyRefs = %q / %v", operation.Causation, operation.PolicyRefs)
	}

	// claim は Context Manifest と base/head を null として区別できなければならない
	claim := decodeOracle[struct {
		ContextManifest *oracleRef `json:"contextManifest"`
		HeadSHA         *string    `json:"headSha"`
		InputArtifacts  []string   `json:"inputArtifacts"`
	}](t, yamlOracle(t, encodeOperationIdentity(sampleClaimOperation(t))))
	if claim.ContextManifest != nil || claim.HeadSHA != nil {
		t.Fatalf("claim の未解決 field が null として復元されない: %+v", claim)
	}
	if claim.InputArtifacts == nil || len(claim.InputArtifacts) != 0 {
		t.Fatalf("空 list が %v として復元された", claim.InputArtifacts)
	}

	// stale_input Result は「どの field が変わったか」を独立した list として復元できる
	stale := decodeOracle[struct {
		Outcome            string   `json:"outcome"`
		HeadSHA            *string  `json:"headSha"`
		ChangedInputFields []string `json:"changedInputFields"`
	}](t, yamlOracle(t, encodeOperationResultIdentity(sampleStaleOperationResult(t, op))))
	if stale.Outcome != string(OutcomeStaleInput) || stale.HeadSHA != nil {
		t.Fatalf("stale_input Result が復元されない: %+v", stale)
	}
	if !reflect.DeepEqual(stale.ChangedInputFields, []string{"executionPolicy", "headSha"}) {
		t.Fatalf("changedInputFields = %v", stale.ChangedInputFields)
	}

	// succeeded Result の output は logical name で引く table として復元できなければならない。
	// scalar list のままなら Controller は「何を残したか」を digest からしか判断できない。
	succeeded := decodeOracle[struct {
		OutputArtifacts []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"outputArtifacts"`
	}](t, yamlOracle(t, encodeOperationResultIdentity(sampleOperationResult(t, op))))
	gotOutputs := make([]string, len(succeeded.OutputArtifacts))
	for i, artifact := range succeeded.OutputArtifacts {
		if artifact.Digest == "" {
			t.Fatalf("outputArtifacts[%d] の digest が復元されない: %+v", i, artifact)
		}
		gotOutputs[i] = artifact.Name
	}
	if !reflect.DeepEqual(gotOutputs, []string{"red-evidence", "source-bundle", "test-plan"}) {
		t.Fatalf("outputArtifacts が logical name 順の table として復元されない: %v", gotOutputs)
	}

	req := sampleReviewRequest(t)
	request := decodeOracle[struct {
		Schema           string    `json:"schema"`
		Kind             string    `json:"kind"`
		HeadSHA          string    `json:"headSha"`
		ArtifactManifest oracleRef `json:"artifactManifest"`
		PolicyRefs       []string  `json:"policyRefs"`
	}](t, yamlOracle(t, encodeReviewRequestIdentity(req)))
	if request.Schema != ReviewRequestSchemaV1Alpha1 || request.Kind != string(req.Kind) || request.HeadSHA != req.HeadSHA {
		t.Fatalf("Review Request identity が復元されない: %+v", request)
	}
	if request.ArtifactManifest.Digest != string(req.ArtifactManifest.Digest) {
		t.Fatalf("artifactManifest = %+v", request.ArtifactManifest)
	}

	result := decodeOracle[struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			ID           string   `json:"id"`
			Severity     string   `json:"severity"`
			Summary      string   `json:"summary"`
			EvidenceRefs []string `json:"evidenceRefs"`
		} `json:"findings"`
	}](t, yamlOracle(t, encodeReviewResultIdentity(sampleReviewResult(t, req))))
	if result.Verdict != string(VerdictRequestChanges) || len(result.Findings) != 1 {
		t.Fatalf("Review Result identity が復元されない: %+v", result)
	}
	if result.Findings[0].Severity != string(SeverityBlocking) || result.Findings[0].Summary != "AC-2 を検証する test が無い" {
		t.Fatalf("finding が escape 前の値へ戻らない: %+v", result.Findings[0])
	}

	// PR observation は canonical protocol で初めて implicit bool scalar を使う。
	// 独立 parser が文字列ではなく bool として復元できることを固定する。
	observation := samplePullRequestObservation()
	parsedObservation := decodeOracle[struct {
		Schema      string `json:"schema"`
		PullRequest string `json:"pullRequest"`
		State       string `json:"state"`
		Draft       bool   `json:"draft"`
		HeadSHA     string `json:"headSha"`
		BaseRef     string `json:"baseRef"`
		BodyDigest  string `json:"bodyDigest"`
	}](t, yamlOracle(t, encodePullRequestObservation(observation)))
	if parsedObservation.Schema != PullRequestObservationSchemaV1Alpha1 ||
		parsedObservation.PullRequest != observation.PullRequest.String() ||
		parsedObservation.State != string(observation.State) ||
		!parsedObservation.Draft ||
		parsedObservation.HeadSHA != observation.HeadSHA ||
		parsedObservation.BaseRef != observation.BaseRef ||
		parsedObservation.BodyDigest != string(observation.BodyDigest) {
		t.Fatalf("Pull Request Observation が復元されない: %+v", parsedObservation)
	}

	// final Result の perspectives は nested mapping の list と bool を組み合わせる。
	// golden fixture との自己比較だけでなく、外部 parser で構造と canonical 順を確認する。
	finalRequest := sampleFinalReviewRequest(t)
	parsedFinalResult := decodeOracle[struct {
		Verdict      string `json:"verdict"`
		Perspectives []struct {
			Perspective  string   `json:"perspective"`
			Applicable   bool     `json:"applicable"`
			Reason       string   `json:"reason"`
			EvidenceRefs []string `json:"evidenceRefs"`
		} `json:"perspectives"`
		Findings []struct{} `json:"findings"`
	}](t, yamlOracle(t, encodeReviewResultIdentity(sampleFinalReviewResult(t, finalRequest))))
	wantPerspectives := []string{"accessibility", "performance", "type-design", "ux"}
	gotPerspectives := make([]string, len(parsedFinalResult.Perspectives))
	for i, perspective := range parsedFinalResult.Perspectives {
		if perspective.Reason == "" || len(perspective.EvidenceRefs) == 0 {
			t.Fatalf("perspectives[%d] の根拠が復元されない: %+v", i, perspective)
		}
		gotPerspectives[i] = perspective.Perspective
	}
	if parsedFinalResult.Verdict != string(VerdictApprove) ||
		!reflect.DeepEqual(gotPerspectives, wantPerspectives) ||
		len(parsedFinalResult.Findings) != 0 {
		t.Fatalf("final Review Result が復元されない: %+v", parsedFinalResult)
	}
	if parsedFinalResult.Perspectives[0].Applicable ||
		parsedFinalResult.Perspectives[1].Applicable ||
		!parsedFinalResult.Perspectives[2].Applicable ||
		parsedFinalResult.Perspectives[3].Applicable {
		t.Fatalf("applicable bool が復元されない: %+v", parsedFinalResult.Perspectives)
	}

	manifest := sampleArtifactManifest(t)
	_, manifestPayload, err := EncodeArtifactManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := decodeOracle[struct {
		Schema  string `json:"schema"`
		Entries []struct {
			Name      string `json:"name"`
			MediaType string `json:"mediaType"`
			Length    string `json:"length"`
			Digest    string `json:"digest"`
		} `json:"entries"`
	}](t, yamlOracle(t, manifestPayload.Data))
	if artifacts.Schema != ArtifactManifestSchemaV1Alpha1 || len(artifacts.Entries) != len(manifest.Entries) {
		t.Fatalf("Artifact Manifest が復元されない: %+v", artifacts)
	}
	// length は implicit int にせず decimal string として復元される
	if artifacts.Entries[0].Name != "context-manifest" || artifacts.Entries[0].Length == "" {
		t.Fatalf("entries[0] = %+v", artifacts.Entries[0])
	}
}
