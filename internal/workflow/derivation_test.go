package workflow

import (
	"reflect"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	derivedIssue                    = int64(70)
	derivedAssignee                 = "mrbaron3"
	derivedImplementerApp           = int64(101)
	derivedReviewerApp              = int64(202)
	derivedImplementerCommentAuthor = int64(303)
	derivedReviewerCommentAuthor    = int64(404)

	bootstrapCommit = "1111111111111111111111111111111111111111"
	redHead         = "2222222222222222222222222222222222222222"
	greenHead       = "3333333333333333333333333333333333333333"
)

func derivedConfig() DeriveConfig {
	return DeriveConfig{
		Issue:           derivedIssue,
		Assignee:        derivedAssignee,
		ReadyLabel:      LabelReady,
		NeedsHumanLabel: string(StatusNeedsHuman),
		Implementer: ActorIdentity{
			CheckRunAppID:   derivedImplementerApp,
			CommentAuthorID: derivedImplementerCommentAuthor,
		},
		Reviewer: ActorIdentity{
			CheckRunAppID:   derivedReviewerApp,
			CommentAuthorID: derivedReviewerCommentAuthor,
		},
	}
}

func openIssue(labels ...string) Observation {
	return Observation{Issue: IssueObservation{
		Number:    derivedIssue,
		State:     IssueStateOpen,
		Assignees: []string{derivedAssignee},
		Labels:    labels,
	}}
}

// runObservation は claim 済み Run（branch + Pull Request）の観測骨格を返す。
// lineage は live head を先頭にした新しい順の系譜である。
func runObservation(state PullRequestState, lineage ...string) Observation {
	observation := openIssue(string(StatusInProgress))
	observation.Branch = &BranchObservation{Name: IssueBranchName(derivedIssue), Head: lineage[0]}
	observation.PullRequests = []PullRequestObservation{{
		Number:      700,
		State:       state,
		Head:        lineage[0],
		HeadLineage: lineage,
	}}
	return observation
}

func evidenceCheck(name, head string) CheckRunObservation {
	return CheckRunObservation{Name: name, Head: head, AppID: derivedImplementerApp}
}

func verdictCheck(kind contract.ReviewKind, verdict contract.ReviewVerdict, head string) CheckRunObservation {
	name := CheckRunTestValidity
	if kind == contract.ReviewFinalImplementation {
		name = CheckRunFinalImplementation
	}
	return CheckRunObservation{Name: name, Head: head, AppID: derivedReviewerApp, Verdict: verdict}
}

func dispatchOperation(kind contract.OperationKind) ReconcileAction {
	return ReconcileAction{Kind: ReconcileDispatchOperation, Operation: kind}
}

func requestReviewAction(kind contract.ReviewKind) ReconcileAction {
	return ReconcileAction{Kind: ReconcileRequestReview, Review: kind}
}

func escalation(reason EscalationReason) ReconcileAction {
	return ReconcileAction{Kind: ReconcileEscalateHuman, Reason: reason}
}

func cloneObservation(observation Observation) Observation {
	clone := observation
	clone.Issue.Assignees = append([]string(nil), observation.Issue.Assignees...)
	clone.Issue.Labels = append([]string(nil), observation.Issue.Labels...)
	clone.Issue.LabelEvents = append([]LabelEventObservation(nil), observation.Issue.LabelEvents...)
	clone.Issue.Dependencies = append([]DependencyObservation(nil), observation.Issue.Dependencies...)
	clone.CheckRuns = append([]CheckRunObservation(nil), observation.CheckRuns...)
	clone.Comments = append([]CommentObservation(nil), observation.Comments...)
	clone.PullRequests = append([]PullRequestObservation(nil), observation.PullRequests...)
	for index := range clone.PullRequests {
		clone.PullRequests[index].HeadLineage =
			append([]string(nil), observation.PullRequests[index].HeadLineage...)
	}
	if observation.Branch != nil {
		branch := *observation.Branch
		clone.Branch = &branch
	}
	return clone
}

