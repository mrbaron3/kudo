package contract

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var sampleREDEvidence = []byte("$ go test ./...\nFAIL\texit status 1\n")

// sampleOpaqueArtifact は Artifact Store の payload 型を持たない artifact の entry を返す。
// evidence、source bundle、draft は canonical schema を持たない不透明 bytes であり、
// producer が media type と length を宣言する。
func sampleOpaqueArtifact(name ArtifactName, mediaType string, data []byte) ArtifactEntry {
	return ArtifactEntry{
		Name:      string(name),
		MediaType: mediaType,
		Length:    int64(len(data)),
		Digest:    SHA256(data),
	}
}

// sampleArtifactManifest は test_validity の必須 entry を満たす manifest を返す。
func sampleArtifactManifest(t *testing.T) ArtifactManifest {
	t.Helper()
	entries := make([]ArtifactEntry, 0, 4)
	_, observationPayload, err := EncodePullRequestObservation(samplePullRequestObservation())
	if err != nil {
		t.Fatalf("pull request observation encode error: %v", err)
	}
	observationEntry, err := NewArtifactEntry(string(ArtifactNamePullRequestObservation), observationPayload)
	if err != nil {
		t.Fatalf("pull request observation entry: %v", err)
	}
	entries = append(entries,
		sampleOpaqueArtifact(ArtifactNameTestPlan, MediaTypeMarkdown, []byte("- AC-1 を検証する test を追加する\n")),
		sampleOpaqueArtifact(ArtifactNameRedEvidence, "text/plain; charset=utf-8", sampleREDEvidence),
		sampleOpaqueArtifact(ArtifactNameSourceBundle, "application/octet-stream", []byte("source-bundle-bytes")),
		observationEntry,
	)
	return ArtifactManifest{Schema: ArtifactManifestSchemaV1Alpha1, Entries: entries}
}

// samplePullRequestObservation は publish 直後の draft PR の観測を返す。
func samplePullRequestObservation() PullRequestObservation {
	return PullRequestObservation{
		Schema:      PullRequestObservationSchemaV1Alpha1,
		PullRequest: PullRequestRef{Owner: "mrbaron3", Repository: "kudo", Number: 57},
		State:       PullRequestOpen,
		Draft:       true,
		HeadSHA:     sampleHeadSHA,
		BaseRef:     "main",
		BodyDigest:  SHA256([]byte("## 概要\n\nKudo が管理する draft PR body\n")),
	}
}

func requirePullRequestObservationRef(t *testing.T, obs PullRequestObservation) PullRequestObservationRef {
	t.Helper()
	ref, _, err := EncodePullRequestObservation(obs)
	if err != nil {
		t.Fatalf("pull request observation encode error: %v", err)
	}
	return ref
}

// sampleFinalImplementationManifest は final_implementation の必須 entry を満たす
// manifest を返す。test_validity の集合を含み、実装 gate 固有の証跡を追加する。
func sampleFinalImplementationManifest(t *testing.T) ArtifactManifest {
	t.Helper()
	manifest := sampleArtifactManifest(t)
	manifest.Entries = append(manifest.Entries,
		sampleOpaqueArtifact(ArtifactNameTestValidityResult, MediaTypeYAML, []byte("verdict: approve\n")),
		sampleOpaqueArtifact(ArtifactNameGreenEvidence, "text/plain; charset=utf-8", []byte("$ go test ./...\nok\n")),
		sampleOpaqueArtifact(ArtifactNameCheckEvidence, "text/plain; charset=utf-8", []byte("$ mise run check\nok\n")),
		sampleOpaqueArtifact(ArtifactNamePullRequestDraft, MediaTypeMarkdown, []byte("## 概要\n\n実装した\n")),
	)
	return manifest
}

func requireArtifactManifestRef(t *testing.T, manifest ArtifactManifest) ArtifactManifestRef {
	t.Helper()
	ref, _, err := EncodeArtifactManifest(manifest)
	if err != nil {
		t.Fatalf("artifact manifest encode error: %v", err)
	}
	return ref
}

