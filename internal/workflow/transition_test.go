package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	testHead     = "89abcdef0123456789abcdef0123456789abcdef"
	finalHead    = "fedcba9876543210fedcba9876543210fedcba98"
	repairedHead = "0123456789abcdef0123456789abcdef01234567"
	mergeCommit  = "abcdef0123456789abcdef0123456789abcdef01"
)

func sampleInput() InputIdentity {
	return InputIdentity{
		ContextManifest: contract.ContextManifestRef{
			Schema: contract.ContextManifestSchemaV1Alpha1,
			Digest: contract.SHA256([]byte("context-manifest")),
		},
		ExecutionPolicy: contract.ExecutionPolicyRef{
			Schema: contract.ExecutionPolicySchemaV1Alpha1,
			Digest: contract.SHA256([]byte("execution-policy")),
		},
	}
}

func sampleClaimContext() contract.ClaimContext {
	return contract.ClaimContext{
		Compiler: contract.IssueCompilerVersionV1Alpha1,
		Observation: contract.IssueObservationRef{
			Schema: contract.IssueObservationSchemaV1Alpha1,
			Digest: contract.SHA256([]byte("issue-observation")),
		},
		BodyDigest: contract.SHA256([]byte("raw issue body")),
		TaskContext: contract.TaskContextRef{
			Schema: contract.TaskContextSchemaV1Alpha1,
			Digest: contract.SHA256([]byte("task-context")),
		},
		ContextManifest: sampleInput().ContextManifest,
		BaseSHA:         "0123456789abcdef0123456789abcdef01234567",
	}
}

func sampleClaimSucceeded() ClaimSucceeded {
	return ClaimSucceeded{
		Context:          sampleClaimContext(),
		ExecutionPolicy:  sampleInput().ExecutionPolicy,
		EscalationPolicy: sampleEscalationPolicyRef(),
		RoundLimits:      sampleRoundLimits(),
	}
}

func sampleObservation(body string) ObservationRecorded {
	bodyDigest := contract.SHA256([]byte(body))
	return ObservationRecorded{
		Observation: contract.IssueObservationRef{
			Schema: contract.IssueObservationSchemaV1Alpha1,
			Digest: contract.SHA256([]byte("observation:" + body)),
		},
		BodyDigest: bodyDigest,
	}
}

func TestClaimSucceededPinsLiveReconstructionCheckpoint(t *testing.T) {
	context := sampleClaimContext()
	decision := requireDecision(t, Run{ID: "run-01"}, ClaimSucceeded{
		Context:          context,
		ExecutionPolicy:  sampleInput().ExecutionPolicy,
		EscalationPolicy: sampleEscalationPolicyRef(),
		RoundLimits:      sampleRoundLimits(),
	})

	if decision.Run.ClaimContext != context {
		t.Fatalf("claim contextがRunへ固定されていない: %+v", decision.Run.ClaimContext)
	}
	if decision.Run.Input.ContextManifest != context.ContextManifest {
		t.Fatal("semantic inputがclaim contextのContext Manifestから導出されていない")
	}
	if decision.Run.Observation != context.Observation || decision.Run.ObservationBodyDigest != context.BodyDigest {
		t.Fatal("Issue Observationのschema/ref/body digestがaudit lineageへ固定されていない")
	}
}

func TestClaimSucceededDelegatesStatusProjectionToControllerAction(t *testing.T) {
	t.Parallel()

	decision := requireDecision(t, Run{ID: "run-01"}, sampleClaimSucceeded())
	if len(decision.Actions) == 0 {
		t.Fatal("claim successにstatus projection actionがない")
	}
	status, ok := decision.Actions[0].(ProjectStatus)
	if !ok || status.Label != StatusInProgress || status.CloseIssue {
		t.Fatalf("first action = %#v", decision.Actions[0])
	}
}

func TestClaimRejectsMissingLiveReconstructionCheckpoint(t *testing.T) {
	claim := sampleClaimSucceeded()
	claim.Context = contract.ClaimContext{}
	requireRejected(t, Run{ID: "run-01"}, claim, TransitionGateUnsatisfied)
}

