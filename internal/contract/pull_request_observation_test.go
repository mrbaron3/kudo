package contract

import (
	"testing"
)

func TestPullRequestObservationCanonicalGolden(t *testing.T) {
	_, payload, err := EncodePullRequestObservation(samplePullRequestObservation())
	if err != nil {
		t.Fatal(err)
	}
	requireGolden(t, payload.Data, "pull-request-observation.yaml")
}

// 同じ観測内容は常に同じ digest を持ち、状態が変われば別 identity になる。
// 観測時刻を canonical content に含めないため、同じ状態の再観測は lineage を伸ばさない。
func TestPullRequestObservationIdentity(t *testing.T) {
	base := samplePullRequestObservation()
	baseRef := requirePullRequestObservationRef(t, base)
	if requirePullRequestObservationRef(t, samplePullRequestObservation()) != baseRef {
		t.Fatal("同じ観測が別 identity になった")
	}

	variants := map[string]func(*PullRequestObservation){
		"state":       func(o *PullRequestObservation) { o.State = PullRequestClosed },
		"draft":       func(o *PullRequestObservation) { o.Draft = false },
		"head":        func(o *PullRequestObservation) { o.HeadSHA = sampleNextSHA },
		"base ref":    func(o *PullRequestObservation) { o.BaseRef = "develop" },
		"body digest": func(o *PullRequestObservation) { o.BodyDigest = SHA256([]byte("別 body")) },
		"pr number":   func(o *PullRequestObservation) { o.PullRequest.Number = 58 },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			got := samplePullRequestObservation()
			mutate(&got)
			if requirePullRequestObservationRef(t, got) == baseRef {
				t.Fatalf("%s の変化で observation identity が変わらない", name)
			}
		})
	}

	// owner/repository の case 差分は Issue reference と同じく別 identity にしない。
	cased := samplePullRequestObservation()
	cased.PullRequest.Owner = "MrBaron3"
	cased.PullRequest.Repository = "Kudo"
	if requirePullRequestObservationRef(t, cased) != baseRef {
		t.Fatal("PR reference の case 差分で observation identity が変化")
	}
}

func TestPullRequestObservationValidation(t *testing.T) {
	tests := map[string]func(*PullRequestObservation){
		"schema":       func(o *PullRequestObservation) { o.Schema = "kudo.issue-observation/v1alpha1" },
		"empty schema": func(o *PullRequestObservation) { o.Schema = "" },
		"pull request": func(o *PullRequestObservation) { o.PullRequest = PullRequestRef{} },
		"state":        func(o *PullRequestObservation) { o.State = "reopened" },
		// GitHub は draft のまま merge できない。矛盾した観測を保存できると、
		// finalize gate は「ready 済みか」を観測から判定できない。
		"merged draft": func(o *PullRequestObservation) { o.State = PullRequestMerged },
		"head":         func(o *PullRequestObservation) { o.HeadSHA = "HEAD" },
		"base ref":     func(o *PullRequestObservation) { o.BaseRef = "" },
		"base newline": func(o *PullRequestObservation) { o.BaseRef = "main\nother" },
		"body digest":  func(o *PullRequestObservation) { o.BodyDigest = "sha256:nope" },
		"empty digest": func(o *PullRequestObservation) { o.BodyDigest = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := samplePullRequestObservation()
			mutate(&got)
			if _, _, err := EncodePullRequestObservation(got); err == nil {
				t.Fatal("invalid な PR observation を受理した")
			}
		})
	}

	// merged 自体は draft でなければ正当な観測である。
	merged := samplePullRequestObservation()
	merged.State = PullRequestMerged
	merged.Draft = false
	if _, _, err := EncodePullRequestObservation(merged); err != nil {
		t.Fatalf("merged の観測を拒否した: %v", err)
	}
}

// ref と payload の binding は他の versioned artifact と同じ規則に従う。
func TestReadPullRequestObservationArtifact(t *testing.T) {
	ref, payload, err := EncodePullRequestObservation(samplePullRequestObservation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPullRequestObservationArtifact(ref, payload); err != nil {
		t.Fatalf("ref/payload の読み出しに失敗: %v", err)
	}

	wrongSchema := ref
	wrongSchema.Schema = "kudo.issue-observation/v1alpha1"
	if _, err := ReadPullRequestObservationArtifact(wrongSchema, payload); err == nil {
		t.Fatal("schema と digest の組を崩した ref を受理した")
	}

	tampered := payload
	tampered.Data = append([]byte(nil), payload.Data...)
	tampered.Data[0] = 'X'
	if _, err := ReadPullRequestObservationArtifact(ref, tampered); err == nil {
		t.Fatal("bytes を改変した payload を受理した")
	}
}