func sampleReviewRequest(t *testing.T) ReviewRequest {
	t.Helper()
	compiled, manifestRef, policyRef := sampleResolvedInput(t)
	observation := samplePullRequestObservation()
	return ReviewRequest{
		Schema:                 ReviewRequestSchemaV1Alpha1,
		RequestID:              "01KUDOEXAMPLE",
		Kind:                   ReviewTestValidity,
		ProducerRunID:          "run-01",
		Issue:                  compiled.TaskContext.Issue,
		PullRequest:            observation.PullRequest,
		Observation:            compiled.ObservationRef,
		PullRequestObservation: requirePullRequestObservationRef(t, observation),
		HeadSHA:                sampleHeadSHA,
		ContextManifest:        manifestRef,
		ExecutionPolicy:        policyRef,
		AgentPackage: AgentPackageRef{
			Schema: AgentPackageSchemaV1Alpha1,
			Digest: SHA256([]byte("test_validity agent package")),
		},
		ArtifactManifest: requireArtifactManifestRef(t, sampleArtifactManifest(t)),
		PolicyRefs: []string{
			"docs/spec/05_design/contracts/issue-contract-v1alpha1.md",
			"agent-packages/test_validity/v1alpha1/instructions.md",
		},
		CreatedAt: sampleCreatedAt,
	}
}

// sampleFinalReviewRequest は final_implementation の標準 policy と必須 entry を備えた
// request を返す。
func sampleFinalReviewRequest(t *testing.T) ReviewRequest {
	t.Helper()
	req := sampleReviewRequest(t)
	req.Kind = ReviewFinalImplementation
	req.PolicyRefs = []string{"docs/spec/05_design/review-policies/final-implementation-v1alpha1.md"}
	req.ArtifactManifest = requireArtifactManifestRef(t, sampleFinalImplementationManifest(t))
	return req
}

// sampleFinalPerspectives は全条件付き観点への applicability 宣言を返す。
func sampleFinalPerspectives() []PerspectiveDecision {
	evidence := []Digest{SHA256([]byte("task-context"))}
	return []PerspectiveDecision{
		{Perspective: PerspectiveUX, Applicable: false, Reason: "no-user-facing-surface-change", EvidenceRefs: evidence},
		{Perspective: PerspectiveAccessibility, Applicable: false, Reason: "no-ui-surface-change", EvidenceRefs: evidence},
		{Perspective: PerspectiveTypeDesign, Applicable: true, Reason: "exported-signature-changed", EvidenceRefs: []Digest{SHA256([]byte("diff"))}},
		{Perspective: PerspectivePerformance, Applicable: false, Reason: "no-bound-and-no-perf-surface", EvidenceRefs: evidence},
	}
}

// sampleFinalReviewResult は applicability 宣言を備えた final review の approve を返す。
func sampleFinalReviewResult(t *testing.T, req ReviewRequest) ReviewResult {
	t.Helper()
	digest, err := ReviewRequestDigest(req)
	if err != nil {
		t.Fatalf("review request digest error: %v", err)
	}
	return ReviewResult{
		Schema:        ReviewResultSchemaV1Alpha1,
		RequestDigest: digest,
		ReviewRunID:   "review-02",
		Verdict:       VerdictApprove,
		Perspectives:  sampleFinalPerspectives(),
		CreatedAt:     sampleCreatedAt.Add(time.Minute),
	}
}

func sampleReviewResult(t *testing.T, req ReviewRequest) ReviewResult {
	t.Helper()
	digest, err := ReviewRequestDigest(req)
	if err != nil {
		t.Fatalf("review request digest error: %v", err)
	}
	return ReviewResult{
		Schema:        ReviewResultSchemaV1Alpha1,
		RequestDigest: digest,
		ReviewRunID:   "review-01",
		Verdict:       VerdictRequestChanges,
		Findings: []ReviewFinding{{
			ID:           "F-1",
			Severity:     SeverityBlocking,
			Summary:      "AC-2 を検証する test が無い",
			Expected:     "AC-1 と AC-2 の観測可能な結果を検証する",
			Observed:     "test plan と patch は AC-1 のみを参照している",
			EvidenceRefs: []Digest{SHA256([]byte("test-plan"))},
		}},
		CreatedAt: sampleCreatedAt.Add(time.Minute),
	}
}

func requireReviewRequestDigest(t *testing.T, req ReviewRequest) Digest {
	t.Helper()
	digest, err := ReviewRequestDigest(req)
	if err != nil {
		t.Fatalf("review request digest error: %v", err)
	}
	return digest
}

