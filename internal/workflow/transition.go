package workflow

import (
	"reflect"

	"github.com/mrbaron3/kudo/internal/contract"
)

// PhaseNew は Run がまだ存在しないことを表す zero value である。
// durable phase ではないため Phases() には含めない。
const PhaseNew Phase = ""

// Decision は transition の結果である。Run は次 state、Actions は Controller が
// 同じ transaction で記録すべき副作用の意図である。
type Decision struct {
	Run     Run
	Actions []Action
}

// transition は 1 つの (phase, event) 組に対する決定である。
type transition func(Run, Event) (Decision, error)

// transitions は phase 固有の遷移を data として持つ。
//
// switch の入れ子で書かないのは、宣言していない組が暗黙の default へ落ちるのを
// 避けるためである。表にすることで「宣言済み以外はすべて拒否」という性質を
// phase × event の全組で機械的に検査できる。
var transitions = map[transitionKey]transition{
	{PhaseNew, KindClaimSucceeded}:                         onClaimSucceeded,
	{PhaseClaimed, KindOperationStarted}:                   onAuthoringStarted,
	{PhaseAuthoringTests, KindTestsAuthored}:               onTestsAuthored,
	{PhasePublishingTestHead, KindHeadPublished}:           onTestHeadPublished,
	{PhaseAwaitingTestReview, KindReviewCompleted}:         onTestReviewCompleted,
	{PhaseImplementing, KindImplementationFixed}:           onImplementationFixed,
	{PhaseImplementing, KindTestRevisionRequired}:          onTestRevisionRequired,
	{PhasePublishingFinalHead, KindHeadPublished}:          onFinalHeadPublished,
	{PhaseAwaitingFinalReview, KindReviewCompleted}:        onFinalReviewCompleted,
	{PhaseFinalizingPullRequest, KindPullRequestFinalized}: onPullRequestFinalized,
	{PhaseMergingPullRequest, KindPullRequestMerged}:       onPullRequestMerged,
}

type transitionKey struct {
	phase Phase
	event EventKind
}

// universalTransitions は phase 固有ではなく Run の生死で可否が決まる event である。
// paused でも受けるかを個別に持つのは、停止中でも audit lineage は伸び、入力変更は
// supersede しうる一方、attempt failure と escalation は実行中にしか起きないためである。
var universalTransitions = map[EventKind]struct {
	whenPaused bool
	apply      transition
}{
	KindObservationRecorded:  {whenPaused: true, apply: onObservationRecorded},
	KindSemanticInputChanged: {whenPaused: true, apply: onSemanticInputChanged},
	KindAttemptFailed:        {apply: onAttemptFailed},
	KindHumanEscalated:       {apply: onHumanEscalated},
}

// Decide は現在の Run と分類済み event から、次 state と必要 action を返す。
//
// pure である。network、clock、filesystem、parser を呼ばず、引数を書き換えない。
// 宣言されていない組、gate を満たさない組、終端 Run への event はすべて error にする。
func Decide(run Run, event Event) (Decision, error) {
	if event == nil {
		return Decision{}, transitionErr(TransitionUnknownEvent, run.Phase, "", "event が nil")
	}
	normalized, kind, ok := normalizeEvent(event)
	if !ok {
		return Decision{}, transitionErr(TransitionUnknownEvent, run.Phase, kind,
			"event の具象型が語彙に無い、または typed nil である")
	}
	if !knownEventKind(kind) {
		return Decision{}, transitionErr(TransitionUnknownEvent, run.Phase, kind, "event kind が語彙に無い")
	}
	if !knownPhase(run.Phase) {
		return Decision{}, transitionErr(TransitionUnknownPhase, run.Phase, kind, "phase が語彙に無い")
	}
	if run.Phase.Terminal() {
		return Decision{}, transitionErr(TransitionTerminal, run.Phase, kind, "終端 Run は event を受け付けない")
	}

	if universal, ok := universalTransitions[kind]; ok {
		if run.Phase.Paused() && !universal.whenPaused {
			return Decision{}, transitionErr(TransitionNotAllowed, run.Phase, kind,
				"停止中の Run はこの event を受け付けない")
		}
		return universal.apply(run, normalized)
	}
	apply, ok := transitions[transitionKey{run.Phase, kind}]
	if !ok {
		return Decision{}, transitionErr(TransitionNotAllowed, run.Phase, kind, "この phase では宣言されていない")
	}
	return apply(run, normalized)
}

