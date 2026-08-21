package contract

import (
	"errors"
	"strings"
	"testing"
)

// 必須 logical name は「Operation が何を残したと主張できるか」と「reviewer が何を読めるか」を
// 決める gate 条件である。以下の test は、その集合が語彙として閉じていること、kind を
// 追加したときに宣言忘れが検出されること、欠落が binding 境界で拒否されることを固定する。

// Issue由来の入力はGitHubから各Operationで再取得・再compileする。canonical bytesは
// digest計算とそのAttemptのmodel入力にだけ使い、Artifact Storeの必須outputへ格上げしない。
func TestReconstructibleIssueInputsAreNotPersistentArtifacts(t *testing.T) {
	reconstructible := map[ArtifactName]bool{
		"raw-issue-body":    true,
		"issue-observation": true,
		"task-context":      true,
		"context-manifest":  true,
	}
	for kind, required := range requiredOperationOutputs {
		for _, name := range required {
			if reconstructible[name] {
				t.Fatalf("operation kind %q が再構築可能な %q の永続化を要求している", kind, name)
			}
		}
	}
	for kind, required := range requiredReviewEntries {
		for _, name := range required {
			if reconstructible[name] {
				t.Fatalf("review kind %q が再構築可能な %q の永続化を要求している", kind, name)
			}
		}
	}
	for _, name := range ArtifactNames() {
		if reconstructible[name] {
			t.Fatalf("再構築可能な %q が永続artifact語彙に残っている", name)
		}
	}

	claim := sampleClaimOperation(t)
	result := sampleOperationResult(t, claim)
	result.HeadSHA = ""
	result.OutputArtifacts = []NamedArtifact{{
		Name:   "task-context",
		Digest: SHA256([]byte("保存してはならないTask Context")),
	}}
	if err := BindOperationResult(claim, result); !errors.Is(err, ProtocolKindConstraint) {
		t.Fatalf("Issue由来output artifactが %q へ分類されない: %v", ProtocolKindConstraint, err)
	}

	manifest := sampleArtifactManifest(t)
	manifest.Entries = append(manifest.Entries, ArtifactEntry{
		Name:      "context-manifest",
		MediaType: MediaTypeYAML,
		Length:    1,
		Digest:    SHA256([]byte("x")),
	})
	if _, _, err := EncodeArtifactManifest(manifest); !errors.Is(err, ProtocolKindConstraint) {
		t.Fatalf("Issue由来manifest entryが %q へ分類されない: %v", ProtocolKindConstraint, err)
	}
}

// TestRequiredArtifactNamesAreClosedAndReachable は語彙と kind 別必須集合の対応を固定する。
//
// 必須集合の網羅は「思いついた kind を列挙する」形で書くと、新しい kind を追加したとき
// test 側も一緒に忘れるため green のまま穴が開く。母集団を既存の kind table から取り、
// 宣言漏れを test 自身が検出できるようにする。
func TestRequiredArtifactNamesAreClosedAndReachable(t *testing.T) {
	vocabulary := map[ArtifactName]bool{}
	for _, name := range ArtifactNames() {
		if vocabulary[name] {
			t.Fatalf("語彙に重複がある: %s", name)
		}
		if !validArtifactName(string(name)) {
			t.Fatalf("語彙 %q が manifest の logical name 規則を満たさない", name)
		}
		vocabulary[name] = true
	}

	reachable := map[ArtifactName]bool{}
	for kind := range operationKindRules {
		required, ok := requiredOperationOutputs[kind]
		if !ok {
			t.Fatalf("operation kind %q の必須 output が宣言されていない", kind)
		}
		for _, name := range required {
			if !vocabulary[name] {
				t.Fatalf("operation kind %q が語彙外の name を要求している: %s", kind, name)
			}
			reachable[name] = true
		}
	}
	// kind 別集合のほかに、outcome に紐付く必須集合も語彙の到達点である。
	for _, name := range requiredTestRevisionOutputs {
		if !vocabulary[name] {
			t.Fatalf("test_revision_required が語彙外の name を要求している: %s", name)
		}
		reachable[name] = true
	}
	for kind := range reviewKinds {
		required, ok := requiredReviewEntries[kind]
		if !ok {
			t.Fatalf("review kind %q の必須 manifest entry が宣言されていない", kind)
		}
		if len(required) == 0 {
			t.Fatalf("review kind %q が manifest entry を一つも要求していない", kind)
		}
		// 条件付き entry も語彙の到達点である。条件の強制は review prerequisite 側だが、
		// どの kind にも帰属しない name は dead vocabulary として検出したい。
		for _, name := range append(append([]ArtifactName(nil), required...), conditionalReviewEntries[kind]...) {
			if !vocabulary[name] {
				t.Fatalf("review kind %q が語彙外の name を要求している: %s", kind, name)
			}
			reachable[name] = true
		}
	}

	for name := range vocabulary {
		if !reachable[name] {
			t.Fatalf("語彙 %q をどの kind も要求していない", name)
		}
	}
}