func TestReviewCanonicalGolden(t *testing.T) {
	req := sampleReviewRequest(t)
	requireGolden(t, encodeReviewRequestIdentity(req), "review-request.yaml")
	requireGolden(t, encodeReviewResultIdentity(sampleReviewResult(t, req)), "review-result.yaml")

	_, payload, err := EncodeArtifactManifest(sampleArtifactManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	requireGolden(t, payload.Data, "artifact-manifest.yaml")

	finalReq := sampleFinalReviewRequest(t)
	requireGolden(t, encodeReviewResultIdentity(sampleFinalReviewResult(t, finalReq)), "review-result-final.yaml")

	identity := string(encodeReviewRequestIdentity(req))
	for _, excluded := range []string{"issueObservation", "pullRequestObservation", "bodyDigest", "observedAt", "requestId", "createdAt"} {
		if strings.Contains(identity, excluded) {
			t.Fatalf("Review Request identity に %q が含まれる", excluded)
		}
	}
}

func TestReviewRequestDigestCanonicalizesIssueReferenceCase(t *testing.T) {
	req := sampleReviewRequest(t)
	want := requireReviewRequestDigest(t, req)

	got := sampleReviewRequest(t)
	got.Issue = IssueRef{Owner: "MrBaron3", Repository: "Kudo", Number: req.Issue.Number}
	if digest := requireReviewRequestDigest(t, got); digest != want {
		t.Fatalf("issue reference の case 差分で digest が変化: got %s, want %s", digest, want)
	}
}

func TestReviewRequestValidation(t *testing.T) {
	tests := map[string]func(*ReviewRequest){
		"schema":       func(r *ReviewRequest) { r.Schema = "kudo.review-request/v2" },
		"unknown kind": func(r *ReviewRequest) { r.Kind = "security_review" },
		"request id":   func(r *ReviewRequest) { r.RequestID = "" },
		"producer run": func(r *ReviewRequest) { r.ProducerRunID = "run 01" },
		"issue":        func(r *ReviewRequest) { r.Issue = IssueRef{} },
		"pull request": func(r *ReviewRequest) { r.PullRequest = PullRequestRef{} },
		"pull request number": func(r *ReviewRequest) {
			r.PullRequest.Number = 0
		},
		"pull request repository": func(r *ReviewRequest) {
			r.PullRequest = PullRequestRef{Owner: "other", Repository: "repo", Number: 57}
		},
		"head": func(r *ReviewRequest) { r.HeadSHA = "" },
		"observation schema": func(r *ReviewRequest) {
			r.Observation.Schema = "kudo.task-context/v1alpha1"
		},
		"observation digest": func(r *ReviewRequest) { r.Observation.Digest = "sha256:nope" },
		"pr observation schema": func(r *ReviewRequest) {
			r.PullRequestObservation.Schema = "kudo.issue-observation/v1alpha1"
		},
		"pr observation digest": func(r *ReviewRequest) { r.PullRequestObservation.Digest = "sha256:nope" },
		"manifest schema":       func(r *ReviewRequest) { r.ContextManifest.Schema = "kudo.artifact-manifest/v1alpha1" },
		"policy schema":         func(r *ReviewRequest) { r.ExecutionPolicy.Schema = "" },
		"agent package schema":  func(r *ReviewRequest) { r.AgentPackage.Schema = "kudo.agent-package/" },
		"agent package digest":  func(r *ReviewRequest) { r.AgentPackage.Digest = "sha256:nope" },
		"artifact manifest schema": func(r *ReviewRequest) {
			r.ArtifactManifest.Schema = "kudo.context-manifest/v1alpha1"
		},
		"artifact manifest digest": func(r *ReviewRequest) { r.ArtifactManifest.Digest = "" },
		"empty policy refs":        func(r *ReviewRequest) { r.PolicyRefs = nil },
		"policy ref path":          func(r *ReviewRequest) { r.PolicyRefs = []string{"../secret.md"} },
		"missing required policy": func(r *ReviewRequest) {
			r.PolicyRefs = []string{"docs/spec/05_design/contracts/issue-contract-v1alpha1.md"}
		},
		"created at": func(r *ReviewRequest) { r.CreatedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := sampleReviewRequest(t)
			mutate(&got)
			if err := ValidateReviewRequest(got); err == nil {
				t.Fatal("invalid Review Request を受理した")
			}
			if _, err := ReviewRequestDigest(got); err == nil {
				t.Fatal("invalid Review Request の digest を返した")
			}
		})
	}

	if err := ValidateReviewRequest(sampleReviewRequest(t)); err != nil {
		t.Fatalf("valid Review Request を拒否した: %v", err)
	}
}

