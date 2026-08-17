package contract

import (
	"strings"
	"testing"
	"time"
)

const (
	sampleHeadSHA = "89abcdef0123456789abcdef0123456789abcdef"
	sampleNextSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

var sampleCreatedAt = time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

func sampleResolvedInput(t *testing.T) (*CompiledIssue, ContextManifestRef, ExecutionPolicyRef) {
	t.Helper()
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	manifestRef, _, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatalf("manifest encode error: %v", err)
	}
	policyRef, _, err := EncodeExecutionPolicy(sampleExecutionPolicy())
	if err != nil {
		t.Fatalf("policy encode error: %v", err)
	}
	return compiled, manifestRef, policyRef
}

func sampleWorkerOperation(t *testing.T) WorkerOperation {
	t.Helper()
	compiled, manifestRef, policyRef := sampleResolvedInput(t)
	observation := compiled.ObservationRef
	return WorkerOperation{
		Schema:          WorkerOperationSchemaV1Alpha1,
		OperationID:     "op-01",
		RunID:           "run-01",
		Kind:            OperationAuthorTests,
		Issue:           compiled.TaskContext.Issue,
		Observation:     &observation,
		ContextManifest: &manifestRef,
		ExecutionPolicy: policyRef,
		HeadSHA:         sampleHeadSHA,
		InputArtifacts:  []Digest{SHA256([]byte("implementation-brief"))},
		PolicyRefs:      []string{"docs/github-routing.md"},
		CausationID:     "transition-01",
		CreatedAt:       sampleCreatedAt,
	}
}

func sampleClaimOperation(t *testing.T) WorkerOperation {
	t.Helper()
	_, _, policyRef := sampleResolvedInput(t)
	return WorkerOperation{
		Schema:          WorkerOperationSchemaV1Alpha1,
		OperationID:     "op-00",
		RunID:           "run-01",
		Kind:            OperationClaim,
		Issue:           IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 10},
		ExecutionPolicy: policyRef,
		CausationID:     "transition-00",
		CreatedAt:       sampleCreatedAt,
	}
}

func sampleOperationResult(t *testing.T, op WorkerOperation) OperationResult {
	t.Helper()
	digest, err := OperationDigest(op)
	if err != nil {
		t.Fatalf("operation digest error: %v", err)
	}
	return OperationResult{
		Schema:          WorkerResultSchemaV1Alpha1,
		OperationDigest: digest,
		AttemptID:       "attempt-01",
		Outcome:         OutcomeSucceeded,
		HeadSHA:         sampleNextSHA,
		OutputArtifacts: []Digest{SHA256([]byte("test-plan")), SHA256([]byte("red-evidence"))},
		CompletedAt:     sampleCreatedAt.Add(time.Minute),
	}
}

// sampleStaleOperationResult は semantic input が変わって止まった attempt の Result を返す。
func sampleStaleOperationResult(t *testing.T, op WorkerOperation) OperationResult {
	t.Helper()
	result := sampleOperationResult(t, op)
	result.Outcome = OutcomeStaleInput
	result.HeadSHA = ""
	result.OutputArtifacts = nil
	result.ChangedInputFields = []string{fieldHeadSHA, fieldExecutionPolicy}
	return result
}

func requireOperationDigest(t *testing.T, op WorkerOperation) Digest {
	t.Helper()
	digest, err := OperationDigest(op)
	if err != nil {
		t.Fatalf("operation digest error: %v", err)
	}
	return digest
}

func TestWorkerOperationCanonicalGolden(t *testing.T) {
	op := sampleWorkerOperation(t)
	requireGolden(t, encodeOperationIdentity(op), "worker-operation.yaml")
	requireGolden(t, encodeOperationIdentity(sampleClaimOperation(t)), "worker-operation-claim.yaml")
	requireGolden(t, encodeOperationResultIdentity(sampleOperationResult(t, op)), "worker-result.yaml")
	requireGolden(t, encodeOperationResultIdentity(sampleStaleOperationResult(t, op)), "worker-result-stale.yaml")

	identity := string(encodeOperationIdentity(op))
	for _, excluded := range []string{"issueObservation", "bodyDigest", "observedAt", "operationId", "createdAt", "attempt"} {
		if strings.Contains(identity, excluded) {
			t.Fatalf("Operation identity に %q が含まれる", excluded)
		}
	}
}

