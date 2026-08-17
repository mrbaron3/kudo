package contract

import (
	"reflect"
	"slices"
	"strings"
	"testing"
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
		Observation:      req.Observation,
		ContextManifest:  req.ContextManifest,
		ExecutionPolicy:  req.ExecutionPolicy,
		HeadSHA:          req.HeadSHA,
		ArtifactManifest: req.ArtifactManifest,
		PolicyRefs:       append([]string(nil), req.PolicyRefs...),
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

// requireFieldPartition は type の field 名が、渡した group の和集合とちょうど一致することを検証する。
// 分類漏れの field をそのまま通すと、この test は「意図した集合」ではなく「今の実装」を写すだけになる。
func requireFieldPartition(t *testing.T, typ reflect.Type, groups ...[]string) {
	t.Helper()
	want := map[string]bool{}
	for _, group := range groups {
		for _, name := range group {
			if want[name] {
				t.Fatalf("%s: field %q が複数の group に属している", typ.Name(), name)
			}
			want[name] = true
		}
	}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !want[name] {
			t.Fatalf("%s.%s が semantic input / audit / 非入力のどれにも分類されていない", typ.Name(), name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Fatalf("%s に存在しない field %q を分類している", typ.Name(), name)
	}
}

// content identity が覆う semantic input と、staleness 比較が覆う入力は同じ集合でなければならない。
// 片側にだけ field を足すと「digest は別 identity、comparison は同じ入力」という状態が作れ、
// 変わった入力へ古い Result と approval を再利用する経路になる。
func TestOperationIdentityAndComparisonCoverSameInput(t *testing.T) {
	semantic := []string{"ContextManifest", "ExecutionPolicy", "HeadSHA", "InputArtifacts", "PolicyRefs"}
	// Observation は identity ではないが、lineage へ追記する signal として比較入力に含む。
	auditOnly := []string{"Observation"}
	requireFieldPartition(t, reflect.TypeOf(WorkerOperation{}), semantic, auditOnly,
		[]string{"Schema", "OperationID", "RunID", "Kind", "Issue", "CausationID", "CreatedAt"})
	requireFieldPartition(t, reflect.TypeOf(LatestOperationInput{}), semantic, auditOnly)

	op := sampleWorkerOperation(t)
	baseDigest := requireOperationDigest(t, op)
	tests := map[string]struct {
		field  string
		change func(*WorkerOperation, *LatestOperationInput)
	}{
		"context manifest": {"contextManifest", func(o *WorkerOperation, l *LatestOperationInput) {
			ref := ContextManifestRef{Schema: o.ContextManifest.Schema, Digest: SHA256([]byte("別 manifest"))}
			o.ContextManifest, l.ContextManifest = &ref, ref
		}},
		"execution policy": {"executionPolicy", func(o *WorkerOperation, l *LatestOperationInput) {
			o.ExecutionPolicy.Digest = SHA256([]byte("別 policy"))
			l.ExecutionPolicy = o.ExecutionPolicy
		}},
		"head": {"headSha", func(o *WorkerOperation, l *LatestOperationInput) {
			o.HeadSHA, l.HeadSHA = sampleNextSHA, sampleNextSHA
		}},
		"input artifacts": {"inputArtifacts", func(o *WorkerOperation, l *LatestOperationInput) {
			o.InputArtifacts = []Digest{SHA256([]byte("別 input"))}
			l.InputArtifacts = append([]Digest(nil), o.InputArtifacts...)
		}},
		"policy refs": {"policyRefs", func(o *WorkerOperation, l *LatestOperationInput) {
			o.PolicyRefs = []string{"docs/workflow.md"}
			l.PolicyRefs = append([]string(nil), o.PolicyRefs...)
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			changed := sampleWorkerOperation(t)
			latest := latestFromOperation(changed)
			tt.change(&changed, &latest)

			if requireOperationDigest(t, changed) == baseDigest {
				t.Fatalf("%s の変更で Operation digest が変わらない", tt.field)
			}
			// 変更後の入力を「最新入力」として渡した元の Operation は stale でなければならない
			if got := requireOperationComparison(t, op, latest); !slices.Contains(got.ChangedFields, tt.field) {
				t.Fatalf("digest が identity と認めた %s を comparison が報告しない: %+v", tt.field, got)
			}
		})
	}
}

func TestReviewIdentityAndComparisonCoverSameInput(t *testing.T) {
	semantic := []string{"ContextManifest", "ExecutionPolicy", "HeadSHA", "ArtifactManifest", "PolicyRefs"}
	auditOnly := []string{"Observation"}
	requireFieldPartition(t, reflect.TypeOf(ReviewRequest{}), semantic, auditOnly,
		[]string{"Schema", "RequestID", "Kind", "ProducerRunID", "Issue", "CreatedAt"})
	requireFieldPartition(t, reflect.TypeOf(LatestReviewInput{}), semantic, auditOnly)
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
			func(l *LatestOperationInput) { l.PolicyRefs = []string{"docs/workflow.md"} },
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
		latest.PolicyRefs = []string{"docs/github-routing.md"}
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
			func(l *LatestReviewInput) { l.PolicyRefs = []string{"docs/workflow.md"} },
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