// kind の標準 policy を欠く request は、reviewer に評価基準を推測させないため
// provider へ渡す前に拒否する。path の形式検証だけでは、別の任意 policy だけを
// 積んだ request が通ってしまう（PR #51 review 指摘）。
func TestReviewRequestRequiresKindPolicyRef(t *testing.T) {
	for kind, build := range map[ReviewKind]func(*testing.T) ReviewRequest{
		ReviewTestValidity:        sampleReviewRequest,
		ReviewFinalImplementation: sampleFinalReviewRequest,
	} {
		t.Run(string(kind), func(t *testing.T) {
			req := build(t)
			if err := ValidateReviewRequest(req); err != nil {
				t.Fatalf("標準 policy を備えた request を拒否した: %v", err)
			}

			required := RequiredReviewPolicyRefs(kind)
			if len(required) == 0 {
				t.Fatalf("kind %q の標準 policy が宣言されていない", kind)
			}
			req.PolicyRefs = []string{"docs/spec/05_design/contracts/issue-contract-v1alpha1.md"}
			err := ValidateReviewRequest(req)
			if !errors.Is(err, ProtocolKindConstraint) {
				t.Fatalf("標準 policy の欠落が %q へ分類されない: %v", ProtocolKindConstraint, err)
			}
			// 何を足せば通るかが error から読めなければ、producer は総当たりで再試行する。
			for _, ref := range required {
				if !strings.Contains(err.Error(), ref) {
					t.Fatalf("欠落した policy ref %q が error に現れない: %v", ref, err)
				}
			}

			// 標準 policy さえ含めば repository 固有 policy の追加は妨げない。
			req.PolicyRefs = append([]string{"docs/spec/05_design/04_github-routing.md"}, required...)
			if err := ValidateReviewRequest(req); err != nil {
				t.Fatalf("標準 policy + 追加 policy の request を拒否した: %v", err)
			}
		})
	}
}

func TestReviewResultValidation(t *testing.T) {
	req := sampleReviewRequest(t)
	valid := sampleReviewResult(t, req)

	tests := map[string]func(*ReviewResult){
		"schema":         func(r *ReviewResult) { r.Schema = "kudo.worker-result/v1alpha1" },
		"request digest": func(r *ReviewResult) { r.RequestDigest = "sha256:nope" },
		"review run":     func(r *ReviewResult) { r.ReviewRunID = "" },
		"unknown verdict": func(r *ReviewResult) {
			r.Verdict = "looks_good"
		},
		"finding id":       func(r *ReviewResult) { r.Findings[0].ID = "" },
		"finding severity": func(r *ReviewResult) { r.Findings[0].Severity = "nit" },
		"finding summary":  func(r *ReviewResult) { r.Findings[0].Summary = " " },
		"finding expected": func(r *ReviewResult) { r.Findings[0].Expected = "" },
		"finding observed": func(r *ReviewResult) { r.Findings[0].Observed = "" },
		"finding evidence": func(r *ReviewResult) { r.Findings[0].EvidenceRefs = nil },
		"finding evidence digest": func(r *ReviewResult) {
			r.Findings[0].EvidenceRefs = []Digest{"sha256:nope"}
		},
		"duplicate finding id": func(r *ReviewResult) {
			r.Findings = append(r.Findings, r.Findings[0])
		},
		"control character": func(r *ReviewResult) { r.Findings[0].Summary = "AC-2\x00" },
		"created at":        func(r *ReviewResult) { r.CreatedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := valid
			got.Findings = append([]ReviewFinding(nil), valid.Findings...)
			got.Findings[0].EvidenceRefs = append([]Digest(nil), valid.Findings[0].EvidenceRefs...)
			mutate(&got)
			if err := ValidateReviewResult(got); err == nil {
				t.Fatal("invalid Review Result を受理した")
			}
		})
	}

	if err := ValidateReviewResult(valid); err != nil {
		t.Fatalf("valid Review Result を拒否した: %v", err)
	}
}