// audit lineage と実行時 metadata は content identity へ混入させない。
// 混入させると、HTML comment 編集や retry のたびに別 Operation になり、
// 既存の approval と queue の重複判定が壊れる。
func TestOperationDigestExcludesAuditAndAttemptFields(t *testing.T) {
	op := sampleWorkerOperation(t)
	want := requireOperationDigest(t, op)

	changedObservation := *op.Observation
	changedObservation.Digest = SHA256([]byte("別 raw body"))

	variants := map[string]func(*WorkerOperation){
		"issue observation": func(o *WorkerOperation) { o.Observation = &changedObservation },
		"operation id":      func(o *WorkerOperation) { o.OperationID = "op-02" },
		"created at":        func(o *WorkerOperation) { o.CreatedAt = sampleCreatedAt.Add(time.Hour) },
		"issue reference case": func(o *WorkerOperation) {
			o.Issue = IssueRef{Owner: "MrBaron3", Repository: "Kudo", Number: o.Issue.Number}
		},
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := sampleWorkerOperation(t)
			mutate(&got)
			if digest := requireOperationDigest(t, got); digest != want {
				t.Fatalf("identity に寄与しない差分で digest が変化: got %s, want %s", digest, want)
			}
		})
	}
}

// digest list と policy ref list は順序を持たない集合であり、
// 再解決で並びが変わっただけで別 Operation にしない。
func TestOperationDigestCanonicalizesSetOrder(t *testing.T) {
	ordered := sampleWorkerOperation(t)
	ordered.InputArtifacts = []Digest{SHA256([]byte("a")), SHA256([]byte("b"))}
	ordered.PolicyRefs = []string{"docs/github-routing.md", "docs/workflow.md"}

	reversed := sampleWorkerOperation(t)
	reversed.InputArtifacts = []Digest{SHA256([]byte("b")), SHA256([]byte("a"))}
	reversed.PolicyRefs = []string{"docs/workflow.md", "docs/github-routing.md"}

	if requireOperationDigest(t, ordered) != requireOperationDigest(t, reversed) {
		t.Fatal("集合の並び替えで Operation digest が変化")
	}
}

func TestOperationDigestCoversSemanticIdentity(t *testing.T) {
	base := sampleWorkerOperation(t)
	baseDigest := requireOperationDigest(t, base)

	otherManifest := *base.ContextManifest
	otherManifest.Digest = SHA256([]byte("別 manifest"))
	sameDigestOtherSchema := *base.ContextManifest
	sameDigestOtherSchema.Schema = "kudo.context-manifest/v1beta1"
	otherPolicy := base.ExecutionPolicy
	otherPolicy.Digest = SHA256([]byte("別 policy"))

	variants := map[string]func(*WorkerOperation){
		"kind":                 func(o *WorkerOperation) { o.Kind = OperationReviseTests },
		"run":                  func(o *WorkerOperation) { o.RunID = "run-02" },
		"issue":                func(o *WorkerOperation) { o.Issue.Number = 11 },
		"context manifest":     func(o *WorkerOperation) { o.ContextManifest = &otherManifest },
		"manifest ref schema":  func(o *WorkerOperation) { o.ContextManifest = &sameDigestOtherSchema },
		"execution policy":     func(o *WorkerOperation) { o.ExecutionPolicy = otherPolicy },
		"head sha":             func(o *WorkerOperation) { o.HeadSHA = sampleNextSHA },
		"input artifacts":      func(o *WorkerOperation) { o.InputArtifacts = []Digest{SHA256([]byte("別 input"))} },
		"input artifact count": func(o *WorkerOperation) { o.InputArtifacts = nil },
		"policy refs":          func(o *WorkerOperation) { o.PolicyRefs = []string{"docs/workflow.md"} },
		"causation":            func(o *WorkerOperation) { o.CausationID = "transition-02" },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := sampleWorkerOperation(t)
			mutate(&got)
			if requireOperationDigest(t, got) == baseDigest {
				t.Fatal("semantic identity の変更で Operation digest が変わらない")
			}
		})
	}
}

