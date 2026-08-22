package workflow

import (
	"reflect"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	testImplementerAppID int64 = 101
	testReviewerAppID    int64 = 202
	testLiveHead               = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testApprovedHead           = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func testDeriveConfig() DeriveConfig {
	return DeriveConfig{
		IssueNumber:              70,
		ImplementerCheckRunAppID: testImplementerAppID,
		ReviewerCheckRunAppID:    testReviewerAppID,
	}
}

func openObservation() Observation {
	return Observation{
		Issue: IssueObservation{
			State:                IssueOpen,
			DependenciesComplete: true,
		},
	}
}

func observationWithRun(prState PullRequestState) Observation {
	observation := openObservation()
	observation.Branch = &BranchObservation{
		Name:        "kudo/issue-70",
		Head:        testLiveHead,
		CommitValid: true,
	}
	observation.PullRequest = &PullRequestObservation{State: prState, Head: testLiveHead}
	return observation
}

func evidence(name, head string) CheckRunObservation {
	return CheckRunObservation{
		Name:      name,
		Head:      head,
		AppID:     testImplementerAppID,
		Reachable: true,
	}
}

func approval(kind contract.ReviewKind, head string, reachable bool) CheckRunObservation {
	name := CheckRunTestValidity
	if kind == contract.ReviewFinalImplementation {
		name = CheckRunFinalImplementation
	}
	return CheckRunObservation{
		Name:      name,
		Head:      head,
		AppID:     testReviewerAppID,
		Verdict:   contract.VerdictApprove,
		Reachable: reachable,
	}
}

func dispatch(operation contract.OperationKind) ReconcileAction {
	return ReconcileAction{Kind: ReconcileDispatchOperation, Operation: operation}
}

func requestReview(kind contract.ReviewKind) ReconcileAction {
	return ReconcileAction{Kind: ReconcileRequestReview, Review: kind}
}

// AC-1: workflow.md の Derived phases 表を上から順に評価し、各行に対応する
// snapshot から phase と次 action を決定論的に導出する。
func TestDeriveEvaluatesEveryDerivedPhaseInPriorityOrder(t *testing.T) {
	needsHuman := observationWithRun(PullRequestMerged)
	needsHuman.Issue.Labels = []LabelObservation{{Name: LabelNeedsHuman}}

	merged := observationWithRun(PullRequestMerged)
	superseded := observationWithRun(PullRequestClosed)

	merging := observationWithRun(PullRequestReady)
	merging.CheckRuns = []CheckRunObservation{
		approval(contract.ReviewFinalImplementation, testLiveHead, true),
	}

	finalizing := observationWithRun(PullRequestDraft)
	finalizing.CheckRuns = []CheckRunObservation{
		approval(contract.ReviewFinalImplementation, testLiveHead, true),
	}

	awaitingFinal := observationWithRun(PullRequestDraft)
	awaitingFinal.CheckRuns = []CheckRunObservation{
		evidence(CheckRunEvidenceGreen, testLiveHead),
		evidence(CheckRunEvidenceChecks, testLiveHead),
	}

	implementing := observationWithRun(PullRequestDraft)
	implementing.CheckRuns = []CheckRunObservation{
		approval(contract.ReviewTestValidity, testApprovedHead, true),
	}

	awaitingTest := observationWithRun(PullRequestDraft)
	awaitingTest.CheckRuns = []CheckRunObservation{
		evidence(CheckRunEvidenceRed, testLiveHead),
	}

	authoring := observationWithRun(PullRequestDraft)

	claimed := openObservation()
	claimed.Branch = &BranchObservation{
		Name:        "kudo/issue-70",
		Head:        testLiveHead,
		CommitValid: true,
	}

	candidate := openObservation()
	candidate.Issue.Labels = []LabelObservation{{Name: LabelReady}}

	tests := []struct {
		name        string
		observation Observation
		wantPhase   Phase
		wantAction  ReconcileAction
	}{
		{"needs human は merged より優先", needsHuman, PhaseNeedsHuman, ReconcileAction{Kind: ReconcileWaitForHuman}},
		{"merged", merged, PhaseMerged, ReconcileAction{}},
		{"superseded", superseded, PhaseSuperseded, ReconcileAction{}},
		{"merging pull request", merging, PhaseMergingPullRequest, dispatch(contract.OperationMergePullRequest)},
		{"finalizing pull request", finalizing, PhaseFinalizingPullRequest, dispatch(contract.OperationFinalizePullRequest)},
		{"awaiting final review", awaitingFinal, PhaseAwaitingFinalReview, requestReview(contract.ReviewFinalImplementation)},
		{"implementing", implementing, PhaseImplementing, dispatch(contract.OperationImplement)},
		{"awaiting test review", awaitingTest, PhaseAwaitingTestReview, requestReview(contract.ReviewTestValidity)},
		{"authoring tests", authoring, PhaseAuthoringTests, dispatch(contract.OperationAuthorTests)},
		{"claimed", claimed, PhaseClaimed, dispatch(contract.OperationClaim)},
		{"candidate", candidate, PhaseCandidate, dispatch(contract.OperationClaim)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := cloneObservationForTest(tc.observation)
			first := Derive(tc.observation, testDeriveConfig())
			second := Derive(tc.observation, testDeriveConfig())

			if first.Phase != tc.wantPhase || first.Next != tc.wantAction {
				t.Fatalf("decision = %+v, want phase=%q action=%+v", first, tc.wantPhase, tc.wantAction)
			}
			if !reflect.DeepEqual(second, first) {
				t.Fatalf("同じ snapshot から異なる decision: first=%+v second=%+v", first, second)
			}
			if !reflect.DeepEqual(tc.observation, before) {
				t.Fatal("Derive が入力 snapshot を変更した")
			}
		})
	}
}