// AC-1: Derived phases 表の各行に対応する snapshot から、同じ phase と同じ次 action が
// 常に導出される。表の優先順位も、上位行の条件を同時に満たす snapshot で確認する。
func TestDeriveEvaluatesEveryDerivedPhaseRow(t *testing.T) {
	needsHuman := runObservation(PullRequestStateMerged, greenHead, redHead, bootstrapCommit)
	needsHuman.Issue.Labels = []string{string(StatusNeedsHuman)}

	merged := runObservation(PullRequestStateMerged, greenHead, redHead, bootstrapCommit)
	merged.Issue.State = IssueStateClosed
	merged.Issue.Labels = []string{string(StatusMerged)}

	superseded := runObservation(PullRequestStateClosed, redHead, bootstrapCommit)

	merging := runObservation(PullRequestStateReady, greenHead, redHead, bootstrapCommit)
	merging.CheckRuns = []CheckRunObservation{
		verdictCheck(contract.ReviewFinalImplementation, contract.VerdictApprove, greenHead),
	}

	finalizing := runObservation(PullRequestStateDraft, greenHead, redHead, bootstrapCommit)
	finalizing.CheckRuns = []CheckRunObservation{
		verdictCheck(contract.ReviewFinalImplementation, contract.VerdictApprove, greenHead),
	}

	awaitingFinal := runObservation(PullRequestStateDraft, greenHead, redHead, bootstrapCommit)
	awaitingFinal.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceGreen, greenHead),
		evidenceCheck(CheckRunEvidenceChecks, greenHead),
	}

	implementing := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	implementing.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
	}

	awaitingTest := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	awaitingTest.CheckRuns = []CheckRunObservation{evidenceCheck(CheckRunEvidenceRed, redHead)}

	authoring := runObservation(PullRequestStateDraft, bootstrapCommit)

	claimed := openIssue(string(StatusInProgress))
	claimed.Branch = &BranchObservation{Name: IssueBranchName(derivedIssue), Head: bootstrapCommit}

	candidate := openIssue(LabelReady)

	tests := []struct {
		name        string
		observation Observation
		phase       Phase
		next        ReconcileAction
	}{
		{"needs_human label は merged より優先する", needsHuman, PhaseNeedsHuman,
			ReconcileAction{Kind: ReconcileAwaitHuman}},
		{"merged", merged, PhaseMerged, ReconcileAction{Kind: ReconcileRecordCompletion}},
		{"superseded", superseded, PhaseSuperseded, ReconcileAction{Kind: ReconcileNone}},
		{"merging_pull_request", merging, PhaseMergingPullRequest,
			dispatchOperation(contract.OperationMergePullRequest)},
		{"finalizing_pull_request", finalizing, PhaseFinalizingPullRequest,
			dispatchOperation(contract.OperationFinalizePullRequest)},
		{"awaiting_final_review", awaitingFinal, PhaseAwaitingFinalReview,
			requestReviewAction(contract.ReviewFinalImplementation)},
		{"implementing", implementing, PhaseImplementing,
			dispatchOperation(contract.OperationImplement)},
		{"awaiting_test_review", awaitingTest, PhaseAwaitingTestReview,
			requestReviewAction(contract.ReviewTestValidity)},
		{"authoring_tests", authoring, PhaseAuthoringTests,
			dispatchOperation(contract.OperationAuthorTests)},
		{"claimed", claimed, PhaseClaimed, dispatchOperation(contract.OperationClaim)},
		{"candidate", candidate, PhaseCandidate, dispatchOperation(contract.OperationClaim)},
	}

	covered := map[Phase]bool{}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			before := cloneObservation(testCase.observation)
			first := Derive(testCase.observation, derivedConfig())
			second := Derive(testCase.observation, derivedConfig())

			if first.Phase != testCase.phase || first.Next != testCase.next {
				t.Fatalf("derivation = %+v, want phase=%q next=%+v", first, testCase.phase, testCase.next)
			}
			if first != second {
				t.Fatalf("同じ snapshot から違う導出: first=%+v second=%+v", first, second)
			}
			if !reflect.DeepEqual(testCase.observation, before) {
				t.Fatal("Derive が入力 snapshot を変更した")
			}
		})
		covered[testCase.phase] = true
	}

	for _, phase := range DerivedPhases() {
		if !covered[phase] {
			t.Fatalf("Derived phases 表の %q に対応する test が無い", phase)
		}
	}
}

