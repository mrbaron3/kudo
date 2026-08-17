package contract

import (
	"strings"
	"testing"
	"time"
)

var sampleREDEvidence = []byte("$ go test ./...\nFAIL\texit status 1\n")

func sampleArtifactManifest(t *testing.T) ArtifactManifest {
	t.Helper()
	compiled, _, _ := sampleResolvedInput(t)
	_, manifestPayload, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatalf("manifest encode error: %v", err)
	}

	named := []struct {
		name    string
		payload ArtifactPayload
	}{
		{"task-context", compiled.TaskContextPayload},
		{"issue-observation", compiled.ObservationPayload},
		{"raw-issue-body", compiled.RawBodyPayload},
		{"context-manifest", manifestPayload},
	}
	entries := make([]ArtifactEntry, 0, len(named)+1)
	for _, item := range named {
		entry, err := NewArtifactEntry(item.name, item.payload)
		if err != nil {
			t.Fatalf("artifact entry %q: %v", item.name, err)
		}
		entries = append(entries, entry)
	}
	// Artifact Store の payload 型を持たない evidence も logical name で列挙できる
	entries = append(entries, ArtifactEntry{
		Name:      "red-evidence",
		MediaType: "text/plain; charset=utf-8",
		Length:    int64(len(sampleREDEvidence)),
		Digest:    SHA256(sampleREDEvidence),
	})
	return ArtifactManifest{Schema: ArtifactManifestSchemaV1Alpha1, Entries: entries}
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
	return ReviewRequest{
		Schema:           ReviewRequestSchemaV1Alpha1,
		RequestID:        "01KUDOEXAMPLE",
		Kind:             ReviewTestValidity,
		ProducerRunID:    "run-01",
		Issue:            compiled.TaskContext.Issue,
		Observation:      compiled.ObservationRef,
		HeadSHA:          sampleHeadSHA,
		ContextManifest:  manifestRef,
		ExecutionPolicy:  policyRef,
		ArtifactManifest: requireArtifactManifestRef(t, sampleArtifactManifest(t)),
		PolicyRefs:       []string{"docs/contracts/issue-contract-v1alpha1.md"},
		CreatedAt:        sampleCreatedAt,
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

	identity := string(encodeReviewRequestIdentity(req))
	for _, excluded := range []string{"issueObservation", "bodyDigest", "observedAt", "requestId", "createdAt"} {
		if strings.Contains(identity, excluded) {
			t.Fatalf("Review Request identity に %q が含まれる", excluded)
		}
	}
}

func TestReviewRequestDigestExcludesAuditAndRunFields(t *testing.T) {
	base := sampleReviewRequest(t)
	want := requireReviewRequestDigest(t, base)

	variants := map[string]func(*ReviewRequest){
		"issue observation": func(r *ReviewRequest) { r.Observation.Digest = SHA256([]byte("別 raw body")) },
		"request id":        func(r *ReviewRequest) { r.RequestID = "01KUDOOTHER" },
		"producer run":      func(r *ReviewRequest) { r.ProducerRunID = "run-02" },
		"created at":        func(r *ReviewRequest) { r.CreatedAt = sampleCreatedAt.Add(time.Hour) },
		"issue reference case": func(r *ReviewRequest) {
			r.Issue = IssueRef{Owner: "MrBaron3", Repository: "Kudo", Number: r.Issue.Number}
		},
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := sampleReviewRequest(t)
			mutate(&got)
			if digest := requireReviewRequestDigest(t, got); digest != want {
				t.Fatalf("identity に寄与しない差分で digest が変化: got %s, want %s", digest, want)
			}
		})
	}
}