// AC-2: table のどの行にも安全に一致しない中間状態は継続 action へ倒さず、
// needs_human と明示的な escalation action を返す。
func TestDeriveFailsClosedForUnknownOrCorruptObservations(t *testing.T) {
	corruptBranch := openObservation()
	corruptBranch.Branch = &BranchObservation{
		Name: "kudo/issue-70", Head: testLiveHead, CommitValid: false,
	}

	pullRequestWithoutBranch := openObservation()
	pullRequestWithoutBranch.PullRequest = &PullRequestObservation{State: PullRequestDraft, Head: testLiveHead}

	headMismatch := observationWithRun(PullRequestDraft)
	headMismatch.PullRequest.Head = testApprovedHead

	readyWithoutApproval := observationWithRun(PullRequestReady)

	for name, observation := range map[string]Observation{
		"branch commit が壊れている":         corruptBranch,
		"PR だけが存在する":                   pullRequestWithoutBranch,
		"branch と PR head が不一致":        headMismatch,
		"ready PR に final approve がない": readyWithoutApproval,
	} {
		t.Run(name, func(t *testing.T) {
			decision := Derive(observation, testDeriveConfig())
			if decision.Phase != PhaseNeedsHuman {
				t.Fatalf("phase = %q, want %q", decision.Phase, PhaseNeedsHuman)
			}
			if decision.Next.Kind != ReconcileEscalateHuman {
				t.Fatalf("next action = %+v, want explicit escalation", decision.Next)
			}
		})
	}
}

// Constraints: verdict は check run name だけでは足りず、Reviewer App identity と
// live head binding の両方を満たす必要がある。
func TestDeriveRejectsSpoofedOrStaleVerdictChecks(t *testing.T) {
	for name, verdict := range map[string]CheckRunObservation{
		"Implementer 名義": {
			Name: CheckRunTestValidity, Head: testLiveHead, AppID: testImplementerAppID,
			Verdict: contract.VerdictApprove, Reachable: true,
		},
		"別 head": approval(contract.ReviewTestValidity, testApprovedHead, false),
	} {
		t.Run(name, func(t *testing.T) {
			observation := observationWithRun(PullRequestDraft)
			observation.CheckRuns = []CheckRunObservation{
				evidence(CheckRunEvidenceRed, testLiveHead),
				verdict,
			}
			decision := Derive(observation, testDeriveConfig())
			if decision.Phase != PhaseAwaitingTestReview ||
				decision.Next != requestReview(contract.ReviewTestValidity) {
				t.Fatalf("spoof/stale verdict で gate が進んだ: %+v", decision)
			}
		})
	}
}

// AC-4: phase や attempt counter を保存・再利用せず、process restart 後に再構築した
// 同値 snapshot だけから同じ継続を再現できる。
func TestDeriveReconstructsTheSameContinuationAfterRestart(t *testing.T) {
	buildSnapshot := func() Observation {
		observation := observationWithRun(PullRequestDraft)
		observation.CheckRuns = []CheckRunObservation{
			approval(contract.ReviewTestValidity, testApprovedHead, true),
		}
		return observation
	}

	beforeRestart := Derive(buildSnapshot(), testDeriveConfig())
	// process-local な attempt tracker は再起動で失われても、phase 導出の入力にはならない。
	tracker, err := NewAttemptTracker(testRetryPolicy(), staticClock{}, noJitter{})
	if err != nil {
		t.Fatalf("attempt tracker: %v", err)
	}
	if _, err := tracker.Next("run-70/implement", contract.FailureTimeout); err != nil {
		t.Fatalf("attempt を進める: %v", err)
	}
	tracker = nil
	_ = tracker
	afterRestart := Derive(buildSnapshot(), testDeriveConfig())

	if beforeRestart != afterRestart {
		t.Fatalf("restart 前後で decision が変化: before=%+v after=%+v", beforeRestart, afterRestart)
	}
	if afterRestart.Phase != PhaseImplementing ||
		afterRestart.Next != dispatch(contract.OperationImplement) {
		t.Fatalf("restart 後の継続 = %+v", afterRestart)
	}
}

func cloneObservationForTest(observation Observation) Observation {
	clone := observation
	clone.Issue.Labels = append([]LabelObservation(nil), observation.Issue.Labels...)
	clone.Issue.LabelEvents = append([]LabelEventObservation(nil), observation.Issue.LabelEvents...)
	clone.CheckRuns = append([]CheckRunObservation(nil), observation.CheckRuns...)
	clone.Comments = append([]CommentObservation(nil), observation.Comments...)
	if observation.Branch != nil {
		branch := *observation.Branch
		clone.Branch = &branch
	}
	if observation.PullRequest != nil {
		pullRequest := *observation.PullRequest
		clone.PullRequest = &pullRequest
	}
	return clone
}
