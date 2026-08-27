package workflow

import (
	"reflect"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const (
	revisedTestHead = "1111111111111111111111111111111111111111"
	secondFinalHead = "2222222222222222222222222222222222222222"
)

func sampleRoundLimits() contract.ReviewRoundLimits {
	return contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
}

func sampleEscalationPolicyRef() contract.EscalationPolicyRef {
	return contract.EscalationPolicyRef{
		Schema: contract.EscalationPolicySchemaV1Alpha1,
		Digest: contract.SHA256([]byte("escalation-policy")),
	}
}

// reviewedRun は指定 gate で review 待ちの Run を、指定した上限と確定済み round 数で作る。
// store から読み直した Run を直接組み立てるのは、loop を回さないと到達できない
// 中間状態の gate を test から確実に突けるようにするためである。
func reviewedRun(phase Phase, limits contract.ReviewRoundLimits, rounds ReviewRounds) Run {
	run := Run{
		ID:               "run-01",
		Phase:            phase,
		Input:            sampleInput(),
		EscalationPolicy: sampleEscalationPolicyRef(),
		RoundLimits:      limits,
		Rounds:           rounds,
		TotalRounds:      rounds,
		PullRequest:      samplePullRequest(),
	}
	switch phase {
	case PhaseAwaitingTestReview:
		run.FixedHead = testHead
		run.PublishedHead = testHead
		run.PublishedTestHead = testHead
	case PhaseAwaitingFinalReview:
		run.FixedHead = finalHead
		run.PublishedHead = finalHead
		run.PublishedTestHead = testHead
		run.ChecksHead = finalHead
		run.TestApproval = &Approval{
			Head:          testHead,
			RequestDigest: contract.SHA256([]byte("test-approve-request")),
			ResultDigest:  contract.SHA256([]byte("test-approve-result")),
		}
	}
	return run
}

func requestChanges(kind contract.ReviewKind, head string) ReviewCompleted {
	return ReviewCompleted{
		Kind:          kind,
		Verdict:       contract.VerdictRequestChanges,
		Head:          head,
		RequestDigest: contract.SHA256([]byte("request-changes-" + head)),
		ResultDigest:  contract.SHA256([]byte("request-changes-result-" + head)),
	}
}

func requireEscalation(t *testing.T, decision Decision, reason EscalationReason, stoppedAt Phase, stretch ReviewRounds) {
	t.Helper()
	if decision.Run.Phase != PhaseNeedsHuman {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseNeedsHuman)
	}
	if got := actionKinds(decision.Actions); !reflect.DeepEqual(got, []string{"project_status", "escalate_human"}) {
		t.Fatalf("action = %v, want [project_status escalate_human]", got)
	}
	escalate, ok := decision.Actions[1].(EscalateHuman)
	if !ok {
		t.Fatalf("action[1] = %+v, want EscalateHuman", decision.Actions[1])
	}
	if escalate.Reason != reason {
		t.Fatalf("reason = %q, want %q", escalate.Reason, reason)
	}
	// 停止 phase は Decision の Run（既に needs_human）から復元できないため action が運ぶ。
	if escalate.StoppedAt != stoppedAt {
		t.Fatalf("stoppedAt = %q, want %q", escalate.StoppedAt, stoppedAt)
	}
	// escalate は無人区間 counter を 0 へ戻すため、今回の round 数も Decision の Run から
	// 復元できない。ledger が「今回何 round 回したか」を書けるよう action が運ぶ。
	if escalate.Rounds != stretch {
		t.Fatalf("escalation の round 数 = %+v, want %+v", escalate.Rounds, stretch)
	}
	// 予算は無人区間ごとに与える。人間が次に見るまでの round 数を縛るのであって、
	// Run の生涯合計を縛るのではない。reset しないと、差し戻し後の review が予算 0 になる。
	if decision.Run.Rounds != (ReviewRounds{}) {
		t.Fatalf("escalation 後の無人区間 counter = %+v, want zero", decision.Run.Rounds)
	}
}