func TestWorkerOperationValidation(t *testing.T) {
	tests := map[string]func(*WorkerOperation){
		"schema":              func(o *WorkerOperation) { o.Schema = "kudo.worker-operation/v2" },
		"unknown kind":        func(o *WorkerOperation) { o.Kind = "author_implementation" },
		"empty kind":          func(o *WorkerOperation) { o.Kind = "" },
		"operation id":        func(o *WorkerOperation) { o.OperationID = "" },
		"run id":              func(o *WorkerOperation) { o.RunID = "run 01" },
		"causation id":        func(o *WorkerOperation) { o.CausationID = "" },
		"issue":               func(o *WorkerOperation) { o.Issue = IssueRef{} },
		"created at":          func(o *WorkerOperation) { o.CreatedAt = time.Time{} },
		"missing observation": func(o *WorkerOperation) { o.Observation = nil },
		"missing manifest":    func(o *WorkerOperation) { o.ContextManifest = nil },
		"observation schema": func(o *WorkerOperation) {
			ref := IssueObservationRef{Schema: "kudo.task-context/v1alpha1", Digest: o.Observation.Digest}
			o.Observation = &ref
		},
		"manifest schema": func(o *WorkerOperation) {
			ref := ContextManifestRef{Schema: "kudo.execution-policy/v1alpha1", Digest: o.ContextManifest.Digest}
			o.ContextManifest = &ref
		},
		"manifest digest": func(o *WorkerOperation) {
			ref := ContextManifestRef{Schema: o.ContextManifest.Schema, Digest: "sha256:short"}
			o.ContextManifest = &ref
		},
		"policy schema":      func(o *WorkerOperation) { o.ExecutionPolicy.Schema = "kudo.context-manifest/v1alpha1" },
		"head sha":           func(o *WorkerOperation) { o.HeadSHA = "" },
		"artifact digest":    func(o *WorkerOperation) { o.InputArtifacts = []Digest{"sha256:nope"} },
		"duplicate artifact": func(o *WorkerOperation) { o.InputArtifacts = []Digest{SHA256([]byte("a")), SHA256([]byte("a"))} },
		"policy ref path":    func(o *WorkerOperation) { o.PolicyRefs = []string{"/etc/passwd"} },
		"policy ref escape":  func(o *WorkerOperation) { o.PolicyRefs = []string{"../secrets.md"} },
		"duplicate policy ref": func(o *WorkerOperation) {
			o.PolicyRefs = []string{"docs/workflow.md", "docs/workflow.md"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := sampleWorkerOperation(t)
			mutate(&got)
			if err := ValidateWorkerOperation(got); err == nil {
				t.Fatal("invalid Operation を受理した")
			}
			if _, err := OperationDigest(got); err == nil {
				t.Fatal("invalid Operation の digest を返した")
			}
		})
	}

	if err := ValidateWorkerOperation(sampleWorkerOperation(t)); err != nil {
		t.Fatalf("valid Operation を拒否した: %v", err)
	}
}

// claim は Issue Observation、Context Manifest、base/head を作る Operation であり、
// 開始時点ではまだ存在しない。空文字や直前 Run の値から推測させないため、
// 該当 field を持つ claim envelope は拒否する。
func TestClaimOperationRejectsContextFields(t *testing.T) {
	claim := sampleClaimOperation(t)
	if err := ValidateWorkerOperation(claim); err != nil {
		t.Fatalf("valid claim を拒否した: %v", err)
	}

	compiled, manifestRef, _ := sampleResolvedInput(t)
	observation := compiled.ObservationRef
	tests := map[string]func(*WorkerOperation){
		"observation":     func(o *WorkerOperation) { o.Observation = &observation },
		"manifest":        func(o *WorkerOperation) { o.ContextManifest = &manifestRef },
		"head sha":        func(o *WorkerOperation) { o.HeadSHA = sampleHeadSHA },
		"input artifacts": func(o *WorkerOperation) { o.InputArtifacts = []Digest{SHA256([]byte("a"))} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := sampleClaimOperation(t)
			mutate(&got)
			if err := ValidateWorkerOperation(got); err == nil {
				t.Fatal("claim がまだ持てない field を受理した")
			}
		})
	}
}