// normalizeEvent は公開 event の値・ポインタ表現を値へ揃える。EventKind が値 receiver
// なのでポインタも Event を満たすが、handler は閉じた値型だけを受け取る。
// typed nil と EventKind だけを偽装した別型は dispatch 前に拒否する。
func normalizeEvent(event Event) (Event, EventKind, bool) {
	value := reflect.ValueOf(event)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, "", false
		}
		if dereferenced, ok := value.Elem().Interface().(Event); ok {
			event = dereferenced
		}
	}

	switch event.(type) {
	case ClaimSucceeded,
		OperationStarted,
		TestsAuthored,
		HeadPublished,
		ReviewCompleted,
		ImplementationFixed,
		TestRevisionRequired,
		PullRequestFinalized,
		PullRequestMerged,
		ObservationRecorded,
		SemanticInputChanged,
		AttemptFailed,
		HumanEscalated:
		return event, event.EventKind(), true
	default:
		return nil, "", false
	}
}

// allowed は (phase, event) が宣言されているかを返す。Decide と同じ判定順を使う。
func allowed(phase Phase, kind EventKind) bool {
	if !knownPhase(phase) || !knownEventKind(kind) || phase.Terminal() {
		return false
	}
	if universal, ok := universalTransitions[kind]; ok {
		return !phase.Paused() || universal.whenPaused
	}
	_, ok := transitions[transitionKey{phase, kind}]
	return ok
}

func knownPhase(phase Phase) bool {
	if phase == PhaseNew {
		return true
	}
	for _, known := range phases {
		if phase == known {
			return true
		}
	}
	return false
}

func knownEventKind(kind EventKind) bool {
	for _, known := range eventKinds {
		if kind == known {
			return true
		}
	}
	return false
}

func decide(run Run, actions ...Action) (Decision, error) {
	return Decision{Run: run, Actions: actions}, nil
}

func gate(run Run, kind EventKind, format string, args ...any) (Decision, error) {
	return Decision{}, transitionErr(TransitionGateUnsatisfied, run.Phase, kind, format, args...)
}

func onClaimSucceeded(run Run, event Event) (Decision, error) {
	claimed := event.(ClaimSucceeded)
	if err := contract.ValidateClaimContext(claimed.Context); err != nil {
		return gate(run, claimed.EventKind(), "live再構築checkpointが不正: %v", err)
	}
	// 範囲外の上限を持つ Run を作らせない。claim で弾かないと、gate 予算が壊れた Run が
	// 最初の request_changes で即 escalate する状態を後段まで持ち越す。
	if err := validateRoundLimits(run, claimed); err != nil {
		return Decision{}, err
	}
	run.Phase = PhaseClaimed
	run.Input = InputIdentity{
		ContextManifest: claimed.Context.ContextManifest,
		ExecutionPolicy: claimed.ExecutionPolicy,
	}
	run.ClaimContext = claimed.Context
	run.Observation = claimed.Context.Observation
	run.ObservationBodyDigest = claimed.Context.BodyDigest
	run.EscalationPolicy = claimed.EscalationPolicy
	run.RoundLimits = claimed.RoundLimits
	return decide(run,
		ProjectStatus{Label: StatusInProgress},
		DispatchOperation{Kind: contract.OperationAuthorTests},
	)
}

func validateRoundLimits(run Run, claimed ClaimSucceeded) error {
	limits := []struct {
		name  string
		value int
	}{
		{"testValidity", claimed.RoundLimits.TestValidity},
		{"finalImplementation", claimed.RoundLimits.FinalImplementation},
	}
	for _, limit := range limits {
		if limit.value < contract.MinReviewRounds || limit.value > contract.MaxReviewRounds {
			_, err := gate(run, claimed.EventKind(),
				"review round 上限 %s は %d 以上 %d 以下でなければならない: %d",
				limit.name, contract.MinReviewRounds, contract.MaxReviewRounds, limit.value)
			return err
		}
	}
	return nil
}

// onAuthoringStarted は claim 直後の 1 辺だけを進める。他の kind の実行開始で
// phase が動くと、dispatch した Operation と phase の対応が崩れる。
func onAuthoringStarted(run Run, event Event) (Decision, error) {
	started := event.(OperationStarted)
	if started.Kind != contract.OperationAuthorTests {
		return gate(run, started.EventKind(), "claim 後に開始できるのは %q だけである: %q",
			contract.OperationAuthorTests, started.Kind)
	}
	run.Phase = PhaseAuthoringTests
	return decide(run)
}