// AC-1: request_changes と needs_human の verdict は表の行そのものではないが、
// workflow.md の test/final review 節が定める自動修正 loop の分岐である。
func TestDeriveRoutesReviewVerdictsToTheRepairLoop(t *testing.T) {
	testRequestChanges := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	testRequestChanges.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictRequestChanges, redHead),
	}

	finalRequestChanges := runObservation(PullRequestStateDraft, greenHead, redHead, bootstrapCommit)
	finalRequestChanges.CheckRuns = []CheckRunObservation{
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
		evidenceCheck(CheckRunEvidenceGreen, greenHead),
		evidenceCheck(CheckRunEvidenceChecks, greenHead),
		verdictCheck(contract.ReviewFinalImplementation, contract.VerdictRequestChanges, greenHead),
	}

	// 差し戻し後に積み直した head。evidence も verdict も新 head には無い。
	revisedHead := "4444444444444444444444444444444444444444"
	afterFinalRequestChanges := runObservation(
		PullRequestStateDraft, revisedHead, greenHead, redHead, bootstrapCommit)
	afterFinalRequestChanges.CheckRuns = finalRequestChanges.CheckRuns

	needsHumanVerdict := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	needsHumanVerdict.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictNeedsHuman, redHead),
	}

	tests := []struct {
		name        string
		observation Observation
		phase       Phase
		next        ReconcileAction
	}{
		{"test request_changes は revise_tests へ戻す", testRequestChanges, PhaseAuthoringTests,
			dispatchOperation(contract.OperationReviseTests)},
		{"final request_changes は repair_implementation へ戻す", finalRequestChanges, PhaseImplementing,
			dispatchOperation(contract.OperationRepairImplementation)},
		{"差し戻し後の新 head も repair lane を維持する", afterFinalRequestChanges, PhaseImplementing,
			dispatchOperation(contract.OperationRepairImplementation)},
		{"needs_human verdict は自動 loop を止める", needsHumanVerdict, PhaseNeedsHuman,
			escalation(EscalationReviewNeedsHuman)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := Derive(testCase.observation, derivedConfig())
			if got.Phase != testCase.phase || got.Next != testCase.next {
				t.Fatalf("derivation = %+v, want phase=%q next=%+v", got, testCase.phase, testCase.next)
			}
		})
	}
}

// AC-2: 表のいずれの行にも安全に一致しない観測は継続へ倒さず、needs_human と
// 明示的な escalation action になる。
func TestDeriveFailsClosedForUnmappedObservations(t *testing.T) {
	corruptBranch := openIssue(string(StatusInProgress))
	corruptBranch.Branch = &BranchObservation{Name: IssueBranchName(derivedIssue)}

	foreignBranch := openIssue(string(StatusInProgress))
	foreignBranch.Branch = &BranchObservation{Name: "kudo/issue-999", Head: bootstrapCommit}

	pullRequestWithoutBranch := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	pullRequestWithoutBranch.Branch = nil

	// ready 化を gate するのは final approve だけである。証跡も verdict も無い head で
	// draft が解除された観測は表のどの行にも一致しない。
	readyWithoutApproval := runObservation(PullRequestStateReady, bootstrapCommit)

	twoOpenRuns := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	twoOpenRuns.PullRequests = append(twoOpenRuns.PullRequests, PullRequestObservation{
		Number: 701, State: PullRequestStateDraft, Head: redHead, HeadLineage: []string{redHead},
	})

	brokenLineage := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	brokenLineage.PullRequests[0].HeadLineage = []string{bootstrapCommit}

	closedIssueWithOpenRun := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	closedIssueWithOpenRun.Issue.State = IssueStateClosed

	unreadableVerdict := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	unreadableVerdict.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		{Name: CheckRunTestValidity, Head: redHead, AppID: derivedReviewerApp, Verdict: "looks-fine"},
	}

	conflictingVerdicts := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	conflictingVerdicts.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictRequestChanges, redHead),
	}

	for name, observation := range map[string]Observation{
		"branch head が解決できない":         corruptBranch,
		"別 Issue の branch を渡された":      foreignBranch,
		"branch が消えた Run":             pullRequestWithoutBranch,
		"final approve の無い ready PR":  readyWithoutApproval,
		"open な Run が複数ある":            twoOpenRuns,
		"live head を含まない系譜":           brokenLineage,
		"Run 進行中に Issue が closed された": closedIssueWithOpenRun,
		"verdict 値が語彙に無い":             unreadableVerdict,
		"同じ head に矛盾する verdict がある":   conflictingVerdicts,
	} {
		t.Run(name, func(t *testing.T) {
			got := Derive(observation, derivedConfig())
			if got.Phase != PhaseNeedsHuman {
				t.Fatalf("phase = %q, want %q", got.Phase, PhaseNeedsHuman)
			}
			if got.Next != escalation(EscalationExternalMutationConflict) {
				t.Fatalf("next = %+v, want 明示的な escalation", got.Next)
			}
		})
	}
}

