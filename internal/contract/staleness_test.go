package contract

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// recompile は同じ Issue identity で body を compile し直し、Context Manifest まで
// 解決し直した「最新入力」を返す。Controller が model-bearing Operation 開始前に
// 行う再取得と同じ経路である。
func recompile(t *testing.T, body string) (*CompiledIssue, ContextManifestRef) {
	t.Helper()
	compiled := requireCompiled(t, body)
	ref, _, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatalf("manifest encode error: %v", err)
	}
	return compiled, ref
}

func latestFromOperation(op WorkerOperation) LatestOperationInput {
	return LatestOperationInput{
		Observation:     *op.Observation,
		ContextManifest: *op.ContextManifest,
		ExecutionPolicy: op.ExecutionPolicy,
		HeadSHA:         op.HeadSHA,
		InputArtifacts:  append([]Digest(nil), op.InputArtifacts...),
		PolicyRefs:      append([]string(nil), op.PolicyRefs...),
	}
}

func latestFromRequest(req ReviewRequest) LatestReviewInput {
	return LatestReviewInput{
		Observation:            req.Observation,
		PullRequestObservation: req.PullRequestObservation,
		ContextManifest:        req.ContextManifest,
		ExecutionPolicy:        req.ExecutionPolicy,
		HeadSHA:                req.HeadSHA,
		PullRequest:            req.PullRequest,
		ArtifactManifest:       req.ArtifactManifest,
		PolicyRefs:             append([]string(nil), req.PolicyRefs...),
	}
}

func requireOperationComparison(t *testing.T, op WorkerOperation, latest LatestOperationInput) SemanticDifference {
	t.Helper()
	got, err := CompareOperationInput(op, latest)
	if err != nil {
		t.Fatalf("compare error: %v", err)
	}
	return got
}

func requireReviewComparison(t *testing.T, req ReviewRequest, latest LatestReviewInput) SemanticDifference {
	t.Helper()
	got, err := CompareReviewInput(req, latest)
	if err != nil {
		t.Fatalf("compare error: %v", err)
	}
	return got
}

// exact Issue 観測だけが変わり、canonical Task Context と解決済み入力が同じなら、
// 既存 Operation identity と review approval を stale にしない。新しい Observation は
// audit lineage へ追記する。
func TestObservationOnlyDifferenceKeepsSemanticInput(t *testing.T) {
	body := readFixture(t, "valid/full.md")
	variants := map[string]string{
		"CRLF": strings.ReplaceAll(body, "\n", "\r\n"),
		"template comment": strings.Replace(
			body,
			"## Deliverables\n\n",
			"## Deliverables\n\n<!-- 進捗 memo。model input には渡さない。 -->\n\n",
			1,
		),
	}

	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			if variant == body {
				t.Fatal("test 前提が壊れている: variant が base と同じ")
			}
			latestCompiled, latestManifest := recompile(t, variant)

			op := sampleWorkerOperation(t)
			if latestCompiled.ObservationRef == *op.Observation {
				t.Fatal("exact body 差分で IssueObservationRef が変わらない")
			}
			if latestManifest != *op.ContextManifest {
				t.Fatal("非意味的差分で ContextManifestRef が変化")
			}

			latest := latestFromOperation(op)
			latest.Observation = latestCompiled.ObservationRef
			got := requireOperationComparison(t, op, latest)
			if got.Comparison != SameSemanticInput {
				t.Fatalf("comparison = %q, want %q (changed: %v)", got.Comparison, SameSemanticInput, got.ChangedFields)
			}
			if !got.ObservationChanged {
				t.Fatal("新しい Observation が audit lineage の対象として報告されない")
			}
			if len(got.ChangedFields) != 0 {
				t.Fatalf("changed fields = %v, want 空", got.ChangedFields)
			}

			// 以前の Result は同じ Operation identity に bind されたままである
			result := sampleOperationResult(t, op)
			if err := BindOperationResult(op, result); err != nil {
				t.Fatalf("Observation 差分で既存 Result が stale になった: %v", err)
			}

			lineage, err := NewObservationLineage(*op.Observation)
			if err != nil {
				t.Fatal(err)
			}
			lineage, err = lineage.Append(latest.Observation)
			if err != nil {
				t.Fatal(err)
			}
			if lineage.Len() != 2 {
				t.Fatalf("lineage 件数 = %d, want 2", lineage.Len())
			}

			req := sampleReviewRequest(t)
			reviewLatest := latestFromRequest(req)
			reviewLatest.Observation = latestCompiled.ObservationRef
			reviewGot := requireReviewComparison(t, req, reviewLatest)
			if reviewGot.Comparison != SameSemanticInput || !reviewGot.ObservationChanged {
				t.Fatalf("review comparison = %+v, want same semantic input", reviewGot)
			}
			if err := BindReviewResult(req, sampleReviewResult(t, req)); err != nil {
				t.Fatalf("Observation 差分で approval が stale になった: %v", err)
			}
		})
	}
}