func onTestsAuthored(run Run, event Event) (Decision, error) {
	authored := event.(TestsAuthored)
	if authored.Head == "" {
		return gate(run, authored.EventKind(), "RED を固定した head が空である")
	}
	run.Phase = PhasePublishingTestHead
	run.FixedHead = authored.Head
	return decide(run, DispatchOperation{Kind: contract.OperationPublishHead})
}

func onTestHeadPublished(run Run, event Event) (Decision, error) {
	decision, err := onHeadPublished(run, event, PhaseAwaitingTestReview, contract.ReviewTestValidity)
	if err != nil {
		return Decision{}, err
	}
	decision.Run.PublishedTestHead = decision.Run.PublishedHead
	return decision, nil
}

func onFinalHeadPublished(run Run, event Event) (Decision, error) {
	return onHeadPublished(run, event, PhaseAwaitingFinalReview, contract.ReviewFinalImplementation)
}

// onHeadPublished は固定済み head だけが publish されたことを確認する。
// 固定していない head を publish 済みとして受理すると、review が evidence の無い
// head へ繋留される。
func onHeadPublished(run Run, event Event, next Phase, review contract.ReviewKind) (Decision, error) {
	published := event.(HeadPublished)
	if published.Head != run.FixedHead {
		return gate(run, published.EventKind(), "固定済み head と一致しない: got %s, want %s",
			published.Head, run.FixedHead)
	}
	if run.PullRequest != (contract.PullRequestRef{}) &&
		run.PullRequest.String() != published.PullRequest.String() {
		return gate(run, published.EventKind(), "最初に bind した Pull Request と一致しない: got %s, want %s",
			published.PullRequest.String(), run.PullRequest.String())
	}
	run.Phase = next
	run.PublishedHead = published.Head
	run.PullRequest = published.PullRequest
	return decide(run, RequestReview{Kind: review, Head: published.Head})
}

func onTestReviewCompleted(run Run, event Event) (Decision, error) {
	completed := event.(ReviewCompleted)
	if err := checkReviewBinding(run, completed, contract.ReviewTestValidity); err != nil {
		return Decision{}, err
	}
	// verdict を読む前に round を数える。verdict が確定した round だけを数えることで、
	// attempt failure や stale input が予算を消費しない。
	run.Rounds.TestValidity++
	run.TotalRounds.TestValidity++
	switch completed.Verdict {
	case contract.VerdictApprove:
		run.Phase = PhaseImplementing
		run.TestApproval = &Approval{
			Head:          completed.Head,
			RequestDigest: completed.RequestDigest,
			ResultDigest:  completed.ResultDigest,
		}
		return decide(run, DispatchOperation{Kind: contract.OperationImplement})
	case contract.VerdictRequestChanges:
		if roundLimitReached(run.Rounds.TestValidity, run.RoundLimits.TestValidity) {
			return escalate(run, EscalationReviewRoundLimitExceeded)
		}
		// 同じ論理 lane へ差し戻す。fresh session は Worker 側の規律であり、
		// phase としては test 作成へ戻ることだけを表す。
		run.Phase = PhaseAuthoringTests
		run.FixedHead = ""
		return decide(run, DispatchOperation{Kind: contract.OperationReviseTests})
	case contract.VerdictNeedsHuman:
		return escalate(run, EscalationReviewNeedsHuman)
	default:
		return gate(run, completed.EventKind(), "review verdict が不正: %q", completed.Verdict)
	}
}

// roundLimitReached は上限に達した round かを返す。
//
// 比較を >= にすることで、上限が未設定（0）の Run は loop を続けずに停止する。
// 信頼境界の既定は「止まる」側へ倒す。上限そのものは claim gate が検証する。
func roundLimitReached(rounds, limit int) bool { return rounds >= limit }