// 先行 artifact を必須入力とする kind では、artifact 参照の欠落を受理しない。
func TestOperationKindRequiresDeclaredInputArtifacts(t *testing.T) {
	requiring := []OperationKind{
		OperationReviseTests,
		OperationImplement,
		OperationRepairImplementation,
		OperationCreatePullRequest,
	}
	for _, kind := range requiring {
		t.Run(string(kind), func(t *testing.T) {
			got := sampleWorkerOperation(t)
			got.Kind = kind
			got.InputArtifacts = nil
			if err := ValidateWorkerOperation(got); err == nil {
				t.Fatal("必須 input artifact の欠落を受理した")
			}
		})
	}

	optional := sampleWorkerOperation(t)
	optional.Kind = OperationAuthorTests
	optional.InputArtifacts = nil
	if err := ValidateWorkerOperation(optional); err != nil {
		t.Fatalf("author_tests の空 input artifact を拒否した: %v", err)
	}
}

func TestBindOperationResult(t *testing.T) {
	op := sampleWorkerOperation(t)
	result := sampleOperationResult(t, op)
	if err := BindOperationResult(op, result); err != nil {
		t.Fatalf("対応する Result を拒否した: %v", err)
	}

	// Result の head は Operation が新しく固定した head であり、
	// 入力 head と一致しないことが正常である。
	if result.HeadSHA == op.HeadSHA {
		t.Fatal("test 前提が壊れている: Result head と入力 head が同じ")
	}

	changes := map[string]func(*WorkerOperation){
		"context manifest": func(o *WorkerOperation) {
			ref := ContextManifestRef{Schema: o.ContextManifest.Schema, Digest: SHA256([]byte("別 manifest"))}
			o.ContextManifest = &ref
		},
		"execution policy": func(o *WorkerOperation) { o.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
		"head":             func(o *WorkerOperation) { o.HeadSHA = sampleNextSHA },
		"input artifact":   func(o *WorkerOperation) { o.InputArtifacts = []Digest{SHA256([]byte("別 input"))} },
		"policy ref":       func(o *WorkerOperation) { o.PolicyRefs = []string{"docs/workflow.md"} },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			changed := sampleWorkerOperation(t)
			mutate(&changed)
			if err := BindOperationResult(changed, result); err == nil {
				t.Fatal("semantic identity が変わった Operation へ古い Result を bind した")
			}
		})
	}

	t.Run("observation only", func(t *testing.T) {
		// audit lineage の差分は binding を壊さない
		changed := sampleWorkerOperation(t)
		ref := IssueObservationRef{Schema: changed.Observation.Schema, Digest: SHA256([]byte("別 raw body"))}
		changed.Observation = &ref
		if err := BindOperationResult(changed, result); err != nil {
			t.Fatalf("Observation 差分だけで binding が壊れた: %v", err)
		}
	})

	t.Run("invalid result", func(t *testing.T) {
		invalid := result
		invalid.AttemptID = ""
		if err := BindOperationResult(op, invalid); err == nil {
			t.Fatal("invalid Result を bind した")
		}
	})

	t.Run("claim result head", func(t *testing.T) {
		claim := sampleClaimOperation(t)
		claimResult := sampleOperationResult(t, claim)
		if err := BindOperationResult(claim, claimResult); err == nil {
			t.Fatal("head を持たない claim の Result が head を報告した")
		}
		claimResult.HeadSHA = ""
		if err := BindOperationResult(claim, claimResult); err != nil {
			t.Fatalf("claim Result を拒否した: %v", err)
		}
	})

	t.Run("succeeded without head", func(t *testing.T) {
		noHead := result
		noHead.HeadSHA = ""
		if err := BindOperationResult(op, noHead); err == nil {
			t.Fatal("head を固定しない succeeded を受理した")
		}
	})
}

// 同じ logical Operation の retry は attempt だけが変わり、Operation identity は変わらない。
func TestOperationAndAttemptIdentityAreSeparate(t *testing.T) {
	op := sampleWorkerOperation(t)
	first := sampleOperationResult(t, op)
	second := first
	second.AttemptID = "attempt-02"
	second.CompletedAt = first.CompletedAt.Add(time.Hour)

	if first.OperationDigest != second.OperationDigest {
		t.Fatal("attempt が変わると Operation digest が変わる")
	}
	if err := BindOperationResult(op, second); err != nil {
		t.Fatalf("新しい attempt の Result を拒否した: %v", err)
	}
	firstDigest, err := OperationResultDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := OperationResultDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("attempt ID と完了時刻が Result の content identity へ混入している")
	}
}