func samplePullRequest() contract.PullRequestRef {
	return contract.PullRequestRef{Owner: "mrbaron3", Repository: "kudo", Number: 42}
}

// advance は event 列を順に適用し、途中で拒否されたら test を失敗させる。
func advance(t *testing.T, run Run, events ...Event) Run {
	t.Helper()
	for i, event := range events {
		decision, err := Decide(run, event)
		if err != nil {
			t.Fatalf("events[%d] (%s) が phase %q で拒否された: %v", i, event.EventKind(), run.Phase, err)
		}
		run = decision.Run
	}
	return run
}

func claimedRun(t *testing.T) Run {
	t.Helper()
	return advance(t, Run{ID: "run-01"}, sampleClaimSucceeded())
}

// awaitingTestReview は RED 固定と head publish まで進んだ Run を返す。
func awaitingTestReview(t *testing.T) Run {
	t.Helper()
	return advance(t, claimedRun(t),
		OperationStarted{Kind: contract.OperationAuthorTests},
		TestsAuthored{Head: testHead},
		HeadPublished{Head: testHead, PullRequest: samplePullRequest()},
	)
}

// awaitingFinalReview は test approve から final head publish まで進んだ Run を返す。
func awaitingFinalReview(t *testing.T) Run {
	t.Helper()
	return advance(t, awaitingTestReview(t),
		ReviewCompleted{
			Kind:          contract.ReviewTestValidity,
			Verdict:       contract.VerdictApprove,
			Head:          testHead,
			RequestDigest: contract.SHA256([]byte("test-request")),
			ResultDigest:  contract.SHA256([]byte("test-result")),
		},
		ImplementationFixed{Head: finalHead, ChecksPassed: true},
		HeadPublished{Head: finalHead, PullRequest: samplePullRequest()},
	)
}

func actionKinds(actions []Action) []string {
	kinds := make([]string, len(actions))
	for i, action := range actions {
		kinds[i] = string(action.ActionKind())
	}
	return kinds
}

func requireDecision(t *testing.T, run Run, event Event) Decision {
	t.Helper()
	decision, err := Decide(run, event)
	if err != nil {
		t.Fatalf("phase %q の %s が拒否された: %v", run.Phase, event.EventKind(), err)
	}
	return decision
}

func requireRejected(t *testing.T, run Run, event Event, code TransitionCode) error {
	t.Helper()
	_, err := Decide(run, event)
	if err == nil {
		t.Fatalf("phase %q で %s が受理された", run.Phase, event.EventKind())
	}
	if !errors.Is(err, code) {
		t.Fatalf("phase %q の %s が %q へ分類されない: %v", run.Phase, event.EventKind(), code, err)
	}
	return err
}

// AC-1: transition は Issue Contract や artifact bytes を parse せず、決定論的に
// 次 state と action を返す。
func TestNormalFlowReachesHumanHandoff(t *testing.T) {
	run := Run{ID: "run-01"}
	steps := []struct {
		event   Event
		phase   Phase
		actions []string
	}{
		{sampleClaimSucceeded(),
			PhaseClaimed, []string{"project_status", "dispatch_operation"}},
		{OperationStarted{Kind: contract.OperationAuthorTests}, PhaseAuthoringTests, nil},
		{TestsAuthored{Head: testHead}, PhasePublishingTestHead, []string{"dispatch_operation"}},
		{HeadPublished{Head: testHead, PullRequest: samplePullRequest()},
			PhaseAwaitingTestReview, []string{"request_review"}},
		{ReviewCompleted{Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
			Head: testHead, RequestDigest: contract.SHA256([]byte("r1")), ResultDigest: contract.SHA256([]byte("result-r1"))},
			PhaseImplementing, []string{"dispatch_operation"}},
		{ImplementationFixed{Head: finalHead, ChecksPassed: true},
			PhasePublishingFinalHead, []string{"dispatch_operation"}},
		{HeadPublished{Head: finalHead, PullRequest: samplePullRequest()},
			PhaseAwaitingFinalReview, []string{"request_review"}},
		{ReviewCompleted{Kind: contract.ReviewFinalImplementation, Verdict: contract.VerdictApprove,
			Head: finalHead, RequestDigest: contract.SHA256([]byte("r2")), ResultDigest: contract.SHA256([]byte("result-r2"))},
			PhaseFinalizingPullRequest, []string{"dispatch_operation"}},
		{PullRequestFinalized{Head: finalHead}, PhaseMergingPullRequest, []string{"dispatch_operation"}},
		{PullRequestMerged{Head: finalHead, MergeCommit: mergeCommit}, PhaseMerged, []string{"project_status"}},
	}
	for i, step := range steps {
		decision := requireDecision(t, run, step.event)
		if decision.Run.Phase != step.phase {
			t.Fatalf("steps[%d] (%s): phase = %q, want %q", i, step.event.EventKind(), decision.Run.Phase, step.phase)
		}
		if got := actionKinds(decision.Actions); !reflect.DeepEqual(got, step.actions) &&
			!(len(got) == 0 && len(step.actions) == 0) {
			t.Fatalf("steps[%d] (%s): actions = %v, want %v", i, step.event.EventKind(), got, step.actions)
		}
		run = decision.Run
	}
	if !run.Phase.Terminal() {
		t.Fatalf("正常 handoff の終端 %q が terminal でない", run.Phase)
	}
}