// verdict と finding の対応を型の外側で崩さない。blocking finding の有無と
// verdict が矛盾する Result は、gate 判定を誤らせるため受理しない。
func TestReviewVerdictMatchesFindings(t *testing.T) {
	req := sampleReviewRequest(t)
	base := sampleReviewResult(t, req)

	advisory := base.Findings[0]
	advisory.Severity = SeverityAdvisory

	tests := map[string]struct {
		verdict  ReviewVerdict
		findings []ReviewFinding
		accept   bool
	}{
		"approve without findings":        {VerdictApprove, nil, true},
		"approve with advisory finding":   {VerdictApprove, []ReviewFinding{advisory}, true},
		"approve with blocking finding":   {VerdictApprove, base.Findings, false},
		"request_changes with blocking":   {VerdictRequestChanges, base.Findings, true},
		"request_changes without finding": {VerdictRequestChanges, nil, false},
		"request_changes advisory only":   {VerdictRequestChanges, []ReviewFinding{advisory}, false},
		"needs_human with blocking":       {VerdictNeedsHuman, base.Findings, true},
		"needs_human without finding":     {VerdictNeedsHuman, nil, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := base
			got.Verdict = tt.verdict
			got.Findings = tt.findings
			err := ValidateReviewResult(got)
			if tt.accept && err != nil {
				t.Fatalf("正当な verdict/finding の組を拒否した: %v", err)
			}
			if !tt.accept && err == nil {
				t.Fatal("verdict と finding が矛盾する Result を受理した")
			}
		})
	}
}

// applicability 宣言は単体でも語彙・一意性・理由 code・根拠を検証する。
// 自由記述の理由を許すと Controller と audit は宣言を分類できない。
func TestReviewResultPerspectiveValidation(t *testing.T) {
	req := sampleFinalReviewRequest(t)
	valid := sampleFinalReviewResult(t, req)
	if err := ValidateReviewResult(valid); err != nil {
		t.Fatalf("valid な applicability 宣言を拒否した: %v", err)
	}

	tests := map[string]func(*ReviewResult){
		"unknown perspective": func(r *ReviewResult) { r.Perspectives[0].Perspective = "security" },
		"duplicate":           func(r *ReviewResult) { r.Perspectives[1].Perspective = r.Perspectives[0].Perspective },
		"empty reason":        func(r *ReviewResult) { r.Perspectives[0].Reason = "" },
		"free-form reason":    func(r *ReviewResult) { r.Perspectives[0].Reason = "UI を変えていないため" },
		"reason with spaces":  func(r *ReviewResult) { r.Perspectives[0].Reason = "no surface" },
		"reason with case":    func(r *ReviewResult) { r.Perspectives[0].Reason = "No-Surface" },
		"no evidence":         func(r *ReviewResult) { r.Perspectives[0].EvidenceRefs = nil },
		"invalid evidence":    func(r *ReviewResult) { r.Perspectives[0].EvidenceRefs = []Digest{"sha256:nope"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := valid
			got.Perspectives = slices.Clone(valid.Perspectives)
			mutate(&got)
			if err := ValidateReviewResult(got); err == nil {
				t.Fatal("invalid な applicability 宣言を受理した")
			}
		})
	}
}

// 宣言の完全性は kind に依存するため binding 境界で検証する。final_implementation は
// 全条件付き観点への態度表明を欠く Result を受理せず、test_validity は宣言を持てない。
// 宣言の欠落は品質 verdict ではなく protocol violation である。
func TestBindReviewResultRequiresPerspectiveCompleteness(t *testing.T) {
	req := sampleFinalReviewRequest(t)
	result := sampleFinalReviewResult(t, req)
	if err := BindReviewResult(req, result); err != nil {
		t.Fatalf("全観点を宣言した final Result を拒否した: %v", err)
	}

	// 1 件ずつ落として、必須集合の全要素が実際に gate として働くことを確かめる。
	for i, decision := range result.Perspectives {
		missing := result
		missing.Perspectives = slices.Delete(slices.Clone(result.Perspectives), i, i+1)
		err := BindReviewResult(req, missing)
		if !errors.Is(err, ProtocolKindConstraint) {
			t.Fatalf("宣言 %q の欠落が %q へ分類されない: %v", decision.Perspective, ProtocolKindConstraint, err)
		}
		if !strings.Contains(err.Error(), string(decision.Perspective)) {
			t.Fatalf("欠落した観点 %q が error に現れない: %v", decision.Perspective, err)
		}
	}

	empty := result
	empty.Perspectives = nil
	if err := BindReviewResult(req, empty); err == nil {
		t.Fatal("applicability 宣言を持たない final Result を受理した")
	}

	testReq := sampleReviewRequest(t)
	testResult := sampleReviewResult(t, testReq)
	testResult.Perspectives = sampleFinalPerspectives()
	if err := BindReviewResult(testReq, testResult); err == nil {
		t.Fatal("条件付き観点を持たない kind の Result が宣言を持ったまま受理された")
	}
}