// AC-2: 導出の入力 identity が壊れている deployment は「候補ではない」と黙って
// 判定せず、外部設定の是正を要求する。
func TestDeriveFailsClosedForIncompleteConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*DeriveConfig){
		"Issue number が無い":       func(config *DeriveConfig) { config.Issue = 0 },
		"assignee が無い":           func(config *DeriveConfig) { config.Assignee = "" },
		"ready label が無い":        func(config *DeriveConfig) { config.ReadyLabel = "" },
		"needs-human label が無い":  func(config *DeriveConfig) { config.NeedsHumanLabel = "" },
		"Implementer App が無い":    func(config *DeriveConfig) { config.Implementer.CheckRunAppID = 0 },
		"Reviewer App が無い":       func(config *DeriveConfig) { config.Reviewer.CheckRunAppID = 0 },
		"Implementer author が無い": func(config *DeriveConfig) { config.Implementer.CommentAuthorID = 0 },
		"Reviewer author が無い":    func(config *DeriveConfig) { config.Reviewer.CommentAuthorID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			config := derivedConfig()
			mutate(&config)
			got := Derive(runObservation(PullRequestStateDraft, redHead, bootstrapCommit), config)
			if got.Phase != PhaseNeedsHuman ||
				got.Next != escalation(EscalationExternalConfigurationRequired) {
				t.Fatalf("derivation = %+v, want 設定不足の escalation", got)
			}
		})
	}
}

// Constraints: verdict は check run name と作成 App identity の両方で検証する。
// Implementer 名義の kudo/test-validity や別 head の verdict は gate を進めない。
func TestDeriveRejectsSpoofedAndUnboundVerdicts(t *testing.T) {
	spoofed := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	spoofed.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		{Name: CheckRunTestValidity, Head: redHead, AppID: derivedImplementerApp,
			Verdict: contract.VerdictApprove},
	}

	unbound := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
	unbound.CheckRuns = []CheckRunObservation{
		evidenceCheck(CheckRunEvidenceRed, redHead),
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove,
			"5555555555555555555555555555555555555555"),
	}

	for name, observation := range map[string]Observation{
		"Implementer 名義の verdict": spoofed,
		"系譜外の head への verdict":    unbound,
	} {
		t.Run(name, func(t *testing.T) {
			got := Derive(observation, derivedConfig())
			if got.Phase != PhaseAwaitingTestReview ||
				got.Next != requestReviewAction(contract.ReviewTestValidity) {
				t.Fatalf("derivation = %+v, want test review 待ちのまま", got)
			}
		})
	}
}

// Constraints: evidence check run も作成 App identity で検証する。Reviewer 名義の
// evidence は Implementer の実行証跡ではないため gate を進めない。
func TestDeriveRejectsEvidenceFromTheWrongActor(t *testing.T) {
	observation := runObservation(PullRequestStateDraft, greenHead, redHead, bootstrapCommit)
	observation.CheckRuns = []CheckRunObservation{
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
		{Name: CheckRunEvidenceGreen, Head: greenHead, AppID: derivedReviewerApp},
		{Name: CheckRunEvidenceChecks, Head: greenHead, AppID: derivedReviewerApp},
	}

	got := Derive(observation, derivedConfig())
	if got.Phase != PhaseImplementing || got.Next != dispatchOperation(contract.OperationImplement) {
		t.Fatalf("derivation = %+v, want implementation の再開", got)
	}
}