// TestSucceededResultRejectsMissingOutputNames は AC-1 を固定する。
func TestSucceededResultRejectsMissingOutputNames(t *testing.T) {
	op := sampleWorkerOperation(t)
	result := sampleOperationResult(t, op)
	result.OutputArtifacts = []NamedArtifact{{
		Name:   "implementation-brief",
		Digest: SHA256([]byte("implementation-brief")),
	}}

	err := BindOperationResult(op, result)
	if !errors.Is(err, ProtocolKindConstraint) {
		t.Fatalf("必須 output の欠落が %q へ分類されない: %v", ProtocolKindConstraint, err)
	}
	// 何を足せば通るかが error から読めなければ、producer は総当たりで再試行する。
	for _, name := range requiredOperationOutputs[OperationAuthorTests] {
		if !strings.Contains(err.Error(), string(name)) {
			t.Fatalf("欠落した logical name %q が error に現れない: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "implementation-brief") {
		t.Fatalf("欠落していない name が欠落として報告された: %v", err)
	}
}

// TestSucceededResultRejectsPartiallyMissingOutputNames は、必須集合の一部だけを
// 満たした Result が通らないことを固定する。非空判定だけの実装はこの case を通す。
func TestSucceededResultRejectsPartiallyMissingOutputNames(t *testing.T) {
	op := sampleWorkerOperation(t)
	result := sampleOperationResult(t, op)
	result.OutputArtifacts = []NamedArtifact{{
		Name:   string(ArtifactNameTestPlan),
		Digest: SHA256([]byte("test-plan")),
	}}

	err := BindOperationResult(op, result)
	if !errors.Is(err, ProtocolKindConstraint) {
		t.Fatalf("必須 output の一部欠落が拒否されない: %v", err)
	}
	if !strings.Contains(err.Error(), string(ArtifactNameRedEvidence)) {
		t.Fatalf("欠落した %q が error に現れない: %v", ArtifactNameRedEvidence, err)
	}
}

// source bundle は Issue Worker の workspace にしか存在しない head を Review Worker へ渡す
// checkpoint である。head を生成した Operation の成功条件に含まれなければ、Controller は
// workspace を読めないまま次の review request を組み立てることになる。
func TestHeadProducingOperationRequiresSourceBundle(t *testing.T) {
	for _, kind := range []OperationKind{
		OperationAuthorTests,
		OperationReviseTests,
		OperationImplement,
		OperationRepairImplementation,
	} {
		t.Run(string(kind), func(t *testing.T) {
			op := sampleWorkerOperation(t)
			op.Kind = kind
			result := sampleOperationResult(t, op)
			result.OutputArtifacts = withoutNamedArtifact(result.OutputArtifacts, ArtifactNameSourceBundle)

			err := BindOperationResult(op, result)
			if !errors.Is(err, ProtocolKindConstraint) {
				t.Fatalf("source bundle を欠く succeeded が %q へ分類されない: %v", ProtocolKindConstraint, err)
			}
			if !strings.Contains(err.Error(), string(ArtifactNameSourceBundle)) {
				t.Fatalf("欠落した %q が error に現れない: %v", ArtifactNameSourceBundle, err)
			}
		})
	}
}

// TestReviewRequestBindingRejectsMissingManifestEntries は AC-2 を固定する。
func TestReviewRequestBindingRejectsMissingManifestEntries(t *testing.T) {
	for _, missing := range []ArtifactName{ArtifactNameTestPlan, ArtifactNameSourceBundle} {
		t.Run(string(missing), func(t *testing.T) {
			manifest := withoutEntry(sampleArtifactManifest(t), missing)
			req := sampleReviewRequest(t)
			req.ArtifactManifest = requireArtifactManifestRef(t, manifest)

			err := BindReviewRequestManifest(req, manifest)
			if !errors.Is(err, ProtocolKindConstraint) {
				t.Fatalf("必須 entry の欠落が %q へ分類されない: %v", ProtocolKindConstraint, err)
			}
			if !strings.Contains(err.Error(), string(missing)) {
				t.Fatalf("欠落した %q が error に現れない: %v", missing, err)
			}
		})
	}
}

// TestReviewRequestBindingRejectsUnreferencedManifest は、必須 entry を揃えた別の
// manifest を渡して gate を通す経路を塞ぐ。name の充足だけを見て ref を照合しない実装は
// 「reviewer が実際に読む manifest」と「検証した manifest」を取り違える。
func TestReviewRequestBindingRejectsUnreferencedManifest(t *testing.T) {
	req := sampleReviewRequest(t)
	other := sampleArtifactManifest(t)
	other.Entries = append(other.Entries, ArtifactEntry{
		Name:      "extra-evidence",
		MediaType: "text/plain; charset=utf-8",
		Length:    int64(len(sampleREDEvidence)),
		Digest:    SHA256([]byte("extra-evidence")),
	})

	err := BindReviewRequestManifest(req, other)
	if !errors.Is(err, ProtocolIdentityMismatch) {
		t.Fatalf("request が参照していない manifest が %q へ分類されない: %v", ProtocolIdentityMismatch, err)
	}
}

// Versioned ref は schema と digest の組である。schema だけが違う場合に digest だけを
// 表示すると、同じ値同士が不一致に見えて producer が version skew を特定できない。
func TestReviewRequestBindingReportsManifestSchemaMismatch(t *testing.T) {
	manifest := sampleArtifactManifest(t)
	req := sampleReviewRequest(t)
	req.ArtifactManifest.Schema = "kudo.artifact-manifest/v1alpha2"

	err := BindReviewRequestManifest(req, manifest)
	if !errors.Is(err, ProtocolIdentityMismatch) {
		t.Fatalf("manifest schema の不一致が %q へ分類されない: %v", ProtocolIdentityMismatch, err)
	}
	for _, schema := range []string{req.ArtifactManifest.Schema, manifest.Schema} {
		if !strings.Contains(err.Error(), schema) {
			t.Fatalf("manifest schema %q が error に現れない: %v", schema, err)
		}
	}
}

// TestFinalImplementationRequiresMoreThanTestValidity は、gate ごとに必須集合が
// 異なることを固定する。test_validity 用の manifest で final review を開始できると、
// GREEN 証跡も check 証跡も無いまま PR 作成 gate へ進む。
func TestFinalImplementationRequiresMoreThanTestValidity(t *testing.T) {
	manifest := sampleArtifactManifest(t)
	req := sampleFinalReviewRequest(t)
	req.ArtifactManifest = requireArtifactManifestRef(t, manifest)

	err := BindReviewRequestManifest(req, manifest)
	if !errors.Is(err, ProtocolKindConstraint) {
		t.Fatalf("test_validity 用 manifest で final review が開始できてしまう: %v", err)
	}
	for _, name := range []ArtifactName{ArtifactNameGreenEvidence, ArtifactNameCheckEvidence} {
		if !strings.Contains(err.Error(), string(name)) {
			t.Fatalf("欠落した %q が error に現れない: %v", name, err)
		}
	}
}

// TestCompleteArtifactSetsAreAccepted は AC-3 を固定する。
func TestCompleteArtifactSetsAreAccepted(t *testing.T) {
	op := sampleWorkerOperation(t)
	if err := BindOperationResult(op, sampleOperationResult(t, op)); err != nil {
		t.Fatalf("必須 output を備えた Result が拒否された: %v", err)
	}

	req := sampleReviewRequest(t)
	if err := BindReviewRequestManifest(req, sampleArtifactManifest(t)); err != nil {
		t.Fatalf("必須 entry を備えた test_validity manifest が拒否された: %v", err)
	}

	final := sampleFinalImplementationManifest(t)
	finalReq := sampleFinalReviewRequest(t)
	finalReq.ArtifactManifest = requireArtifactManifestRef(t, final)
	if err := BindReviewRequestManifest(finalReq, final); err != nil {
		t.Fatalf("必須 entry を備えた final_implementation manifest が拒否された: %v", err)
	}
}

// TestExtraArtifactNamesAreAccepted は必須集合が下限であって上限でないことを固定する。
// 語彙外の evidence を足しただけで gate が止まると、Worker は必要な証跡を残せなくなる。
func TestExtraArtifactNamesAreAccepted(t *testing.T) {
	op := sampleWorkerOperation(t)
	result := sampleOperationResult(t, op)
	result.OutputArtifacts = append(result.OutputArtifacts, NamedArtifact{
		Name:   "coverage-report",
		Digest: SHA256([]byte("coverage-report")),
	})
	if err := BindOperationResult(op, result); err != nil {
		t.Fatalf("語彙外の output artifact を足した Result が拒否された: %v", err)
	}
}

// TestOperationResultIdentityIgnoresOutputOrder は、logical name を導入しても Result
// identity が producer の列挙順に依存しないことを固定する。model provider は同じ結果でも
// 順序を再現しないため、並びだけが違う Result を別 identity にすると再実行が重複を生む。
func TestOperationResultIdentityIgnoresOutputOrder(t *testing.T) {
	op := sampleWorkerOperation(t)
	result := sampleOperationResult(t, op)
	reversed := sampleOperationResult(t, op)
	outputs := reversed.OutputArtifacts
	for i, j := 0, len(outputs)-1; i < j; i, j = i+1, j-1 {
		outputs[i], outputs[j] = outputs[j], outputs[i]
	}

	first, err := OperationResultDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OperationResultDigest(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("output artifact の列挙順で Result identity が変わった: %s != %s", first, second)
	}
}

// TestOutputArtifactNamesAreValidatedAsTable は、Result の output が name で引く table
// であることを固定する。name の重複を通すと、同じ name が二つの digest を指す Result が
// 保存され、後続 Operation はどちらを読むかを選べない。
func TestOutputArtifactNamesAreValidatedAsTable(t *testing.T) {
	op := sampleWorkerOperation(t)
	for name, mutate := range map[string]func(*OperationResult){
		"name-invalid": func(r *OperationResult) {
			r.OutputArtifacts[0].Name = "../escape"
		},
		"name-duplicate": func(r *OperationResult) {
			r.OutputArtifacts[1].Name = r.OutputArtifacts[0].Name
		},
		"digest-invalid": func(r *OperationResult) {
			r.OutputArtifacts[0].Digest = "sha256:nope"
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := sampleOperationResult(t, op)
			mutate(&result)
			err := ValidateOperationResult(result)
			if _, ok := ProtocolViolation(err); !ok {
				t.Fatalf("output artifact table の違反が分類可能な error にならない: %v", err)
			}
		})
	}
}

func withoutEntry(manifest ArtifactManifest, name ArtifactName) ArtifactManifest {
	kept := make([]ArtifactEntry, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Name != string(name) {
			kept = append(kept, entry)
		}
	}
	manifest.Entries = kept
	return manifest
}

func withoutNamedArtifact(artifacts []NamedArtifact, name ArtifactName) []NamedArtifact {
	kept := make([]NamedArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Name != string(name) {
			kept = append(kept, artifact)
		}
	}
	return kept
}