// AC-1: Decide は pure である。同じ入力から同じ結果を返し、引数を書き換えない。
func TestDecideIsPureAndDoesNotMutateArguments(t *testing.T) {
	run := awaitingTestReview(t)
	before := run
	event := ReviewCompleted{Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
		Head: testHead, RequestDigest: contract.SHA256([]byte("r1")), ResultDigest: contract.SHA256([]byte("result-r1"))}

	first := requireDecision(t, run, event)
	second := requireDecision(t, run, event)

	if !reflect.DeepEqual(first.Run, second.Run) {
		t.Fatal("同じ入力から異なる次 state が返った")
	}
	if !reflect.DeepEqual(run, before) {
		t.Fatal("Decide が引数の Run を書き換えた")
	}
}

func TestPointerEventsHaveTheSameSemanticsAsValues(t *testing.T) {
	authoring := advance(t, claimedRun(t), OperationStarted{Kind: contract.OperationAuthorTests})
	publishingTest := advance(t, authoring, TestsAuthored{Head: testHead})
	implementing := advance(t, awaitingTestReview(t), ReviewCompleted{
		Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
		Head: testHead, RequestDigest: contract.SHA256([]byte("test-request")),
		ResultDigest: contract.SHA256([]byte("test-result")),
	})
	finalizing := advance(t, awaitingFinalReview(t), ReviewCompleted{
		Kind: contract.ReviewFinalImplementation, Verdict: contract.VerdictApprove,
		Head: finalHead, RequestDigest: contract.SHA256([]byte("final-request")),
		ResultDigest: contract.SHA256([]byte("final-result")),
	})
	merging := advance(t, finalizing, PullRequestFinalized{Head: finalHead})
	changed := sampleInput()
	changed.ContextManifest.Digest = contract.SHA256([]byte("changed-context"))

	for _, tc := range []struct {
		name    string
		run     Run
		value   Event
		pointer Event
	}{
		{"claim", Run{ID: "run-01"},
			sampleClaimSucceeded(), func() *ClaimSucceeded { event := sampleClaimSucceeded(); return &event }()},
		{"operation started", claimedRun(t), OperationStarted{Kind: contract.OperationAuthorTests}, &OperationStarted{Kind: contract.OperationAuthorTests}},
		{"tests authored", authoring, TestsAuthored{Head: testHead}, &TestsAuthored{Head: testHead}},
		{"head published", publishingTest, HeadPublished{Head: testHead, PullRequest: samplePullRequest()}, &HeadPublished{Head: testHead, PullRequest: samplePullRequest()}},
		{"review completed", awaitingTestReview(t),
			ReviewCompleted{
				Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove, Head: testHead,
				RequestDigest: contract.SHA256([]byte("request")), ResultDigest: contract.SHA256([]byte("result")),
			},
			&ReviewCompleted{
				Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove, Head: testHead,
				RequestDigest: contract.SHA256([]byte("request")), ResultDigest: contract.SHA256([]byte("result")),
			}},
		{"implementation fixed", implementing, ImplementationFixed{Head: finalHead, ChecksPassed: true}, &ImplementationFixed{Head: finalHead, ChecksPassed: true}},
		{"pull request finalized", finalizing, PullRequestFinalized{Head: finalHead}, &PullRequestFinalized{Head: finalHead}},
		{"pull request merged", merging, PullRequestMerged{Head: finalHead, MergeCommit: mergeCommit}, &PullRequestMerged{Head: finalHead, MergeCommit: mergeCommit}},
		{"observation recorded", awaitingTestReview(t), sampleObservation("o2"), func() *ObservationRecorded { event := sampleObservation("o2"); return &event }()},
		{"semantic input changed", awaitingTestReview(t), SemanticInputChanged{ChangedFields: []string{"contextManifest"}, Input: changed}, &SemanticInputChanged{ChangedFields: []string{"contextManifest"}, Input: changed}},
		{"attempt failed", awaitingTestReview(t), AttemptFailed{Class: contract.FailureTimeout}, &AttemptFailed{Class: contract.FailureTimeout}},
		{"human escalated", awaitingTestReview(t),
			HumanEscalated{Reason: EscalationContractAuthorityConflict},
			&HumanEscalated{Reason: EscalationContractAuthorityConflict}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := requireDecision(t, tc.run, tc.value)
			got := requireDecision(t, tc.run, tc.pointer)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pointer event の decision = %+v, want %+v", got, want)
			}
		})
	}
}

