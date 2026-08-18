package workflow

import "github.com/mrbaron3/kudo/internal/contract"

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
	{PhasePublishingFinalHead, KindHeadPublished}:          onFinalHeadPublished,
	{PhaseAwaitingFinalReview, KindReviewCompleted}:        onFinalReviewCompleted,
	{PhaseFinalizingPullRequest, KindPullRequestFinalized}: onPullRequestFinalized,
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
	kind := event.EventKind()
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
		return universal.apply(run, event)
	}
	apply, ok := transitions[transitionKey{run.Phase, kind}]
	if !ok {
		return Decision{}, transitionErr(TransitionNotAllowed, run.Phase, kind, "この phase では宣言されていない")
	}
	return apply(run, event)
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
	run.Phase = PhaseClaimed
	run.Input = claimed.Input
	run.Observation = claimed.Observation
	return decide(run,
		ProjectStatus{Label: StatusInProgress},
		DispatchOperation{Kind: contract.OperationAuthorTests},
	)
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
	return onHeadPublished(run, event, PhaseAwaitingTestReview, contract.ReviewTestValidity)
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
	switch completed.Verdict {
	case contract.VerdictApprove:
		run.Phase = PhaseImplementing
		run.TestApproval = &Approval{Head: completed.Head, RequestDigest: completed.RequestDigest}
		return decide(run, DispatchOperation{Kind: contract.OperationImplement})
	case contract.VerdictRequestChanges:
		// 同じ論理 lane へ差し戻す。fresh session は Worker 側の規律であり、
		// phase としては test 作成へ戻ることだけを表す。
		run.Phase = PhaseAuthoringTests
		run.FixedHead = ""
		return decide(run, DispatchOperation{Kind: contract.OperationReviseTests})
	case contract.VerdictNeedsHuman:
		return pause(run)
	default:
		return gate(run, completed.EventKind(), "review verdict が不正: %q", completed.Verdict)
	}
}

func onFinalReviewCompleted(run Run, event Event) (Decision, error) {
	completed := event.(ReviewCompleted)
	if err := checkReviewBinding(run, completed, contract.ReviewFinalImplementation); err != nil {
		return Decision{}, err
	}
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
		run.FinalApproval = &Approval{Head: completed.Head, RequestDigest: completed.RequestDigest}
		return decide(run, DispatchOperation{Kind: contract.OperationFinalizePullRequest})
	case contract.VerdictRequestChanges:
		run.Phase = PhaseImplementing
		run.FixedHead = ""
		run.ChecksHead = ""
		return decide(run, DispatchOperation{Kind: contract.OperationRepairImplementation})
	case contract.VerdictNeedsHuman:
		return pause(run)
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
	return nil
}

func onImplementationFixed(run Run, event Event) (Decision, error) {
	fixed := event.(ImplementationFixed)
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

func onPullRequestFinalized(run Run, event Event) (Decision, error) {
	finalized := event.(PullRequestFinalized)
	if run.FinalApproval == nil || finalized.Head != run.FinalApproval.Head {
		return gate(run, finalized.EventKind(), "final approve に bind された head だけを確定できる: got %s",
			finalized.Head)
	}
	run.Phase = PhaseAwaitingHumanReview
	return decide(run, ProjectStatus{Label: StatusReviewWaiting})
}

// onObservationRecorded は audit lineage だけを進める。phase、approval、semantic
// input を触らないのが本 event の意味であり、ここで停止させると raw body の
// 非意味的差分のたびに Run が止まる。
func onObservationRecorded(run Run, event Event) (Decision, error) {
	recorded := event.(ObservationRecorded)
	run.Observation = recorded.Observation
	return decide(run)
}

// onSemanticInputChanged は Run を打ち切り、新しい identity での再 claim へ回す。
// 古い approval は新しい入力の approval として再利用できないため破棄する。
func onSemanticInputChanged(run Run, event Event) (Decision, error) {
	changed := event.(SemanticInputChanged)
	run.Phase = PhaseSuperseded
	run.Input = changed.Input
	run.TestApproval = nil
	run.FinalApproval = nil
	return decide(run, SupersedeRun{ChangedFields: changed.ChangedFields})
}

// onAttemptFailed は transport / execution failure を品質判断へ変換しない。
// retry budget を使い切った場合だけ人へ上げる。
func onAttemptFailed(run Run, event Event) (Decision, error) {
	failed := event.(AttemptFailed)
	if !failed.RetryBudgetExhausted {
		return decide(run, ScheduleRetry{Class: failed.Class})
	}
	return pause(run)
}

func onHumanEscalated(run Run, event Event) (Decision, error) {
	return pause(run)
}

func pause(run Run) (Decision, error) {
	run.Phase = PhaseNeedsHuman
	return decide(run, ProjectStatus{Label: StatusNeedsHuman})
}