// Task Context が変われば Context Manifest ref も変わる。protocol 側は manifest ref を
// opaque に比較するだけで、Task Context YAML を parse せずに staleness を判定できる。
func TestTaskContextDifferenceIsStale(t *testing.T) {
	body := readFixture(t, "valid/full.md")
	variant := strings.Replace(body, "外部から観測できる結果を書く。", "別の外部結果を書く。", 1)
	if variant == body {
		t.Fatal("test 前提が壊れている: variant が base と同じ")
	}

	base, baseManifest := recompile(t, body)
	changed, changedManifest := recompile(t, variant)
	if changed.TaskContextRef == base.TaskContextRef {
		t.Fatal("意味のある差分で TaskContextRef が変わらない")
	}
	if changedManifest == baseManifest {
		t.Fatal("Task Context 差分が ContextManifestRef へ伝播しない")
	}

	op := sampleWorkerOperation(t)
	latest := latestFromOperation(op)
	latest.Observation = changed.ObservationRef
	latest.ContextManifest = changedManifest

	got := requireOperationComparison(t, op, latest)
	if got.Comparison != ChangedSemanticInput {
		t.Fatalf("comparison = %q, want %q", got.Comparison, ChangedSemanticInput)
	}
	if !reflect.DeepEqual(got.ChangedFields, []string{"contextManifest"}) {
		t.Fatalf("changed fields = %v, want [contextManifest]", got.ChangedFields)
	}
	if !got.ObservationChanged {
		t.Fatal("Observation の変化が報告されない")
	}
}

// identityRole は envelope の field が content identity と staleness 比較に対して持つ役割である。
//
// 分類を宣言だけで済ませると、新しい field を「その他」へ入れるだけで test が通り、
// 「digest は別 identity と言うのに comparison は同じ入力と言う」状態が再び作れる。
// そのため各 role は digest と comparison の実際の振る舞いで検証する。
type identityRole int

const (
	// roleSemanticInput は digest に効き、再取得入力として comparison も見る field。
	roleSemanticInput identityRole = iota
	// roleAuditLineage は digest に効かず、観測の変化として comparison が報告する field。
	roleAuditLineage
	// roleIdentityOnly は digest に効くが、再取得できる入力ではない field。
	// Operation が「どの transition のどの Run のどの Issue に対する何の作業か」を
	// 決める値であり、最新入力として取り直す対象ではない。
	roleIdentityOnly
	// roleNonIdentity は digest に効かない実行時 metadata。
	roleNonIdentity
	// roleFixedSchema は有効値が一つしかなく、mutation では役割を検証できない field。
	roleFixedSchema
)

// identityCase は 1 field の役割と、それを確かめるための最小の変更である。
// compared は roleSemanticInput のときに comparison が返す field 名である。
type identityCase[T any, L any] struct {
	field    string
	role     identityRole
	compared string
	mutate   func(*T)
	latest   func(*L)
}