func TestTypedNilPointerEventIsRejectedWithoutPanic(t *testing.T) {
	var event *ClaimSucceeded
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("typed nil event で panic した: %v", recovered)
		}
	}()

	_, err := Decide(Run{ID: "run-01"}, event)
	if !errors.Is(err, TransitionUnknownEvent) {
		t.Fatalf("typed nil event の error = %v, want %q", err, TransitionUnknownEvent)
	}
}

// 宣言されていない (phase, event) の組は一つ残らず拒否されなければならない。
// 母集団を表と phase/event の語彙から取ることで、phase や event を追加したときに
// 実装と test が同じ組を見落として通り抜けることを防ぐ。
func TestUndeclaredTransitionsAreRejected(t *testing.T) {
	events := map[EventKind]Event{
		KindClaimSucceeded:   sampleClaimSucceeded(),
		KindOperationStarted: OperationStarted{Kind: contract.OperationAuthorTests},
		KindTestsAuthored:    TestsAuthored{Head: testHead},
		KindHeadPublished:    HeadPublished{Head: testHead, PullRequest: samplePullRequest()},
		KindReviewCompleted: ReviewCompleted{
			Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove, Head: testHead,
			RequestDigest: contract.SHA256([]byte("r")), ResultDigest: contract.SHA256([]byte("result-r")),
		},
		KindImplementationFixed:  ImplementationFixed{Head: finalHead, ChecksPassed: true},
		KindTestRevisionRequired: TestRevisionRequired{Head: testHead},
		KindPullRequestFinalized: PullRequestFinalized{Head: finalHead},
		KindPullRequestMerged:    PullRequestMerged{Head: finalHead, MergeCommit: mergeCommit},
		KindObservationRecorded:  sampleObservation("o2"),
		KindSemanticInputChanged: SemanticInputChanged{ChangedFields: []string{"contextManifest"}, Input: sampleInput()},
		KindAttemptFailed:        AttemptFailed{Class: contract.FailureTimeout},
		KindHumanEscalated:       HumanEscalated{Reason: EscalationContractAuthorityConflict},
	}
	for _, kind := range EventKinds() {
		if _, ok := events[kind]; !ok {
			t.Fatalf("event kind %q の代表値が test に無い", kind)
		}
	}

	declared := 0
	for _, phase := range Phases() {
		for _, kind := range EventKinds() {
			run := Run{ID: "run-01", Phase: phase, Input: sampleInput(), RoundLimits: sampleRoundLimits()}
			_, err := Decide(run, events[kind])
			if allowed(phase, kind) {
				declared++
				continue
			}
			if err == nil {
				t.Fatalf("phase %q で未宣言の %q が受理された", phase, kind)
			}
			if !errors.Is(err, TransitionNotAllowed) && !errors.Is(err, TransitionTerminal) &&
				!errors.Is(err, TransitionGateUnsatisfied) {
				t.Fatalf("phase %q の未宣言 %q が分類可能な error にならない: %v", phase, kind, err)
			}
		}
	}
	if declared == 0 {
		t.Fatal("宣言済み transition が一つも無い")
	}
}