// 宣言は判断の一部として Result identity へ寄与し、並びは寄与しない。
func TestReviewResultDigestCoversPerspectives(t *testing.T) {
	req := sampleFinalReviewRequest(t)
	base := sampleFinalReviewResult(t, req)
	want, err := ReviewResultDigest(base)
	if err != nil {
		t.Fatal(err)
	}

	flipped := base
	flipped.Perspectives = slices.Clone(base.Perspectives)
	flipped.Perspectives[0].Applicable = !flipped.Perspectives[0].Applicable
	digest, err := ReviewResultDigest(flipped)
	if err != nil {
		t.Fatal(err)
	}
	if digest == want {
		t.Fatal("applicable の変更で Result digest が変わらない")
	}

	reordered := base
	reordered.Perspectives = slices.Clone(base.Perspectives)
	slices.Reverse(reordered.Perspectives)
	digest, err = ReviewResultDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if digest != want {
		t.Fatal("宣言の並び替えで Result digest が変化")
	}
}

// transport/execution failure は品質 verdict へ変換しない。
func TestReviewVerdictRejectsExecutionOutcomes(t *testing.T) {
	req := sampleReviewRequest(t)
	rejected := []string{
		string(OutcomeSucceeded),
		string(OutcomeStaleInput),
		string(OutcomeFailedTerminal),
		string(FailureTimeout),
		string(FailureProviderCrash),
		string(FailureGitHubTransport),
	}
	for _, verdict := range rejected {
		t.Run(verdict, func(t *testing.T) {
			got := sampleReviewResult(t, req)
			got.Verdict = ReviewVerdict(verdict)
			if err := ValidateReviewResult(got); err == nil {
				t.Fatalf("execution outcome %q を quality verdict として受理した", verdict)
			}
		})
	}
}

func TestBindReviewResult(t *testing.T) {
	req := sampleReviewRequest(t)
	result := sampleReviewResult(t, req)
	if err := BindReviewResult(req, result); err != nil {
		t.Fatalf("対応する Review Result を拒否した: %v", err)
	}

	changes := map[string]func(*ReviewRequest){
		"context manifest":  func(r *ReviewRequest) { r.ContextManifest.Digest = SHA256([]byte("別 manifest")) },
		"execution policy":  func(r *ReviewRequest) { r.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
		"head":              func(r *ReviewRequest) { r.HeadSHA = sampleNextSHA },
		"artifact manifest": func(r *ReviewRequest) { r.ArtifactManifest.Digest = SHA256([]byte("別 artifact")) },
		"policy refs":       func(r *ReviewRequest) { r.PolicyRefs = []string{"docs/spec/05_design/02_workflow.md"} },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			changed := sampleReviewRequest(t)
			mutate(&changed)
			if err := BindReviewResult(changed, result); err == nil {
				t.Fatal("semantic identity が変わった Request へ古い Result を bind した")
			}
		})
	}

	t.Run("observation only", func(t *testing.T) {
		changed := sampleReviewRequest(t)
		changed.Observation.Digest = SHA256([]byte("別 raw body"))
		if err := BindReviewResult(changed, result); err != nil {
			t.Fatalf("Observation 差分だけで binding が壊れた: %v", err)
		}
	})

	t.Run("invalid result", func(t *testing.T) {
		invalid := result
		invalid.ReviewRunID = ""
		if err := BindReviewResult(req, invalid); err == nil {
			t.Fatal("invalid Review Result を bind した")
		}
	})
}