// requireFieldPartition は type の field 名が、渡した group の和集合とちょうど一致することを検証する。
// 分類漏れの field をそのまま通すと、この test は「意図した集合」ではなく「今の実装」を写すだけになる。
func requireFieldPartition(t *testing.T, typ reflect.Type, names map[string]bool) {
	t.Helper()
	remaining := map[string]bool{}
	for name := range names {
		remaining[name] = true
	}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !names[name] {
			t.Fatalf("%s.%s が identity role のどれにも分類されていない", typ.Name(), name)
		}
		delete(remaining, name)
	}
	for name := range remaining {
		t.Fatalf("%s に存在しない field %q を分類している", typ.Name(), name)
	}
}

func fieldNames[T any, L any](cases []identityCase[T, L], roles ...identityRole) map[string]bool {
	names := map[string]bool{}
	for _, c := range cases {
		if len(roles) == 0 || slices.Contains(roles, c.role) {
			names[c.field] = true
		}
	}
	return names
}

// operationIdentityCases は WorkerOperation の全 field を role へ割り当てる。
func operationIdentityCases() []identityCase[WorkerOperation, LatestOperationInput] {
	otherManifest := func(o *WorkerOperation) ContextManifestRef {
		return ContextManifestRef{Schema: o.ContextManifest.Schema, Digest: SHA256([]byte("別 manifest"))}
	}
	return []identityCase[WorkerOperation, LatestOperationInput]{
		{field: "Schema", role: roleFixedSchema},
		{field: "ContextManifest", role: roleSemanticInput, compared: fieldContextManifest,
			mutate: func(o *WorkerOperation) { ref := otherManifest(o); o.ContextManifest = &ref },
			latest: func(l *LatestOperationInput) { l.ContextManifest.Digest = SHA256([]byte("別 manifest")) }},
		{field: "ContextManifest", role: roleSemanticInput, compared: fieldContextManifest,
			mutate: func(o *WorkerOperation) {
				ref := ContextManifestRef{Schema: "kudo.context-manifest/v1beta1", Digest: o.ContextManifest.Digest}
				o.ContextManifest = &ref
			},
			latest: func(l *LatestOperationInput) { l.ContextManifest.Schema = "kudo.context-manifest/v1beta1" }},
		{field: "ExecutionPolicy", role: roleSemanticInput, compared: fieldExecutionPolicy,
			mutate: func(o *WorkerOperation) { o.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
			latest: func(l *LatestOperationInput) { l.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) }},
		{field: "HeadSHA", role: roleSemanticInput, compared: fieldHeadSHA,
			mutate: func(o *WorkerOperation) { o.HeadSHA = sampleNextSHA },
			latest: func(l *LatestOperationInput) { l.HeadSHA = sampleNextSHA }},
		{field: "InputArtifacts", role: roleSemanticInput, compared: fieldInputArtifacts,
			mutate: func(o *WorkerOperation) { o.InputArtifacts = []Digest{SHA256([]byte("別 input"))} },
			latest: func(l *LatestOperationInput) { l.InputArtifacts = []Digest{SHA256([]byte("別 input"))} }},
		{field: "PolicyRefs", role: roleSemanticInput, compared: fieldPolicyRefs,
			mutate: func(o *WorkerOperation) { o.PolicyRefs = []string{"docs/spec/05_design/02_workflow.md"} },
			latest: func(l *LatestOperationInput) { l.PolicyRefs = []string{"docs/spec/05_design/02_workflow.md"} }},
		{field: "Observation", role: roleAuditLineage,
			mutate: func(o *WorkerOperation) {
				ref := IssueObservationRef{Schema: o.Observation.Schema, Digest: SHA256([]byte("別 raw body"))}
				o.Observation = &ref
			},
			latest: func(l *LatestOperationInput) { l.Observation.Digest = SHA256([]byte("別 raw body")) }},
		{field: "Kind", role: roleIdentityOnly, mutate: func(o *WorkerOperation) { o.Kind = OperationReviseTests }},
		{field: "RunID", role: roleIdentityOnly, mutate: func(o *WorkerOperation) { o.RunID = "run-02" }},
		{field: "Issue", role: roleIdentityOnly, mutate: func(o *WorkerOperation) { o.Issue.Number = 11 }},
		{field: "CausationID", role: roleIdentityOnly, mutate: func(o *WorkerOperation) { o.CausationID = "transition-02" }},
		{field: "OperationID", role: roleNonIdentity, mutate: func(o *WorkerOperation) { o.OperationID = "op-02" }},
		{field: "CreatedAt", role: roleNonIdentity, mutate: func(o *WorkerOperation) { o.CreatedAt = sampleCreatedAt.Add(time.Hour) }},
	}
}