// AC-2: test validity approve が無いまま implementation を開始できない。
func TestImplementationRequiresTestValidityApproval(t *testing.T) {
	// approve 前の phase から implement 開始相当の event を送っても進めない
	for _, phase := range []Phase{PhaseClaimed, PhaseAuthoringTests, PhasePublishingTestHead} {
		run := Run{ID: "run-01", Phase: phase, Input: sampleInput(), PublishedHead: testHead}
		requireRejected(t, run, ImplementationFixed{Head: finalHead, ChecksPassed: true}, TransitionNotAllowed)
	}

	// review 待ちでも、承認が現在の published head へ bind されていなければ進めない
	run := awaitingTestReview(t)
	err := requireRejected(t, run, ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictApprove,
		Head:          finalHead,
		RequestDigest: contract.SHA256([]byte("r1")),
		ResultDigest:  contract.SHA256([]byte("result-r1")),
	}, TransitionGateUnsatisfied)
	if !strings.Contains(err.Error(), testHead) {
		t.Fatalf("bind されるべき head が error に現れない: %v", err)
	}

	// final review の verdict を test gate として流用できない
	requireRejected(t, run, ReviewCompleted{
		Kind:          contract.ReviewFinalImplementation,
		Verdict:       contract.VerdictApprove,
		Head:          testHead,
		RequestDigest: contract.SHA256([]byte("r1")),
		ResultDigest:  contract.SHA256([]byte("result-r1")),
	}, TransitionGateUnsatisfied)
}

func TestRepublishRequiresOriginalPullRequest(t *testing.T) {
	otherPullRequest := samplePullRequest()
	otherPullRequest.Number++

	revisedTests := advance(t, awaitingTestReview(t),
		ReviewCompleted{
			Kind: contract.ReviewTestValidity, Verdict: contract.VerdictRequestChanges,
			Head: testHead, RequestDigest: contract.SHA256([]byte("revise-tests")),
			ResultDigest: contract.SHA256([]byte("revise-tests-result")),
		},
		TestsAuthored{Head: repairedHead},
	)
	finalImplementation := advance(t, awaitingTestReview(t),
		ReviewCompleted{
			Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
			Head: testHead, RequestDigest: contract.SHA256([]byte("test-approve")),
			ResultDigest: contract.SHA256([]byte("test-approve-result")),
		},
		ImplementationFixed{Head: finalHead, ChecksPassed: true},
	)

	for _, tc := range []struct {
		name string
		run  Run
		head string
	}{
		{"revised test head", revisedTests, repairedHead},
		{"final implementation head", finalImplementation, finalHead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireRejected(t, tc.run, HeadPublished{
				Head: tc.head, PullRequest: otherPullRequest,
			}, TransitionGateUnsatisfied)
		})
	}
}

func TestRepairImplementationRetainsTheApprovedTestBinding(t *testing.T) {
	run := advance(t, awaitingFinalReview(t), ReviewCompleted{
		Kind: contract.ReviewFinalImplementation, Verdict: contract.VerdictRequestChanges,
		Head: finalHead, RequestDigest: contract.SHA256([]byte("repair")),
		ResultDigest: contract.SHA256([]byte("repair-result")),
	})

	decision := requireDecision(t, run, ImplementationFixed{Head: repairedHead, ChecksPassed: true})
	if decision.Run.Phase != PhasePublishingFinalHead {
		t.Fatalf("repair 後の phase = %q, want %q", decision.Run.Phase, PhasePublishingFinalHead)
	}
}