// Review Result は後続 gate から digest で参照される（final review の Artifact Manifest は
// approved な test validity Result を含む）。verdict と finding は identity へ寄与し、
// review Run と作成時刻は寄与しない。
func TestReviewResultDigest(t *testing.T) {
	req := sampleReviewRequest(t)
	base := sampleReviewResult(t, req)
	want, err := ReviewResultDigest(base)
	if err != nil {
		t.Fatal(err)
	}

	same := map[string]func(*ReviewResult){
		"review run": func(r *ReviewResult) { r.ReviewRunID = "review-02" },
		"created at": func(r *ReviewResult) { r.CreatedAt = sampleCreatedAt.Add(time.Hour) },
	}
	for name, mutate := range same {
		t.Run(name, func(t *testing.T) {
			got := base
			mutate(&got)
			digest, err := ReviewResultDigest(got)
			if err != nil {
				t.Fatal(err)
			}
			if digest != want {
				t.Fatal("identity に寄与しない差分で Review Result digest が変化")
			}
		})
	}

	changed := map[string]func(*ReviewResult){
		"verdict": func(r *ReviewResult) {
			r.Verdict = VerdictNeedsHuman
		},
		"finding severity": func(r *ReviewResult) {
			r.Findings = []ReviewFinding{{
				ID:           base.Findings[0].ID,
				Severity:     SeverityBlocking,
				Summary:      "別の blocking finding",
				Expected:     base.Findings[0].Expected,
				Observed:     base.Findings[0].Observed,
				EvidenceRefs: base.Findings[0].EvidenceRefs,
			}}
		},
		"request digest": func(r *ReviewResult) { r.RequestDigest = SHA256([]byte("別 request")) },
	}
	for name, mutate := range changed {
		t.Run(name, func(t *testing.T) {
			got := base
			mutate(&got)
			digest, err := ReviewResultDigest(got)
			if err != nil {
				t.Fatal(err)
			}
			if digest == want {
				t.Fatal("verdict/finding/request の変更で Review Result digest が変わらない")
			}
		})
	}

	// finding の並びは reviewer の出力順であり「同じ判断」の一部ではない。
	// model provider は順序を再現しないため、並びが identity に効くと同じ判断が
	// 別 Result として記録され、重複判定と再利用が壊れる。
	t.Run("findings 並び替え", func(t *testing.T) {
		second := base.Findings[0]
		second.ID = "F-2"
		second.Severity = SeverityAdvisory
		second.Summary = "test 名が AC を参照していない"

		ordered := base
		ordered.Findings = []ReviewFinding{base.Findings[0], second}
		reordered := base
		reordered.Findings = []ReviewFinding{second, base.Findings[0]}

		orderedDigest, err := ReviewResultDigest(ordered)
		if err != nil {
			t.Fatal(err)
		}
		reorderedDigest, err := ReviewResultDigest(reordered)
		if err != nil {
			t.Fatal(err)
		}
		if orderedDigest != reorderedDigest {
			t.Fatal("finding の並び替えで Review Result digest が変化")
		}
	})

	invalid := base
	invalid.Verdict = VerdictApprove
	if _, err := ReviewResultDigest(invalid); err == nil {
		t.Fatal("invalid Review Result の digest を返した")
	}
}

// attempt failure は verdict と同じ field で表現しない。両方を持つ attempt outcome、
// どちらも持たない attempt outcome はいずれも受理しない。
func TestReviewAttemptOutcomeSeparatesVerdictAndFailure(t *testing.T) {
	req := sampleReviewRequest(t)
	result := sampleReviewResult(t, req)
	failure := sampleAttemptFailure()

	if err := (ReviewAttemptOutcome{Result: &result}).Validate(); err != nil {
		t.Fatalf("verdict だけを持つ attempt outcome を拒否した: %v", err)
	}
	if err := (ReviewAttemptOutcome{Failure: &failure}).Validate(); err != nil {
		t.Fatalf("failure だけを持つ attempt outcome を拒否した: %v", err)
	}
	if err := (ReviewAttemptOutcome{Result: &result, Failure: &failure}).Validate(); err == nil {
		t.Fatal("verdict と failure を同時に持つ attempt outcome を受理した")
	}
	if err := (ReviewAttemptOutcome{}).Validate(); err == nil {
		t.Fatal("結末を持たない attempt outcome を受理した")
	}
}