// content identity が覆う semantic input と、staleness 比較が覆う入力は同じ集合でなければならない。
// 片側にだけ field を足すと「digest は別 identity、comparison は同じ入力」という状態が作れ、
// 変わった入力へ古い Result と approval を再利用する経路になる。
func TestOperationIdentityRolesAreEnforced(t *testing.T) {
	cases := operationIdentityCases()
	requireFieldPartition(t, reflect.TypeOf(WorkerOperation{}), fieldNames(cases))
	requireFieldPartition(t, reflect.TypeOf(LatestOperationInput{}),
		fieldNames(cases, roleSemanticInput, roleAuditLineage))

	op := sampleWorkerOperation(t)
	baseDigest := requireOperationDigest(t, op)
	for _, c := range cases {
		if c.role == roleFixedSchema {
			continue
		}
		t.Run(c.field+"/"+c.compared, func(t *testing.T) {
			changed := sampleWorkerOperation(t)
			c.mutate(&changed)
			digest := requireOperationDigest(t, changed)

			switch c.role {
			case roleSemanticInput, roleIdentityOnly:
				if digest == baseDigest {
					t.Fatalf("%s の変更で Operation digest が変わらない", c.field)
				}
			case roleAuditLineage, roleNonIdentity:
				if digest != baseDigest {
					t.Fatalf("identity に寄与しない %s の差分で digest が変化", c.field)
				}
			}
			if c.latest == nil {
				return
			}

			latest := latestFromOperation(op)
			c.latest(&latest)
			got := requireOperationComparison(t, op, latest)
			if c.role == roleSemanticInput {
				if !slices.Contains(got.ChangedFields, c.compared) {
					t.Fatalf("digest が identity と認めた %s を comparison が報告しない: %+v", c.field, got)
				}
				return
			}
			if !got.ObservationChanged || len(got.ChangedFields) != 0 {
				t.Fatalf("audit lineage の差分が semantic 変化として報告された: %+v", got)
			}
		})
	}
}