// AC-3: final approve と required checks の binding が無ければ PR 準備へ進めない。
func TestFinalizeRequiresApprovalAndChecksOnPublishedHead(t *testing.T) {
	approve := func(head string) ReviewCompleted {
		return ReviewCompleted{
			Kind:          contract.ReviewFinalImplementation,
			Verdict:       contract.VerdictApprove,
			Head:          head,
			RequestDigest: contract.SHA256([]byte("r2")),
			ResultDigest:  contract.SHA256([]byte("result-r2")),
		}
	}

	// 別 head への approve は現在の published head を承認しない
	requireRejected(t, awaitingFinalReview(t), approve(testHead), TransitionGateUnsatisfied)

	// required checks が通っていない実装は final review 自体へ進めない
	noChecks := advance(t, awaitingTestReview(t), ReviewCompleted{
		Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
		Head: testHead, RequestDigest: contract.SHA256([]byte("r1")),
		ResultDigest: contract.SHA256([]byte("result-r1")),
	})
	requireRejected(t, noChecks, ImplementationFixed{Head: finalHead, ChecksPassed: false}, TransitionGateUnsatisfied)

	// PR finalize は final approve に bind された head だけを確定できる
	finalizing := advance(t, awaitingFinalReview(t), approve(finalHead))
	requireRejected(t, finalizing, PullRequestFinalized{Head: testHead}, TransitionGateUnsatisfied)

	// merge も同じ head へ束縛する。承認していない head の merge を成功として受理すると、
	// review していない変更が base へ入る。
	merging := advance(t, finalizing, PullRequestFinalized{Head: finalHead})
	requireRejected(t, merging, PullRequestMerged{Head: testHead, MergeCommit: mergeCommit},
		TransitionGateUnsatisfied)
}

// AC-4: transport failure と request_changes を needs_human と混同しない。
func TestTransportFailureAndRequestChangesAreNotNeedsHuman(t *testing.T) {
	for _, class := range []contract.FailureClass{
		contract.FailureTimeout, contract.FailureRateLimit, contract.FailureNetwork,
		contract.FailureProviderCrash, contract.FailureGitHubTransport,
	} {
		run := awaitingTestReview(t)
		decision := requireDecision(t, run, AttemptFailed{Class: class})
		if decision.Run.Phase != run.Phase {
			t.Fatalf("%s で phase が %q へ動いた", class, decision.Run.Phase)
		}
		if got := actionKinds(decision.Actions); !reflect.DeepEqual(got, []string{"schedule_retry"}) {
			t.Fatalf("%s の action = %v, want [schedule_retry]", class, got)
		}
	}

	// retry budget を使い切った失敗だけが人へ上がる
	exhausted := requireDecision(t, awaitingTestReview(t),
		AttemptFailed{Class: contract.FailureTimeout, RetryBudgetExhausted: true})
	if exhausted.Run.Phase != PhaseNeedsHuman {
		t.Fatalf("retry budget 枯渇後の phase = %q, want %q", exhausted.Run.Phase, PhaseNeedsHuman)
	}

	// request_changes は修正 Operation へ routing し、needs_human にしない
	for _, tc := range []struct {
		name string
		run  Run
		kind contract.ReviewKind
		head string
		want Phase
		op   contract.OperationKind
	}{
		{"test", awaitingTestReview(t), contract.ReviewTestValidity, testHead,
			PhaseAuthoringTests, contract.OperationReviseTests},
		{"final", awaitingFinalReview(t), contract.ReviewFinalImplementation, finalHead,
			PhaseImplementing, contract.OperationRepairImplementation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := requireDecision(t, tc.run, ReviewCompleted{
				Kind: tc.kind, Verdict: contract.VerdictRequestChanges,
				Head: tc.head, RequestDigest: contract.SHA256([]byte("rc")),
				ResultDigest: contract.SHA256([]byte("rc-result")),
			})
			if decision.Run.Phase != tc.want {
				t.Fatalf("phase = %q, want %q", decision.Run.Phase, tc.want)
			}
			if decision.Run.Phase == PhaseNeedsHuman {
				t.Fatal("request_changes が needs_human になった")
			}
			dispatch, ok := decision.Actions[0].(DispatchOperation)
			if !ok || dispatch.Kind != tc.op {
				t.Fatalf("action = %+v, want dispatch %q", decision.Actions[0], tc.op)
			}
		})
	}

	// needs_human verdict だけが停止する
	needsHuman := requireDecision(t, awaitingTestReview(t), ReviewCompleted{
		Kind: contract.ReviewTestValidity, Verdict: contract.VerdictNeedsHuman,
		Head: testHead, RequestDigest: contract.SHA256([]byte("nh")),
		ResultDigest: contract.SHA256([]byte("nh-result")),
	})
	if needsHuman.Run.Phase != PhaseNeedsHuman {
		t.Fatalf("needs_human verdict の phase = %q", needsHuman.Run.Phase)
	}
}