// implementingRun は test approve 済みで実装中の Run を、指定した上限と round 数で作る。
func implementingRun(limits contract.ReviewRoundLimits, rounds ReviewRounds) Run {
	return Run{
		ID:                "run-01",
		Phase:             PhaseImplementing,
		Input:             sampleInput(),
		EscalationPolicy:  sampleEscalationPolicyRef(),
		RoundLimits:       limits,
		Rounds:            rounds,
		TotalRounds:       rounds,
		PullRequest:       samplePullRequest(),
		PublishedHead:     testHead,
		PublishedTestHead: testHead,
		TestApproval:      &Approval{Head: testHead, RequestDigest: contract.SHA256([]byte("test-approve"))},
	}
}

// implement 発の test 差し戻しも test_validity の round 予算を消費する。
// 消費しないと implement→revise→approve→implement の往復がどの予算にも数えられず、
// 無人区間が有限にならない（ADR-0003 2026-08-21 追記）。
func TestTestRevisionRequiredConsumesTestValidityRounds(t *testing.T) {
	run := implementingRun(sampleRoundLimits(), ReviewRounds{TestValidity: 1})

	decision := requireDecision(t, run, TestRevisionRequired{Head: testHead})
	if decision.Run.Phase != PhaseAuthoringTests {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseAuthoringTests)
	}
	dispatch, ok := decision.Actions[0].(DispatchOperation)
	if !ok || dispatch.Kind != contract.OperationReviseTests {
		t.Fatalf("action = %+v, want dispatch %q", decision.Actions[0], contract.OperationReviseTests)
	}
	if decision.Run.Rounds.TestValidity != 2 || decision.Run.TotalRounds.TestValidity != 2 {
		t.Fatalf("rounds = %d / %d, want 2 / 2",
			decision.Run.Rounds.TestValidity, decision.Run.TotalRounds.TestValidity)
	}
	// 差し戻した時点で以前の approval を前提にしない。新しい approve を得るまで
	// implementation へ戻れないことを、gate ではなく state で表す。
	if decision.Run.TestApproval != nil {
		t.Fatal("差し戻し後も以前の test approval が残っている")
	}
}

// 上限に達した差し戻しは revise_tests を発行せず人へ渡す。reviewer ではなく
// Controller の予算切れなので reason は review_round_limit_exceeded になる。
func TestTestRevisionRequiredEscalatesAtRoundLimit(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	run := implementingRun(limits, ReviewRounds{TestValidity: 2})

	decision := requireDecision(t, run, TestRevisionRequired{Head: testHead})
	requireEscalation(t, decision, EscalationReviewRoundLimitExceeded, PhaseImplementing,
		ReviewRounds{TestValidity: 3})
}

// rollback 先は最後に承認された test checkpoint でなければならない。別 head の差し戻しを
// 受理すると、承認していない test 状態から revise_tests が始まる。
func TestTestRevisionRequiredBindsToApprovedTestHead(t *testing.T) {
	run := implementingRun(sampleRoundLimits(), ReviewRounds{TestValidity: 1})
	requireRejected(t, run, TestRevisionRequired{Head: revisedTestHead}, TransitionGateUnsatisfied)

	noApproval := run
	noApproval.TestApproval = nil
	requireRejected(t, noApproval, TestRevisionRequired{Head: testHead}, TransitionGateUnsatisfied)
}

// round 上限は quality verdict ではなく Controller の gate 判断である。
// reviewer は同じ request_changes を返し続け、Controller だけが loop を打ち切る。
func TestReviewRoundLimitStopsTheAutomaticRepairLoop(t *testing.T) {
	for name, tc := range map[string]struct {
		phase  Phase
		kind   contract.ReviewKind
		head   string
		limits contract.ReviewRoundLimits
		rounds func(int) ReviewRounds
		next   Phase
		op     contract.OperationKind
	}{
		"test validity": {
			phase: PhaseAwaitingTestReview, kind: contract.ReviewTestValidity, head: testHead,
			limits: contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3},
			rounds: func(n int) ReviewRounds { return ReviewRounds{TestValidity: n} },
			next:   PhaseAuthoringTests, op: contract.OperationReviseTests,
		},
		"final implementation": {
			phase: PhaseAwaitingFinalReview, kind: contract.ReviewFinalImplementation, head: finalHead,
			limits: contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3},
			rounds: func(n int) ReviewRounds { return ReviewRounds{FinalImplementation: n} },
			next:   PhaseImplementing, op: contract.OperationRepairImplementation,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// 上限未満の round は従来どおり修正 Operation へ routing する。
			for confirmed := 0; confirmed < 2; confirmed++ {
				run := reviewedRun(tc.phase, tc.limits, tc.rounds(confirmed))
				decision := requireDecision(t, run, requestChanges(tc.kind, tc.head))
				if decision.Run.Phase != tc.next {
					t.Fatalf("round %d の phase = %q, want %q", confirmed+1, decision.Run.Phase, tc.next)
				}
				dispatch, ok := decision.Actions[0].(DispatchOperation)
				if !ok || dispatch.Kind != tc.op {
					t.Fatalf("round %d の action = %+v, want dispatch %q", confirmed+1, decision.Actions[0], tc.op)
				}
			}

			// 上限に達した round の request_changes は修正 Operation を発行せず人へ渡す。
			run := reviewedRun(tc.phase, tc.limits, tc.rounds(2))
			decision := requireDecision(t, run, requestChanges(tc.kind, tc.head))
			requireEscalation(t, decision, EscalationReviewRoundLimitExceeded, tc.phase, tc.rounds(3))
		})
	}
}