// reviewIdentityCases は ReviewRequest の全 field を role へ割り当てる。
func reviewIdentityCases(t *testing.T) []identityCase[ReviewRequest, LatestReviewInput] {
	t.Helper()
	changedManifest := sampleArtifactManifest(t)
	changedManifest.Entries[0].Digest = SHA256([]byte("別 artifact bytes"))
	changedRef := requireArtifactManifestRef(t, changedManifest)

	return []identityCase[ReviewRequest, LatestReviewInput]{
		{field: "Schema", role: roleFixedSchema},
		{field: "ContextManifest", role: roleSemanticInput, compared: fieldContextManifest,
			mutate: func(r *ReviewRequest) { r.ContextManifest.Digest = SHA256([]byte("別 manifest")) },
			latest: func(l *LatestReviewInput) { l.ContextManifest.Digest = SHA256([]byte("別 manifest")) }},
		{field: "ExecutionPolicy", role: roleSemanticInput, compared: fieldExecutionPolicy,
			mutate: func(r *ReviewRequest) { r.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
			latest: func(l *LatestReviewInput) { l.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) }},
		{field: "HeadSHA", role: roleSemanticInput, compared: fieldHeadSHA,
			mutate: func(r *ReviewRequest) { r.HeadSHA = sampleNextSHA },
			latest: func(l *LatestReviewInput) { l.HeadSHA = sampleNextSHA }},
		// PR が外部で close され別 PR として作り直された場合、古い approve を新しい PR の
		// approval として再利用してはならない。
		{field: "PullRequest", role: roleSemanticInput, compared: fieldPullRequest,
			mutate: func(r *ReviewRequest) { r.PullRequest.Number = 58 },
			latest: func(l *LatestReviewInput) { l.PullRequest.Number = 58 }},
		{field: "ArtifactManifest", role: roleSemanticInput, compared: fieldArtifactManifest,
			mutate: func(r *ReviewRequest) { r.ArtifactManifest = changedRef },
			latest: func(l *LatestReviewInput) { l.ArtifactManifest = changedRef }},
		{field: "ArtifactManifest", role: roleSemanticInput, compared: fieldArtifactManifest,
			mutate: func(r *ReviewRequest) { r.ArtifactManifest.Schema = "kudo.artifact-manifest/v1beta1" },
			latest: func(l *LatestReviewInput) { l.ArtifactManifest.Schema = "kudo.artifact-manifest/v1beta1" }},
		// 標準 policy は kind の必須 ref なので、追加 policy の差分で比較する。
		{field: "PolicyRefs", role: roleSemanticInput, compared: fieldPolicyRefs,
			mutate: func(r *ReviewRequest) {
				r.PolicyRefs = []string{"docs/spec/05_design/review-policies/test-validity-v1alpha1.md", "docs/spec/05_design/02_workflow.md"}
			},
			latest: func(l *LatestReviewInput) {
				l.PolicyRefs = []string{"docs/spec/05_design/review-policies/test-validity-v1alpha1.md", "docs/spec/05_design/02_workflow.md"}
			}},
		{field: "Observation", role: roleAuditLineage,
			mutate: func(r *ReviewRequest) { r.Observation.Digest = SHA256([]byte("別 raw body")) },
			latest: func(l *LatestReviewInput) { l.Observation.Digest = SHA256([]byte("別 raw body")) }},
		// PR body の編集や draft/ready の状態遷移だけでは identity と approval を stale に
		// しない。観測の変化は lineage へ追記される。
		{field: "PullRequestObservation", role: roleAuditLineage,
			mutate: func(r *ReviewRequest) { r.PullRequestObservation.Digest = SHA256([]byte("別 PR 観測")) },
			latest: func(l *LatestReviewInput) { l.PullRequestObservation.Digest = SHA256([]byte("別 PR 観測")) }},
		// kind の変更は標準 policy ref も変える。policy ref を揃えないと validation が先に
		// 落ち、identity の検証にならない。
		{field: "Kind", role: roleIdentityOnly, mutate: func(r *ReviewRequest) {
			r.Kind = ReviewFinalImplementation
			r.PolicyRefs = append(r.PolicyRefs, "docs/spec/05_design/review-policies/final-implementation-v1alpha1.md")
		}},
		{field: "Issue", role: roleIdentityOnly, mutate: func(r *ReviewRequest) { r.Issue.Number = 11 }},
		{field: "RequestID", role: roleNonIdentity, mutate: func(r *ReviewRequest) { r.RequestID = "01KUDOOTHER" }},
		{field: "ProducerRunID", role: roleNonIdentity, mutate: func(r *ReviewRequest) { r.ProducerRunID = "run-02" }},
		{field: "CreatedAt", role: roleNonIdentity, mutate: func(r *ReviewRequest) { r.CreatedAt = sampleCreatedAt.Add(time.Hour) }},
	}
}