func onFinalReviewCompleted(run Run, event Event) (Decision, error) {
	completed := event.(ReviewCompleted)
	if err := checkReviewBinding(run, completed, contract.ReviewFinalImplementation); err != nil {
		return Decision{}, err
	}
	run.Rounds.FinalImplementation++
	run.TotalRounds.FinalImplementation++
	switch completed.Verdict {
	case contract.VerdictApprove:
		// required checks が publish 済み head へ bind されていなければ approve を
		// PR 確定の gate として使えない。checks 無しで ready 化すると、review した
		// head と required checks を通した head が別になりうる。
		if run.ChecksHead != run.PublishedHead {
			return gate(run, completed.EventKind(),
				"required checks が publish 済み head へ bind されていない: got %s, want %s",
				run.ChecksHead, run.PublishedHead)
		}
		run.Phase = PhaseFinalizingPullRequest
		run.FinalApproval = &Approval{
			Head:          completed.Head,
			RequestDigest: completed.RequestDigest,
			ResultDigest:  completed.ResultDigest,
		}
		return decide(run, DispatchOperation{Kind: contract.OperationFinalizePullRequest})
	case contract.VerdictRequestChanges:
		if roundLimitReached(run.Rounds.FinalImplementation, run.RoundLimits.FinalImplementation) {
			return escalate(run, EscalationReviewRoundLimitExceeded)
		}
		run.Phase = PhaseImplementing
		run.FixedHead = ""
		run.ChecksHead = ""
		return decide(run, DispatchOperation{Kind: contract.OperationRepairImplementation})
	case contract.VerdictNeedsHuman:
		return escalate(run, EscalationReviewNeedsHuman)
	default:
		return gate(run, completed.EventKind(), "review verdict が不正: %q", completed.Verdict)
	}
}

// checkReviewBinding は verdict を読む前に、その review が現在の gate と head へ
// 繋留されているかを確認する。別 gate や別 head の verdict を流用できると、
// review していない head が承認済みとして進む。
func checkReviewBinding(run Run, completed ReviewCompleted, expected contract.ReviewKind) error {
	if completed.Kind != expected {
		_, err := gate(run, completed.EventKind(), "この phase が待つのは %q の verdict である: %q",
			expected, completed.Kind)
		return err
	}
	if completed.Head != run.PublishedHead {
		_, err := gate(run, completed.EventKind(), "publish 済み head への verdict でない: got %s, want %s",
			completed.Head, run.PublishedHead)
		return err
	}
	if !completed.RequestDigest.Valid() {
		_, err := gate(run, completed.EventKind(), "Review Request digest が不正: %q", completed.RequestDigest)
		return err
	}
	if !completed.ResultDigest.Valid() {
		_, err := gate(run, completed.EventKind(), "Review Result digest が不正: %q", completed.ResultDigest)
		return err
	}
	return nil
}

func onImplementationFixed(run Run, event Event) (Decision, error) {
	fixed := event.(ImplementationFixed)
	if run.TestApproval == nil {
		return gate(run, fixed.EventKind(), "test validity approve が無い")
	}
	if run.PublishedTestHead == "" || run.TestApproval.Head != run.PublishedTestHead {
		return gate(run, fixed.EventKind(),
			"test validity approve が publish 済み test head へ bind されていない: got %s, want %s",
			run.TestApproval.Head, run.PublishedTestHead)
	}
	if fixed.Head == "" {
		return gate(run, fixed.EventKind(), "実装を固定した head が空である")
	}
	if !fixed.ChecksPassed {
		return gate(run, fixed.EventKind(), "required checks を通していない実装は publish できない")
	}
	run.Phase = PhasePublishingFinalHead
	run.FixedHead = fixed.Head
	run.ChecksHead = fixed.Head
	return decide(run, DispatchOperation{Kind: contract.OperationPublishHead})
}

// onTestRevisionRequired は implementation lane からの test 差し戻しを test gate へ戻す。
//
// quality verdict ではないが test_validity の round を消費する。消費しないと、
// implement→revise→approve→implement の往復がどの予算にも数えられず、無人区間が
// 有限にならない（ADR-0003 2026-08-21 追記）。rollback 先を承認済み test checkpoint へ
// 束縛するのは、承認していない test 状態から revise_tests を始めさせないためである。
func onTestRevisionRequired(run Run, event Event) (Decision, error) {
	revision := event.(TestRevisionRequired)
	if run.TestApproval == nil {
		return gate(run, revision.EventKind(), "test validity approve が無い")
	}
	if revision.Head != run.TestApproval.Head {
		return gate(run, revision.EventKind(),
			"rollback 先が承認済み test checkpoint と一致しない: got %s, want %s",
			revision.Head, run.TestApproval.Head)
	}
	run.Rounds.TestValidity++
	run.TotalRounds.TestValidity++
	if roundLimitReached(run.Rounds.TestValidity, run.RoundLimits.TestValidity) {
		return escalate(run, EscalationReviewRoundLimitExceeded)
	}
	// 差し戻した時点で以前の approval を前提にしない。新しい test_validity approve を
	// 得るまで implementation へ戻れないことを、後段の gate ではなく state で表す。
	run.Phase = PhaseAuthoringTests
	run.FixedHead = ""
	run.ChecksHead = ""
	run.TestApproval = nil
	return decide(run, DispatchOperation{Kind: contract.OperationReviseTests})
}