// counter は gate ごとに独立していなければならない。通算にすると、test gate が
// 荒れただけで final gate の予算を失う。
func TestReviewRoundsAreCountedPerGate(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 2, FinalImplementation: 2}

	// test gate を上限まで使った Run でも、final gate の予算は残っている。
	run := reviewedRun(PhaseAwaitingFinalReview, limits, ReviewRounds{TestValidity: 2})
	decision := requireDecision(t, run, requestChanges(contract.ReviewFinalImplementation, finalHead))
	if decision.Run.Phase != PhaseImplementing {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseImplementing)
	}
	if decision.Run.Rounds.TestValidity != 2 || decision.Run.Rounds.FinalImplementation != 1 {
		t.Fatalf("rounds = %+v, want {TestValidity:2 FinalImplementation:1}", decision.Run.Rounds)
	}
	if decision.Run.TotalRounds != decision.Run.Rounds {
		t.Fatalf("escalation 前は生涯 counter と一致する: %+v / %+v",
			decision.Run.TotalRounds, decision.Run.Rounds)
	}

	// 逆向きも同じ。final gate の消費は test gate の判定に影響しない。
	run = reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{FinalImplementation: 2})
	decision = requireDecision(t, run, requestChanges(contract.ReviewTestValidity, testHead))
	if decision.Run.Phase != PhaseAuthoringTests {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseAuthoringTests)
	}
}

// counter は同じ gate へ再入しても reset しない。reset すると loop が上限を迂回する。
func TestReviewRoundsAccumulateAcrossTheRepairLoop(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	run := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{})

	run = advance(t, run,
		requestChanges(contract.ReviewTestValidity, testHead),
		TestsAuthored{Head: revisedTestHead},
		HeadPublished{Head: revisedTestHead, PullRequest: samplePullRequest()},
	)
	if run.Rounds.TestValidity != 1 {
		t.Fatalf("1 round 後の counter = %d, want 1", run.Rounds.TestValidity)
	}

	run = advance(t, run,
		requestChanges(contract.ReviewTestValidity, revisedTestHead),
		TestsAuthored{Head: testHead},
		HeadPublished{Head: testHead, PullRequest: samplePullRequest()},
	)
	if run.Rounds.TestValidity != 2 || run.TotalRounds.TestValidity != 2 {
		t.Fatalf("2 round 後の counter = %d / %d, want 2", run.Rounds.TestValidity, run.TotalRounds.TestValidity)
	}

	decision := requireDecision(t, run, requestChanges(contract.ReviewTestValidity, testHead))
	requireEscalation(t, decision, EscalationReviewRoundLimitExceeded, PhaseAwaitingTestReview,
		ReviewRounds{TestValidity: 3})
	if decision.Run.TotalRounds.TestValidity != 3 {
		t.Fatalf("生涯 counter = %d, want 3", decision.Run.TotalRounds.TestValidity)
	}
}