func TestReviewRequestDigestCoversSemanticIdentity(t *testing.T) {
	base := sampleReviewRequest(t)
	want := requireReviewRequestDigest(t, base)

	changedManifest := sampleArtifactManifest(t)
	changedManifest.Entries[0].Digest = SHA256([]byte("別 artifact"))

	variants := map[string]func(*ReviewRequest){
		"kind":             func(r *ReviewRequest) { r.Kind = ReviewFinalImplementation },
		"issue":            func(r *ReviewRequest) { r.Issue.Number = 11 },
		"head":             func(r *ReviewRequest) { r.HeadSHA = sampleNextSHA },
		"context manifest": func(r *ReviewRequest) { r.ContextManifest.Digest = SHA256([]byte("別 manifest")) },
		"manifest ref schema": func(r *ReviewRequest) {
			r.ContextManifest.Schema = "kudo.context-manifest/v1beta1"
		},
		"execution policy": func(r *ReviewRequest) { r.ExecutionPolicy.Digest = SHA256([]byte("別 policy")) },
		"artifact manifest": func(r *ReviewRequest) {
			r.ArtifactManifest = requireArtifactManifestRef(t, changedManifest)
		},
		"artifact manifest schema": func(r *ReviewRequest) {
			r.ArtifactManifest.Schema = "kudo.artifact-manifest/v1beta1"
		},
		"policy refs": func(r *ReviewRequest) { r.PolicyRefs = []string{"docs/workflow.md"} },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := sampleReviewRequest(t)
			mutate(&got)
			if requireReviewRequestDigest(t, got) == want {
				t.Fatal("semantic identity の変更で Review Request digest が変わらない")
			}
		})
	}
}

func TestReviewRequestValidation(t *testing.T) {
	tests := map[string]func(*ReviewRequest){
		"schema":       func(r *ReviewRequest) { r.Schema = "kudo.review-request/v2" },
		"unknown kind": func(r *ReviewRequest) { r.Kind = "security_review" },
		"request id":   func(r *ReviewRequest) { r.RequestID = "" },
		"producer run": func(r *ReviewRequest) { r.ProducerRunID = "run 01" },
		"issue":        func(r *ReviewRequest) { r.Issue = IssueRef{} },
		"head":         func(r *ReviewRequest) { r.HeadSHA = "" },
		"observation schema": func(r *ReviewRequest) {
			r.Observation.Schema = "kudo.task-context/v1alpha1"
		},
		"observation digest": func(r *ReviewRequest) { r.Observation.Digest = "sha256:nope" },
		"manifest schema":    func(r *ReviewRequest) { r.ContextManifest.Schema = "kudo.artifact-manifest/v1alpha1" },
		"policy schema":      func(r *ReviewRequest) { r.ExecutionPolicy.Schema = "" },
		"artifact manifest schema": func(r *ReviewRequest) {
			r.ArtifactManifest.Schema = "kudo.context-manifest/v1alpha1"
		},
		"artifact manifest digest": func(r *ReviewRequest) { r.ArtifactManifest.Digest = "" },
		"empty policy refs":        func(r *ReviewRequest) { r.PolicyRefs = nil },
		"policy ref path":          func(r *ReviewRequest) { r.PolicyRefs = []string{"../secret.md"} },
		"created at":               func(r *ReviewRequest) { r.CreatedAt = time.Time{} },
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
		"policy refs":       func(r *ReviewRequest) { r.PolicyRefs = []string{"docs/workflow.md"} },
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
		"name":       func(m *ArtifactManifest) { m.Entries[0].Name = "test-plan" },
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
	compiled, _, _ := sampleResolvedInput(t)
	entry, err := NewArtifactEntry("task-context", compiled.TaskContextPayload)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Digest != compiled.TaskContextPayload.Digest ||
		entry.MediaType != compiled.TaskContextPayload.MediaType ||
		entry.Length != int64(len(compiled.TaskContextPayload.Data)) {
		t.Fatalf("payload から導出されていない: %+v", entry)
	}

	tampered := compiled.TaskContextPayload
	tampered.Data = append([]byte(nil), tampered.Data...)
	tampered.Data[0] = 'X'
	if _, err := NewArtifactEntry("task-context", tampered); err == nil {
		t.Fatal("digest と bytes が一致しない payload から entry を作った")
	}
	if _, err := NewArtifactEntry("Task Context", compiled.TaskContextPayload); err == nil {
		t.Fatal("不正な logical name を受理した")
	}
}