// 中断した Operation は escalation ではなく同じ Operation の再 dispatch へ収束する。
// evidence が揃っていない head は review へ進めず、実装 lane をやり直す。
func TestDeriveResumesInterruptedOperationsInsteadOfEscalating(t *testing.T) {
	partialEvidence := runObservation(PullRequestStateDraft, greenHead, redHead, bootstrapCommit)
	partialEvidence.CheckRuns = []CheckRunObservation{
		verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
		evidenceCheck(CheckRunEvidenceGreen, greenHead),
	}

	got := Derive(partialEvidence, derivedConfig())
	if got.Phase != PhaseImplementing || got.Next != dispatchOperation(contract.OperationImplement) {
		t.Fatalf("derivation = %+v, want implementation の再開", got)
	}
}

// routing.md の candidate 条件を満たさない Issue は no-op であり、needs_human へ
// 倒さない。Kudo が所有する観測が一切無いため、進行も停止も起きていない。
func TestDeriveSkipsIssuesOutsideTheControlLoop(t *testing.T) {
	closed := openIssue(LabelReady)
	closed.Issue.State = IssueStateClosed

	unassigned := openIssue(LabelReady)
	unassigned.Issue.Assignees = []string{"someone-else"}

	notRequested := openIssue()

	for name, observation := range map[string]Observation{
		"closed Issue":     closed,
		"対象 assignee でない":  unassigned,
		"ai-ready が付いていない": notRequested,
	} {
		t.Run(name, func(t *testing.T) {
			got := Derive(observation, derivedConfig())
			if got.Phase != PhaseNone || got.Next != (ReconcileAction{Kind: ReconcileSkipNotCandidate}) {
				t.Fatalf("derivation = %+v, want no-op", got)
			}
		})
	}
}

// routing.md: dependency 待ちは failure でも needs_human でもなく、ai-ready を
// 残したまま次の reconcile で再評価する待機である。
func TestDeriveWaitsForIncompleteDependencies(t *testing.T) {
	observation := openIssue(LabelReady)
	observation.Issue.Dependencies = []DependencyObservation{
		{Number: 16, Closed: true},
		{Number: 59, Closed: false},
	}

	got := Derive(observation, derivedConfig())
	if got.Phase != PhaseNone || got.Next != (ReconcileAction{Kind: ReconcileAwaitDependency}) {
		t.Fatalf("derivation = %+v, want dependency 待ち", got)
	}

	observation.Issue.Dependencies[1].Closed = true
	got = Derive(observation, derivedConfig())
	if got.Phase != PhaseCandidate || got.Next != dispatchOperation(contract.OperationClaim) {
		t.Fatalf("dependency 完了後の derivation = %+v", got)
	}
}

// AC-4: phase も attempt counter も保存せず、process を作り直した後の同値 snapshot
// だけから同じ継続が再現される。
func TestDeriveReproducesTheSameContinuationAfterRestart(t *testing.T) {
	snapshot := func() Observation {
		observation := runObservation(PullRequestStateDraft, redHead, bootstrapCommit)
		observation.CheckRuns = []CheckRunObservation{
			evidenceCheck(CheckRunEvidenceRed, redHead),
			verdictCheck(contract.ReviewTestValidity, contract.VerdictApprove, redHead),
		}
		return observation
	}

	beforeRestart := Derive(snapshot(), derivedConfig())

	tracker, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{}, NoJitter{})
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}
	if _, err := tracker.Next("run-700/implement", contract.FailureTimeout); err != nil {
		t.Fatalf("attempt を進める: %v", err)
	}
	// process 再起動を模して attempt tracker と導出結果を捨てる。
	tracker = nil
	_ = tracker

	afterRestart := Derive(snapshot(), derivedConfig())
	if beforeRestart != afterRestart {
		t.Fatalf("restart 前後で導出が変わった: before=%+v after=%+v", beforeRestart, afterRestart)
	}
	if afterRestart.Phase != PhaseImplementing ||
		afterRestart.Next != dispatchOperation(contract.OperationImplement) {
		t.Fatalf("restart 後の継続 = %+v", afterRestart)
	}
}

func TestIssueBranchNameMatchesTheClaimTarget(t *testing.T) {
	if got := IssueBranchName(70); got != "kudo/issue-70" {
		t.Fatalf("branch name = %q", got)
	}
}