// AC-5: Observation だけの変化は audit 更新に留め、semantic input の変化だけを
// 停止・再 claim へ route する。
func TestObservationOnlyChangeDoesNotSupersedeRun(t *testing.T) {
	// approval を保持済みの Run で確認する。approval を持たない phase で試すと
	// 「approval が破棄されていない」という主張が空振りする。
	run := awaitingFinalReview(t)
	if run.TestApproval == nil {
		t.Fatal("前提の Run が test approval を持っていない")
	}
	before := run.Input
	beforeBodyDigest := run.ObservationBodyDigest

	decision := requireDecision(t, run, sampleObservation("observation-2"))
	if decision.Run.Phase != run.Phase {
		t.Fatalf("observation 更新で phase が %q へ動いた", decision.Run.Phase)
	}
	if decision.Run.Input != before {
		t.Fatal("observation 更新で semantic input が変わった")
	}
	if len(decision.Actions) != 0 {
		t.Fatalf("observation 更新で action が発生した: %v", actionKinds(decision.Actions))
	}
	if decision.Run.Observation == run.Observation {
		t.Fatal("observation lineage が更新されていない")
	}
	if decision.Run.ObservationBodyDigest == beforeBodyDigest {
		t.Fatal("observation body digestが更新されていない")
	}
	if decision.Run.TestApproval != run.TestApproval {
		t.Fatal("observation 更新で approval が破棄された")
	}
}

func TestSemanticInputChangeSupersedesRun(t *testing.T) {
	run := awaitingFinalReview(t)
	original := run.Input
	changed := sampleInput()
	changed.ContextManifest.Digest = contract.SHA256([]byte("別 context manifest"))

	decision := requireDecision(t, run, SemanticInputChanged{
		ChangedFields: []string{"contextManifest"},
		Input:         changed,
	})
	if decision.Run.Phase != PhaseSuperseded {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseSuperseded)
	}
	if got := actionKinds(decision.Actions); !reflect.DeepEqual(got, []string{"supersede_run"}) {
		t.Fatalf("action = %v, want [supersede_run]", got)
	}
	supersede, ok := decision.Actions[0].(SupersedeRun)
	if !ok || !reflect.DeepEqual(supersede.ChangedFields, []string{"contextManifest"}) {
		t.Fatalf("supersede action に変更 field が載っていない: %+v", decision.Actions[0])
	}
	if decision.Run.Input != original {
		t.Fatal("supersede された Run の semantic input が上書きされた")
	}
	if supersede.Input != changed {
		t.Fatalf("supersede action に新しい semantic input が載っていない: %+v", supersede)
	}
	// 古い approval を新しい入力へ持ち越さない
	if decision.Run.TestApproval != nil {
		t.Fatal("supersede 後も test approval が残っている")
	}
}