func TestArtifactManifestIdentity(t *testing.T) {
	base := sampleArtifactManifest(t)
	baseRef, basePayload, err := EncodeArtifactManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	if baseRef.Schema != ArtifactManifestSchemaV1Alpha1 || baseRef.Digest != basePayload.Digest {
		t.Fatalf("ArtifactManifestRef が payload と一致しない: %+v", baseRef)
	}

	reordered := ArtifactManifest{Schema: base.Schema, Entries: append([]ArtifactEntry(nil), base.Entries...)}
	reordered.Entries[0], reordered.Entries[len(reordered.Entries)-1] = reordered.Entries[len(reordered.Entries)-1], reordered.Entries[0]
	if requireArtifactManifestRef(t, reordered) != baseRef {
		t.Fatal("logical name で引く manifest が並び順で別 identity になった")
	}

	variants := map[string]func(*ArtifactManifest){
		"digest":     func(m *ArtifactManifest) { m.Entries[0].Digest = SHA256([]byte("別 bytes")) },
		"length":     func(m *ArtifactManifest) { m.Entries[0].Length++ },
		"media type": func(m *ArtifactManifest) { m.Entries[0].MediaType = "text/plain; charset=utf-8" },
		"name":       func(m *ArtifactManifest) { m.Entries[0].Name = "renamed-artifact" },
		"entry 追加": func(m *ArtifactManifest) {
			m.Entries = append(m.Entries, ArtifactEntry{
				Name:      "green-evidence",
				MediaType: "text/plain; charset=utf-8",
				Length:    3,
				Digest:    SHA256([]byte("ok\n")),
			})
		},
		"entry 削除": func(m *ArtifactManifest) { m.Entries = m.Entries[1:] },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := ArtifactManifest{Schema: base.Schema, Entries: append([]ArtifactEntry(nil), base.Entries...)}
			mutate(&got)
			if requireArtifactManifestRef(t, got) == baseRef {
				t.Fatal("artifact 差分で ArtifactManifestRef が変わらない")
			}
		})
	}

	if _, err := ReadArtifactManifestArtifact(baseRef, basePayload); err != nil {
		t.Fatalf("ref/payload の読み出しに失敗: %v", err)
	}
	wrongSchema := baseRef
	wrongSchema.Schema = ContextManifestSchemaV1Alpha1
	if _, err := ReadArtifactManifestArtifact(wrongSchema, basePayload); err == nil {
		t.Fatal("schema と digest の組を崩した ref を受理した")
	}
}

func TestArtifactManifestValidation(t *testing.T) {
	base := sampleArtifactManifest(t)
	tests := map[string]func(*ArtifactManifest){
		"schema":         func(m *ArtifactManifest) { m.Schema = "kudo.artifact-manifest/v2" },
		"empty entries":  func(m *ArtifactManifest) { m.Entries = nil },
		"empty name":     func(m *ArtifactManifest) { m.Entries[0].Name = "" },
		"name character": func(m *ArtifactManifest) { m.Entries[0].Name = "Test Plan" },
		"duplicate name": func(m *ArtifactManifest) { m.Entries[0].Name = m.Entries[1].Name },
		"media type":     func(m *ArtifactManifest) { m.Entries[0].MediaType = "" },
		"negative length": func(m *ArtifactManifest) {
			m.Entries[0].Length = -1
		},
		"digest": func(m *ArtifactManifest) { m.Entries[0].Digest = "sha256:nope" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := ArtifactManifest{Schema: base.Schema, Entries: append([]ArtifactEntry(nil), base.Entries...)}
			mutate(&got)
			if _, _, err := EncodeArtifactManifest(got); err == nil {
				t.Fatal("invalid Artifact Manifest を受理した")
			}
		})
	}
}

// entry の length/digest/media type を producer に自己申告させると、bytes と
// manifest が食い違ったまま review へ渡る。payload から導出する経路を用意する。
func TestNewArtifactEntryDerivesMetadataFromPayload(t *testing.T) {
	_, payload, err := EncodePullRequestObservation(samplePullRequestObservation())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := NewArtifactEntry(string(ArtifactNamePullRequestObservation), payload)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Digest != payload.Digest ||
		entry.MediaType != payload.MediaType ||
		entry.Length != int64(len(payload.Data)) {
		t.Fatalf("payload から導出されていない: %+v", entry)
	}

	tampered := payload
	tampered.Data = append([]byte(nil), tampered.Data...)
	tampered.Data[0] = 'X'
	if _, err := NewArtifactEntry(string(ArtifactNamePullRequestObservation), tampered); err == nil {
		t.Fatal("digest と bytes が一致しない payload から entry を作った")
	}
	if _, err := NewArtifactEntry("Pull Request Observation", payload); err == nil {
		t.Fatal("不正な logical name を受理した")
	}
}