// 上限は request_changes だけを止める。上限ちょうどの round で approve が出たら次の gate へ進む。
func TestRoundLimitDoesNotBlockApproval(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 2, FinalImplementation: 2}
	run := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{TestValidity: 1})
	decision := requireDecision(t, run, ReviewCompleted{
		Kind: contract.ReviewTestValidity, Verdict: contract.VerdictApprove,
		Head: testHead, RequestDigest: contract.SHA256([]byte("approve")),
		ResultDigest: contract.SHA256([]byte("approve-result")),
	})
	if decision.Run.Phase != PhaseImplementing {
		t.Fatalf("phase = %q, want %q", decision.Run.Phase, PhaseImplementing)
	}
	if decision.Run.Rounds.TestValidity != 2 {
		t.Fatalf("approve も round を数える: got %d, want 2", decision.Run.Rounds.TestValidity)
	}
}

// round を消費するのは quality verdict が確定したときだけである。実行環境の不調が
// round を食うと、transport failure が人間への差し戻しに化ける。
func TestNonVerdictEventsDoNotConsumeReviewRounds(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 2, FinalImplementation: 2}
	base := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{TestValidity: 1})

	for name, event := range map[string]Event{
		"attempt failure":  AttemptFailed{Class: contract.FailureTimeout},
		"observation only": sampleObservation("o2"),
	} {
		t.Run(name, func(t *testing.T) {
			decision := requireDecision(t, base, event)
			if decision.Run.Rounds != base.Rounds {
				t.Fatalf("rounds = %+v, want %+v", decision.Run.Rounds, base.Rounds)
			}
		})
	}
}

// semantic input が変われば Run ごと打ち切られ、新しい Run が counter 0 から始まる。
// 停止した Run 自身の counter は監査 lineage として保持する。
func TestSupersedeStartsANewRunWithFreshCounters(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	run := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{TestValidity: 2})

	superseded := requireDecision(t, run, SemanticInputChanged{
		ChangedFields: []string{"contextManifest"},
		Input:         sampleInput(),
	})
	if superseded.Run.Phase != PhaseSuperseded {
		t.Fatalf("phase = %q, want %q", superseded.Run.Phase, PhaseSuperseded)
	}
	if superseded.Run.TotalRounds.TestValidity != 2 {
		t.Fatal("superseded Run の生涯 counter が監査 lineage として残っていない")
	}

	claim := sampleClaimSucceeded()
	claim.RoundLimits = limits
	fresh := requireDecision(t, Run{ID: "run-02"}, claim)
	if fresh.Run.Rounds != (ReviewRounds{}) || fresh.Run.TotalRounds != (ReviewRounds{}) {
		t.Fatalf("新しい Run の counter = %+v / %+v, want zero", fresh.Run.Rounds, fresh.Run.TotalRounds)
	}
}

// 上限は無人区間ごとの予算である。人間へ差し戻した時点で区間が終わるため、
// 停止した Run は次の区間へ向けて満額の予算を持つ。reset しないと、人間が直した後の
// review が予算 0 になり、1 round で収束する修正にも automation が追従できない。
//
// 生涯 counter は reset しない。差し戻しを繰り返す Run を人間が識別できるようにする。
func TestEscalationResetsTheStretchBudgetButKeepsLineage(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	paused := requireDecision(t,
		reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{TestValidity: 2}),
		requestChanges(contract.ReviewTestValidity, testHead)).Run

	if paused.Rounds != (ReviewRounds{}) {
		t.Fatalf("停止時の無人区間 counter = %+v, want zero", paused.Rounds)
	}
	if paused.TotalRounds.TestValidity != 3 {
		t.Fatalf("停止時の生涯 counter = %d, want 3", paused.TotalRounds.TestValidity)
	}
	if paused.RoundLimits != limits {
		t.Fatalf("停止時の上限 = %+v, want %+v", paused.RoundLimits, limits)
	}

	// 停止中の audit 更新は counter を触らない。
	resumed := requireDecision(t, paused, sampleObservation("o2"))
	if resumed.Run.Rounds != (ReviewRounds{}) || resumed.Run.TotalRounds != paused.TotalRounds {
		t.Fatalf("observation で counter が動いた: %+v / %+v", resumed.Run.Rounds, resumed.Run.TotalRounds)
	}
}