func TestOperationResultValidation(t *testing.T) {
	op := sampleWorkerOperation(t)
	valid := sampleOperationResult(t, op)

	tests := map[string]func(*OperationResult){
		"schema":           func(r *OperationResult) { r.Schema = "kudo.review-result/v1alpha1" },
		"operation digest": func(r *OperationResult) { r.OperationDigest = "sha256:nope" },
		"attempt id":       func(r *OperationResult) { r.AttemptID = "attempt 01" },
		"unknown outcome":  func(r *OperationResult) { r.Outcome = "retry" },
		"empty outcome":    func(r *OperationResult) { r.Outcome = "" },
		"head sha":         func(r *OperationResult) { r.HeadSHA = "not-a-sha" },
		"completed at":     func(r *OperationResult) { r.CompletedAt = time.Time{} },
		"artifact digest":  func(r *OperationResult) { r.OutputArtifacts = []Digest{"sha256:nope"} },
		"duplicate artifact": func(r *OperationResult) {
			r.OutputArtifacts = []Digest{SHA256([]byte("a")), SHA256([]byte("a"))}
		},
		"external ref": func(r *OperationResult) { r.ExternalRefs = []string{"github://owner/repo/pull/1\n"} },
		"duplicate external ref": func(r *OperationResult) {
			r.ExternalRefs = []string{"github://owner/repo/pull/1", "github://owner/repo/pull/1"}
		},
		"changed fields without stale outcome": func(r *OperationResult) {
			r.ChangedInputFields = []string{"headSha"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := valid
			mutate(&got)
			if err := ValidateOperationResult(got); err == nil {
				t.Fatal("invalid Result を受理した")
			}
		})
	}

	if err := ValidateOperationResult(valid); err != nil {
		t.Fatalf("valid Result を拒否した: %v", err)
	}
}

// `stale_input`は「どの field が変わったか」を Result へ載せる。載せる場所が無いと、
// Controller は他の outcome と区別のつかない Result を受け取り、comparison の結果を
// 別経路で持ち回るしかなくなる。field 名の語彙は comparison の出力と共有する。
func TestStaleInputResultRecordsChangedFields(t *testing.T) {
	op := sampleWorkerOperation(t)
	latest := latestFromOperation(op)
	latest.HeadSHA = sampleNextSHA
	latest.ExecutionPolicy.Digest = SHA256([]byte("別 policy"))
	diff := requireOperationComparison(t, op, latest)

	stale := sampleOperationResult(t, op)
	stale.Outcome = OutcomeStaleInput
	stale.HeadSHA = ""
	stale.OutputArtifacts = nil
	stale.ChangedInputFields = diff.ChangedFields
	if err := ValidateOperationResult(stale); err != nil {
		t.Fatalf("comparison が返した changed fields を持つ Result を拒否した: %v", err)
	}
	if err := BindOperationResult(op, stale); err != nil {
		t.Fatalf("stale_input Result を bind できない: %v", err)
	}

	invalid := map[string][]string{
		"空":           nil,
		"未知の field":   {"baseSha"},
		"重複":          {"headSha", "headSha"},
		"observation": {"issueObservation"},
	}
	for name, fields := range invalid {
		t.Run(name, func(t *testing.T) {
			got := stale
			got.ChangedInputFields = fields
			if err := ValidateOperationResult(got); err == nil {
				t.Fatal("記録できない changedInputFields を受理した")
			}
		})
	}

	// 何が変わったかは Result の content identity である。順序は identity ではない。
	other := stale
	other.ChangedInputFields = []string{"headSha"}
	staleDigest, err := OperationResultDigest(stale)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest, err := OperationResultDigest(other)
	if err != nil {
		t.Fatal(err)
	}
	if staleDigest == otherDigest {
		t.Fatal("changed fields の違いが Result identity へ反映されない")
	}

	reordered := stale
	reordered.ChangedInputFields = []string{"headSha", "executionPolicy"}
	reorderedDigest, err := OperationResultDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedDigest != staleDigest {
		t.Fatal("changed fields の並び替えで Result identity が変化")
	}
}