// Run aggregate は parser の出力や本文を保持してはならない。field 型を辿って
// opaque な identity 以外が入り込んでいないことを構造として固定する。
func TestRunHoldsOnlyOpaqueIdentity(t *testing.T) {
	allowed := map[string]bool{
		"contract.IssueRef":            true,
		"contract.IssueObservationRef": true,
		"contract.PullRequestRef":      true,
		"contract.Digest":              true,
		"contract.ContextManifestRef":  true,
		"contract.ExecutionPolicyRef":  true,
		// live再構築用のversion/ref/digest/baseだけを持ち、canonical bytesを持たない。
		"contract.ClaimContext": true,
		// gate 予算の ref と解決済みの上限値。prose でも parse 結果でもない。
		"contract.EscalationPolicyRef": true,
		"contract.ReviewRoundLimits":   true,
	}
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.PkgPath() == "github.com/mrbaron3/kudo/internal/contract" {
			name := "contract." + typ.Name()
			if !allowed[name] {
				t.Fatalf("%s が contract の非 identity 型 %s を保持している", path, name)
			}
			return
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := range typ.NumField() {
			field := typ.Field(i)
			walk(field.Type, path+"."+field.Name)
		}
	}
	walk(reflect.TypeOf(Run{}), "Run")
}

// Decide は store から読み直した任意の Run を受け取る。event を順に流して組み立てた
// Run だけを渡していると、集約の不変条件を守る gate が test から到達不能になり、
// 実装を外しても green のままになる。永続化された Run が壊れている場合を直接作る。
func TestGatesRejectInconsistentStoredRun(t *testing.T) {
	finalApprove := ReviewCompleted{
		Kind:          contract.ReviewFinalImplementation,
		Verdict:       contract.VerdictApprove,
		Head:          finalHead,
		RequestDigest: contract.SHA256([]byte("r2")),
		ResultDigest:  contract.SHA256([]byte("result-r2")),
	}

	for name, tc := range map[string]struct {
		run   Run
		event Event
	}{
		"checks bound to another head": {
			run: Run{ID: "run-01", Phase: PhaseAwaitingFinalReview, Input: sampleInput(),
				RoundLimits: sampleRoundLimits(), PublishedHead: finalHead, ChecksHead: testHead},
			event: finalApprove,
		},
		"checks never bound": {
			run: Run{ID: "run-01", Phase: PhaseAwaitingFinalReview, Input: sampleInput(),
				RoundLimits: sampleRoundLimits(), PublishedHead: finalHead},
			event: finalApprove,
		},
		"finalize without final approval": {
			run: Run{ID: "run-01", Phase: PhaseFinalizingPullRequest, Input: sampleInput(),
				PublishedHead: finalHead, ChecksHead: finalHead},
			event: PullRequestFinalized{Head: finalHead},
		},
		"merge without final approval": {
			run: Run{ID: "run-01", Phase: PhaseMergingPullRequest, Input: sampleInput(),
				PublishedHead: finalHead, ChecksHead: finalHead},
			event: PullRequestMerged{Head: finalHead, MergeCommit: mergeCommit},
		},
		// merge commit 無しの成功は「merge した」と主張できていない。base 側に commit が
		// 生まれていない状態を terminal として確定させない。
		"merge without merge commit": {
			run: Run{ID: "run-01", Phase: PhaseMergingPullRequest, Input: sampleInput(),
				PublishedHead: finalHead, ChecksHead: finalHead,
				FinalApproval: &Approval{Head: finalHead, RequestDigest: contract.SHA256([]byte("final-approve"))}},
			event: PullRequestMerged{Head: finalHead},
		},
		"publish head never fixed": {
			run:   Run{ID: "run-01", Phase: PhasePublishingTestHead, Input: sampleInput()},
			event: HeadPublished{Head: testHead, PullRequest: samplePullRequest()},
		},
		"implementation without test approval": {
			run: Run{ID: "run-01", Phase: PhaseImplementing, Input: sampleInput(),
				FixedHead: testHead, PublishedHead: testHead, PublishedTestHead: testHead},
			event: ImplementationFixed{Head: finalHead, ChecksPassed: true},
		},
		"implementation with approval for another test head": {
			run: Run{ID: "run-01", Phase: PhaseImplementing, Input: sampleInput(),
				FixedHead: testHead, PublishedHead: testHead, PublishedTestHead: testHead,
				TestApproval: &Approval{
					Head: finalHead, RequestDigest: contract.SHA256([]byte("test-approve")),
					ResultDigest: contract.SHA256([]byte("test-approve-result")),
				}},
			event: ImplementationFixed{Head: finalHead, ChecksPassed: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireRejected(t, tc.run, tc.event, TransitionGateUnsatisfied)
		})
	}
}