// escalation 理由によらず無人区間は終わる。round 上限以外で停止した Run も、
// 次の区間では満額の予算から始まる。
func TestEveryEscalationEndsTheUnattendedStretch(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	run := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{TestValidity: 1})

	for name, tc := range map[string]struct {
		event   Event
		reason  EscalationReason
		stretch ReviewRounds
	}{
		"needs_human verdict": {
			ReviewCompleted{Kind: contract.ReviewTestValidity, Verdict: contract.VerdictNeedsHuman,
				Head: testHead, RequestDigest: contract.SHA256([]byte("nh")),
				ResultDigest: contract.SHA256([]byte("nh-result"))},
			EscalationReviewNeedsHuman, ReviewRounds{TestValidity: 2},
		},
		"retry budget": {
			AttemptFailed{Class: contract.FailureTimeout, RetryBudgetExhausted: true},
			EscalationRetryBudgetExhausted, ReviewRounds{TestValidity: 1},
		},
		"explicit escalation": {
			HumanEscalated{Reason: EscalationContractAuthorityConflict},
			EscalationContractAuthorityConflict, ReviewRounds{TestValidity: 1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireEscalation(t, requireDecision(t, run, tc.event), tc.reason,
				PhaseAwaitingTestReview, tc.stretch)
		})
	}
}

// 範囲外の上限を持つ Run を作らせない。claim で弾かないと、上限 0 の Run が
// 最初の request_changes で即 escalate する状態を後段まで持ち越す。
func TestClaimRejectsOutOfRangeRoundLimits(t *testing.T) {
	for name, limits := range map[string]contract.ReviewRoundLimits{
		"未設定":      {},
		"test 0":   {TestValidity: 0, FinalImplementation: 3},
		"final 0":  {TestValidity: 3, FinalImplementation: 0},
		"test 負":   {TestValidity: -1, FinalImplementation: 3},
		"test 超過":  {TestValidity: contract.MaxReviewRounds + 1, FinalImplementation: 3},
		"final 超過": {TestValidity: 3, FinalImplementation: contract.MaxReviewRounds + 1},
	} {
		t.Run(name, func(t *testing.T) {
			claim := sampleClaimSucceeded()
			claim.RoundLimits = limits
			requireRejected(t, Run{ID: "run-01"}, claim, TransitionGateUnsatisfied)
		})
	}
}

// escalation の理由は機械可読な code で分類する。Controller は文字列一致で分岐しない。
func TestEscalationReasonsAreClassified(t *testing.T) {
	limits := contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}
	run := reviewedRun(PhaseAwaitingTestReview, limits, ReviewRounds{})

	requireEscalation(t, requireDecision(t, run, ReviewCompleted{
		Kind: contract.ReviewTestValidity, Verdict: contract.VerdictNeedsHuman,
		Head: testHead, RequestDigest: contract.SHA256([]byte("nh")),
		ResultDigest: contract.SHA256([]byte("nh-result")),
	}), EscalationReviewNeedsHuman, PhaseAwaitingTestReview, ReviewRounds{TestValidity: 1})

	requireEscalation(t, requireDecision(t, run,
		AttemptFailed{Class: contract.FailureTimeout, RetryBudgetExhausted: true}),
		EscalationRetryBudgetExhausted, PhaseAwaitingTestReview, ReviewRounds{})

	requireEscalation(t, requireDecision(t, run,
		HumanEscalated{Reason: EscalationContractAuthorityConflict}),
		EscalationContractAuthorityConflict, PhaseAwaitingTestReview, ReviewRounds{})
}

// state machine が自ら導出する reason は、外部からの明示 escalation では指定できない。
// 指定できると、上限に達していない Run を「上限到達」として停止させられ、
// reason code と counter lineage が食い違う。
func TestHumanEscalationCannotForgeDerivedReasons(t *testing.T) {
	run := reviewedRun(PhaseAwaitingTestReview,
		contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3}, ReviewRounds{})

	for _, reason := range []EscalationReason{
		EscalationReviewRoundLimitExceeded,
		EscalationReviewNeedsHuman,
		EscalationRetryBudgetExhausted,
		EscalationProtocolValidationFailed,
	} {
		t.Run(string(reason), func(t *testing.T) {
			requireRejected(t, run, HumanEscalated{Reason: reason}, TransitionGateUnsatisfied)
		})
	}

	requireRejected(t, run, HumanEscalated{Reason: "made_up_reason"}, TransitionGateUnsatisfied)
	requireRejected(t, run, HumanEscalated{}, TransitionGateUnsatisfied)
}