func TestReviewIdentityRolesAreEnforced(t *testing.T) {
	cases := reviewIdentityCases(t)
	requireFieldPartition(t, reflect.TypeOf(ReviewRequest{}), fieldNames(cases))
	requireFieldPartition(t, reflect.TypeOf(LatestReviewInput{}),
		fieldNames(cases, roleSemanticInput, roleAuditLineage))

	req := sampleReviewRequest(t)
	baseDigest := requireReviewRequestDigest(t, req)
	for _, c := range cases {
		if c.role == roleFixedSchema {
			continue
		}
		t.Run(c.field+"/"+c.compared, func(t *testing.T) {
			changed := sampleReviewRequest(t)
			c.mutate(&changed)
			digest := requireReviewRequestDigest(t, changed)

			switch c.role {
			case roleSemanticInput, roleIdentityOnly:
				if digest == baseDigest {
					t.Fatalf("%s の変更で Review Request digest が変わらない", c.field)
				}
			case roleAuditLineage, roleNonIdentity:
				if digest != baseDigest {
					t.Fatalf("identity に寄与しない %s の差分で digest が変化", c.field)
				}
			}
			if c.latest == nil {
				return
			}

			latest := latestFromRequest(req)
			c.latest(&latest)
			got := requireReviewComparison(t, req, latest)
			if c.role == roleSemanticInput {
				if !slices.Contains(got.ChangedFields, c.compared) {
					t.Fatalf("digest が identity と認めた %s を comparison が報告しない: %+v", c.field, got)
				}
				return
			}
			if !got.ObservationChanged || len(got.ChangedFields) != 0 {
				t.Fatalf("audit lineage の差分が semantic 変化として報告された: %+v", got)
			}
		})
	}
}