// attempt failure は verdict や Result と同じ field で表現しない、を Operation 側でも
// 型で固定する。protocol 文書は両 role へ同じ規律を課している。
func TestOperationAttemptOutcomeSeparatesResultAndFailure(t *testing.T) {
	result := sampleOperationResult(t, sampleWorkerOperation(t))
	failure := sampleAttemptFailure()

	if err := (OperationAttemptOutcome{Result: &result}).Validate(); err != nil {
		t.Fatalf("Result だけを持つ attempt outcome を拒否した: %v", err)
	}
	if err := (OperationAttemptOutcome{Failure: &failure}).Validate(); err != nil {
		t.Fatalf("failure だけを持つ attempt outcome を拒否した: %v", err)
	}
	if err := (OperationAttemptOutcome{Result: &result, Failure: &failure}).Validate(); err == nil {
		t.Fatal("Result と failure を同時に持つ attempt outcome を受理した")
	}
	if err := (OperationAttemptOutcome{}).Validate(); err == nil {
		t.Fatal("結末を持たない attempt outcome を受理した")
	}
}

// succeeded は「Operation contract を満たす output が immutable に固定された」ことを意味する。
// output を何も残さない succeeded を通すと、後続 gate は存在しない artifact を前提に進む。
func TestSucceededResultRequiresKindOutput(t *testing.T) {
	producing := []OperationKind{
		OperationClaim,
		OperationAuthorTests,
		OperationReviseTests,
		OperationImplement,
		OperationRepairImplementation,
	}
	for _, kind := range producing {
		t.Run(string(kind), func(t *testing.T) {
			op := sampleWorkerOperation(t)
			op.Kind = kind
			if kind == OperationClaim {
				op = sampleClaimOperation(t)
			}
			result := sampleOperationResult(t, op)
			if kind == OperationClaim {
				result.HeadSHA = ""
			}
			if err := BindOperationResult(op, result); err != nil {
				t.Fatalf("output artifact を持つ succeeded を拒否した: %v", err)
			}

			empty := result
			empty.OutputArtifacts = nil
			if err := BindOperationResult(op, empty); err == nil {
				t.Fatal("output artifact を持たない succeeded を受理した")
			}

			// terminal な非 succeeded は output を持たなくてよい
			staleResult := sampleStaleOperationResult(t, op)
			if err := BindOperationResult(op, staleResult); err != nil {
				t.Fatalf("stale_input Result を拒否した: %v", err)
			}
		})
	}
}

// create_pull_request は source head を進めない Operation であり、承認済み head をそのまま
// 観測して PR を作る。Result が別の head を報告できると、review 済みでない head に対する
// PR が gate を通ってしまう。作成した PR の reference も succeeded の必須出力である。
func TestCreatePullRequestBindsApprovedHeadAndReference(t *testing.T) {
	op := sampleWorkerOperation(t)
	op.Kind = OperationCreatePullRequest

	result := sampleOperationResult(t, op)
	result.HeadSHA = op.HeadSHA
	result.OutputArtifacts = nil
	result.ExternalRefs = []string{"github://mrbaron3/kudo/pull/42"}
	if err := BindOperationResult(op, result); err != nil {
		t.Fatalf("承認済み head と PR reference を持つ Result を拒否した: %v", err)
	}

	movedHead := result
	movedHead.HeadSHA = sampleNextSHA
	if err := BindOperationResult(op, movedHead); err == nil {
		t.Fatal("承認済み head と異なる head を報告した Result を受理した")
	}

	noReference := result
	noReference.ExternalRefs = nil
	if err := BindOperationResult(op, noReference); err == nil {
		t.Fatal("PR reference を持たない succeeded を受理した")
	}

	// 他の kind は新しい head を固定してよい
	authoring := sampleWorkerOperation(t)
	if err := BindOperationResult(authoring, sampleOperationResult(t, authoring)); err != nil {
		t.Fatalf("head を進める kind の Result を拒否した: %v", err)
	}
}