func onPullRequestFinalized(run Run, event Event) (Decision, error) {
	finalized := event.(PullRequestFinalized)
	if run.FinalApproval == nil || finalized.Head != run.FinalApproval.Head {
		return gate(run, finalized.EventKind(), "final approve に bind された head だけを確定できる: got %s",
			finalized.Head)
	}
	run.Phase = PhaseMergingPullRequest
	return decide(run, DispatchOperation{Kind: contract.OperationMergePullRequest})
}

// onPullRequestMerged は merge を terminal として確定する。
//
// merge 済み head を final approve へ束縛し直すのは、gate 評価から mutation までの間に
// head が動きうるためである。ここで approve と一致しない head の merge を成功として
// 受理すると、review していない変更が base へ入ったまま Run が正常終了する。
func onPullRequestMerged(run Run, event Event) (Decision, error) {
	merged := event.(PullRequestMerged)
	if run.FinalApproval == nil || merged.Head != run.FinalApproval.Head {
		return gate(run, merged.EventKind(), "final approve に bind された head だけを merge できる: got %s",
			merged.Head)
	}
	if merged.MergeCommit == "" {
		return gate(run, merged.EventKind(), "merge commit を持たない merge は成立していない")
	}
	run.Phase = PhaseMerged
	run.MergeCommit = merged.MergeCommit
	return decide(run, ProjectStatus{Label: StatusMerged, CloseIssue: true})
}

// onObservationRecorded は audit lineage だけを進める。phase、approval、semantic
// input を触らないのが本 event の意味であり、ここで停止させると raw body の
// 非意味的差分のたびに Run が止まる。
func onObservationRecorded(run Run, event Event) (Decision, error) {
	recorded := event.(ObservationRecorded)
	run.Observation = recorded.Observation
	run.ObservationBodyDigest = recorded.BodyDigest
	return decide(run)
}

// onSemanticInputChanged は Run を打ち切り、新しい identity での再 claim へ回す。
// 古い approval は新しい入力の approval として再利用できないため破棄する。
func onSemanticInputChanged(run Run, event Event) (Decision, error) {
	changed := event.(SemanticInputChanged)
	run.Phase = PhaseSuperseded
	run.TestApproval = nil
	run.FinalApproval = nil
	return decide(run, SupersedeRun{ChangedFields: changed.ChangedFields, Input: changed.Input})
}

// onAttemptFailed は transport / execution failure を品質判断へ変換しない。
// retry budget を使い切った場合だけ人へ上げる。
func onAttemptFailed(run Run, event Event) (Decision, error) {
	failed := event.(AttemptFailed)
	if !failed.RetryBudgetExhausted {
		return decide(run, ScheduleRetry{Class: failed.Class})
	}
	return escalate(run, EscalationRetryBudgetExhausted)
}

// onHumanEscalated は明示的な escalation 要求を受ける。state machine が Run state から
// 導出する理由を外部から指定させないのは、counter lineage と reason code の食い違いを
// 防ぐためである。
func onHumanEscalated(run Run, event Event) (Decision, error) {
	escalated := event.(HumanEscalated)
	switch {
	case !escalationReasons[escalated.Reason]:
		return gate(run, escalated.EventKind(), "escalation reason が語彙に無い: %q", escalated.Reason)
	case derivedEscalationReasons[escalated.Reason]:
		return gate(run, escalated.EventKind(),
			"%q は state machine が導出する理由であり、明示的 escalation では指定できない", escalated.Reason)
	}
	return escalate(run, escalated.Reason)
}

// escalate は Run を停止し、同時に無人区間を終了させる。
//
// 予算の単位は Run の生涯ではなく「人間が次にこの Run を見るまで」である。
// 理由によらず escalation は区間の終わりなので、reset をここへ置く。resume の
// 再開 phase 選択は別問題として未設計だが、どの経路でも escalate を通るため、
// resume が実装された時点で予算は満額から始まる。
func escalate(run Run, reason EscalationReason) (Decision, error) {
	stopped := run.Phase
	stretch := run.Rounds
	run.Phase = PhaseNeedsHuman
	run.Rounds = ReviewRounds{}
	return decide(run,
		ProjectStatus{Label: StatusNeedsHuman},
		EscalateHuman{Reason: reason, StoppedAt: stopped, Rounds: stretch},
	)
}