func TestOperationSemanticDifferenceMatrix(t *testing.T) {
	op := sampleWorkerOperation(t)

	tests := map[string]struct {
		mutate func(*LatestOperationInput)
		want   []string
	}{
		"context manifest": {
			func(l *LatestOperationInput) { l.ContextManifest.Digest = SHA256([]byte("別 manifest")) },
			[]string{"contextManifest"},
		},
		"manifest ref schema": {
			func(l *LatestOperationInput) { l.ContextManifest.Schema = "kudo.context-manifest/v1beta1" },
			[]string{"contextManifest"},
		},
		"execution policy": {
			func(l *LatestOperationInput) { l.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
			[]string{"executionPolicy"},
		},
		"head": {
			func(l *LatestOperationInput) { l.HeadSHA = sampleNextSHA },
			[]string{"headSha"},
		},
		"input artifacts": {
			func(l *LatestOperationInput) { l.InputArtifacts = []Digest{SHA256([]byte("別 input"))} },
			[]string{"inputArtifacts"},
		},
		"policy refs": {
			func(l *LatestOperationInput) { l.PolicyRefs = []string{"docs/spec/05_design/02_workflow.md"} },
			[]string{"policyRefs"},
		},
		"複数": {
			func(l *LatestOperationInput) {
				l.HeadSHA = sampleNextSHA
				l.ExecutionPolicy.Digest = SHA256([]byte("別 policy"))
			},
			[]string{"executionPolicy", "headSha"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			latest := latestFromOperation(op)
			tt.mutate(&latest)
			got := requireOperationComparison(t, op, latest)
			if got.Comparison != ChangedSemanticInput {
				t.Fatalf("comparison = %q, want %q", got.Comparison, ChangedSemanticInput)
			}
			if !reflect.DeepEqual(got.ChangedFields, tt.want) {
				t.Fatalf("changed fields = %v, want %v", got.ChangedFields, tt.want)
			}
			if got.ObservationChanged {
				t.Fatal("Observation は変えていないのに変化として報告された")
			}
		})
	}

	t.Run("差分なし", func(t *testing.T) {
		got := requireOperationComparison(t, op, latestFromOperation(op))
		if got.Comparison != SameSemanticInput || got.ObservationChanged || len(got.ChangedFields) != 0 {
			t.Fatalf("comparison = %+v, want 差分なし", got)
		}
	})

	t.Run("input artifact の並び替え", func(t *testing.T) {
		reordered := sampleWorkerOperation(t)
		reordered.InputArtifacts = []Digest{SHA256([]byte("a")), SHA256([]byte("b"))}
		latest := latestFromOperation(reordered)
		latest.InputArtifacts = []Digest{SHA256([]byte("b")), SHA256([]byte("a"))}
		latest.PolicyRefs = []string{"docs/spec/05_design/04_github-routing.md"}
		if got := requireOperationComparison(t, reordered, latest); got.Comparison != SameSemanticInput {
			t.Fatalf("集合の並び替えを差分と判定した: %+v", got)
		}
	})
}

func TestReviewSemanticDifferenceMatrix(t *testing.T) {
	req := sampleReviewRequest(t)

	changedManifest := sampleArtifactManifest(t)
	changedManifest.Entries[0].Digest = SHA256([]byte("別 artifact bytes"))

	tests := map[string]struct {
		mutate func(*LatestReviewInput)
		want   []string
	}{
		"context manifest": {
			func(l *LatestReviewInput) { l.ContextManifest.Digest = SHA256([]byte("別 manifest")) },
			[]string{"contextManifest"},
		},
		"execution policy": {
			func(l *LatestReviewInput) { l.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
			[]string{"executionPolicy"},
		},
		"head": {
			func(l *LatestReviewInput) { l.HeadSHA = sampleNextSHA },
			[]string{"headSha"},
		},
		"artifact manifest": {
			func(l *LatestReviewInput) { l.ArtifactManifest = requireArtifactManifestRef(t, changedManifest) },
			[]string{"artifactManifest"},
		},
		"artifact manifest schema": {
			func(l *LatestReviewInput) { l.ArtifactManifest.Schema = "kudo.artifact-manifest/v1beta1" },
			[]string{"artifactManifest"},
		},
		"policy refs": {
			func(l *LatestReviewInput) { l.PolicyRefs = []string{"docs/spec/05_design/02_workflow.md"} },
			[]string{"policyRefs"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			latest := latestFromRequest(req)
			tt.mutate(&latest)
			got := requireReviewComparison(t, req, latest)
			if got.Comparison != ChangedSemanticInput {
				t.Fatalf("comparison = %q, want %q", got.Comparison, ChangedSemanticInput)
			}
			if !reflect.DeepEqual(got.ChangedFields, tt.want) {
				t.Fatalf("changed fields = %v, want %v", got.ChangedFields, tt.want)
			}
		})
	}
}

// 比較は最新入力を既存 Operation へ書き戻さない。差し替えを暗黙に行うと、
// 古い approval が新しい入力の approval として再利用されてしまう。
func TestCompareDoesNotAdoptLatestInput(t *testing.T) {
	op := sampleWorkerOperation(t)
	before := requireOperationDigest(t, op)

	latest := latestFromOperation(op)
	latest.ContextManifest.Digest = SHA256([]byte("別 manifest"))
	latest.Observation.Digest = SHA256([]byte("別 raw body"))
	latest.HeadSHA = sampleNextSHA

	if _, err := CompareOperationInput(op, latest); err != nil {
		t.Fatal(err)
	}
	if requireOperationDigest(t, op) != before {
		t.Fatal("比較が既存 Operation の入力を書き換えた")
	}

	req := sampleReviewRequest(t)
	beforeRequest := requireReviewRequestDigest(t, req)
	reviewLatest := latestFromRequest(req)
	reviewLatest.HeadSHA = sampleNextSHA
	if _, err := CompareReviewInput(req, reviewLatest); err != nil {
		t.Fatal(err)
	}
	if requireReviewRequestDigest(t, req) != beforeRequest {
		t.Fatal("比較が既存 Review Request の入力を書き換えた")
	}
}

// 判定不能な入力は「変化なし」に倒さない。
func TestCompareRejectsAmbiguousInput(t *testing.T) {
	op := sampleWorkerOperation(t)

	invalidLatest := map[string]func(*LatestOperationInput){
		"observation digest": func(l *LatestOperationInput) { l.Observation.Digest = "sha256:nope" },
		"observation schema": func(l *LatestOperationInput) { l.Observation.Schema = "" },
		"manifest schema":    func(l *LatestOperationInput) { l.ContextManifest.Schema = "context-manifest/v1alpha1" },
		"manifest digest":    func(l *LatestOperationInput) { l.ContextManifest.Digest = "" },
		"policy digest":      func(l *LatestOperationInput) { l.ExecutionPolicy.Digest = "sha256:nope" },
		"head":               func(l *LatestOperationInput) { l.HeadSHA = "HEAD" },
		"artifact digest":    func(l *LatestOperationInput) { l.InputArtifacts = []Digest{"sha256:nope"} },
		"policy ref":         func(l *LatestOperationInput) { l.PolicyRefs = []string{"../secret.md"} },
	}
	for name, mutate := range invalidLatest {
		t.Run(name, func(t *testing.T) {
			latest := latestFromOperation(op)
			mutate(&latest)
			if _, err := CompareOperationInput(op, latest); err == nil {
				t.Fatal("判定できない最新入力を受理した")
			}
		})
	}

	t.Run("invalid operation", func(t *testing.T) {
		invalid := sampleWorkerOperation(t)
		invalid.RunID = ""
		if _, err := CompareOperationInput(invalid, latestFromOperation(op)); err == nil {
			t.Fatal("invalid Operation を比較した")
		}
	})

	// claim はまだ Context Manifest も head も持たない。semantic input の比較対象にしない。
	t.Run("claim", func(t *testing.T) {
		claim := sampleClaimOperation(t)
		if _, err := CompareOperationInput(claim, latestFromOperation(op)); err == nil {
			t.Fatal("claim Operation を semantic 比較の対象にした")
		}
	})

	t.Run("invalid review request", func(t *testing.T) {
		req := sampleReviewRequest(t)
		latest := latestFromRequest(req)
		latest.ArtifactManifest.Digest = "sha256:nope"
		if _, err := CompareReviewInput(req, latest); err == nil {
			t.Fatal("判定できない最新入力を受理した")
		}
	})
}

func TestObservationLineage(t *testing.T) {
	compiled, _, _ := sampleResolvedInput(t)
	first := compiled.ObservationRef
	second := IssueObservationRef{Schema: first.Schema, Digest: SHA256([]byte("別 raw body"))}

	if _, err := NewObservationLineage(IssueObservationRef{}); err == nil {
		t.Fatal("invalid Observation で lineage を作った")
	}

	lineage, err := NewObservationLineage(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lineage.Append(IssueObservationRef{Schema: first.Schema, Digest: "sha256:nope"}); err == nil {
		t.Fatal("invalid Observation を lineage へ追記した")
	}

	// 同じ Observation の再観測は履歴を増やさない
	same, err := lineage.Append(first)
	if err != nil {
		t.Fatal(err)
	}
	if same.Len() != 1 {
		t.Fatalf("同一 Observation の再観測で件数 = %d, want 1", same.Len())
	}

	appended, err := lineage.Append(second)
	if err != nil {
		t.Fatal(err)
	}
	if lineage.Len() != 1 {
		t.Fatal("Append が元の lineage を破壊した")
	}
	if got := appended.Entries(); !reflect.DeepEqual(got, []IssueObservationRef{first, second}) {
		t.Fatalf("entries = %+v", got)
	}
	if latest, ok := appended.Latest(); !ok || latest != second {
		t.Fatalf("latest = %+v, ok = %v", latest, ok)
	}

	// Entries は内部 slice を共有しない
	entries := appended.Entries()
	entries[0] = second
	if appended.Entries()[0] != first {
		t.Fatal("Entries が内部 slice を共有した")
	}
}
