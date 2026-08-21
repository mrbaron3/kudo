package workflow

import (
	"slices"

	"github.com/mrbaron3/kudo/internal/contract"
)

// EventKind は event の種類である。transition 表の key に使う。
type EventKind string

const (
	KindClaimSucceeded       EventKind = "claim_succeeded"
	KindOperationStarted     EventKind = "operation_started"
	KindTestsAuthored        EventKind = "tests_authored"
	KindHeadPublished        EventKind = "head_published"
	KindReviewCompleted      EventKind = "review_completed"
	KindImplementationFixed  EventKind = "implementation_fixed"
	KindTestRevisionRequired EventKind = "test_revision_required"
	KindPullRequestFinalized EventKind = "pull_request_finalized"
	KindPullRequestMerged    EventKind = "pull_request_merged"
	KindObservationRecorded  EventKind = "observation_recorded"
	KindSemanticInputChanged EventKind = "semantic_input_changed"
	KindAttemptFailed        EventKind = "attempt_failed"
	KindHumanEscalated       EventKind = "human_escalated"
)

var eventKinds = []EventKind{
	KindClaimSucceeded,
	KindOperationStarted,
	KindTestsAuthored,
	KindHeadPublished,
	KindReviewCompleted,
	KindImplementationFixed,
	KindTestRevisionRequired,
	KindPullRequestFinalized,
	KindPullRequestMerged,
	KindObservationRecorded,
	KindSemanticInputChanged,
	KindAttemptFailed,
	KindHumanEscalated,
}

// EventKinds は event 語彙を宣言順で返す。
func EventKinds() []EventKind { return slices.Clone(eventKinds) }

// Event は既に分類済みの workflow event である。
//
// 単一の struct へ全 field を集めず kind ごとの型に分けるのは、event が運べる値を
// 型で閉じるためである。共通 struct に optional field を並べると、
// SemanticInputChanged が review verdict を持つような、意味を成さない event を
// caller が構築できてしまい、その排除が transition 側の分岐に押し出される。
type Event interface {
	EventKind() EventKind
}

// InputIdentity は Run の semantic input を opaque な versioned ref の組で表す。
//
// Issue Observation を含めないのは、それが audit lineage であって identity では
// ないからである。意味のある変更は Task Context ref を変え、Task Context ref は
// Context Manifest に含まれるため、semantic staleness はこの 2 つの比較で判定できる。
type InputIdentity struct {
	ContextManifest contract.ContextManifestRef
	ExecutionPolicy contract.ExecutionPolicyRef
}

// ClaimSucceeded は claim が確定し Run の semantic input が固定されたことを表す。
type ClaimSucceeded struct {
	// Contextはlive sourceから各Operationの入力を再構築するためのcheckpointである。
	// canonical bytesは持たず、claimで検証済みのversion/ref/digest/baseだけを運ぶ。
	Context         contract.ClaimContext
	ExecutionPolicy contract.ExecutionPolicyRef
	// EscalationPolicy と RoundLimits は Run へ pin する Controller 側の gate 予算である。
	// ref と解決済みの値の両方を運ぶのは、pure transition が artifact を decode できない
	// 一方で、escalation の根拠としては digest が必要なためである。
	EscalationPolicy contract.EscalationPolicyRef
	RoundLimits      contract.ReviewRoundLimits
}

// OperationStarted は dispatch した Operation を Worker が実行し始めたことを表す。
//
// claim 直後だけ、次の phase へ進むのに証跡ではなく実行開始が必要になる。
// 他の phase 遷移は RED / GREEN / publish / review 結果という成果で駆動するため、
// この event は claimed からの 1 辺だけで使う。
type OperationStarted struct {
	Kind contract.OperationKind
}

// TestsAuthored は test-only head と RED evidence が固定されたことを表す。
type TestsAuthored struct {
	Head string
}

// HeadPublished は固定済み head が branch と draft PR へ publish されたことを表す。
type HeadPublished struct {
	Head        string
	PullRequest contract.PullRequestRef
}

// ReviewCompleted は Review Worker が返した versioned verdict である。
// Head は review 対象の head であり、現在 published head との binding を gate が確認する。
type ReviewCompleted struct {
	Kind          contract.ReviewKind
	Verdict       contract.ReviewVerdict
	Head          string
	RequestDigest contract.Digest
	ResultDigest  contract.Digest
}

// ImplementationFixed は GREEN と refactor 後の required checks が固定されたことを表す。
// ChecksPassed が false の実装は publish も review も gate される。
type ImplementationFixed struct {
	Head         string
	ChecksPassed bool
}

// TestRevisionRequired は implement lane が「承認済み test の変更が必要」と判断して
// 停止したことを表す。Head は最後に承認された test checkpoint へ rollback 済みの head で
// あり、根拠は test-revision-report artifact が担う。quality verdict でも failure でも
// ないが、test gate を再び開く差し戻しとして test_validity の round を消費する。
type TestRevisionRequired struct {
	Head string
}

// PullRequestFinalized は required PR body の確定と draft 解除が durable になったことを表す。
type PullRequestFinalized struct {
	Head string
}

// PullRequestMerged は承認済み head が base へ統合されたことを表す。
//
// MergeCommit を運ぶのは、merge の成立を真偽値ではなく base 側に生まれた commit で
// 表すためである。応答を失った retry は同じ commit の観測から自分の merge を再確認でき、
// intent を持たない merged 観測（外部干渉）と区別できる。
type PullRequestMerged struct {
	Head        string
	MergeCommit string
}

// ObservationRecorded は exact な観測だけが変わったことを表す audit event である。
type ObservationRecorded struct {
	Observation contract.IssueObservationRef
	BodyDigest  contract.Digest
}

// SemanticInputChanged は semantic identity が変わったことを表す。
// ChangedFields は contract の semantic comparison が返した field 名をそのまま運ぶ。
type SemanticInputChanged struct {
	ChangedFields []string
	Input         InputIdentity
}

// AttemptFailed は retry 可能な execution / transport failure である。
// RetryBudgetExhausted が false の間は品質判断へ変換せず、同じ phase で retry する。
type AttemptFailed struct {
	Class                contract.FailureClass
	RetryBudgetExhausted bool
}

// HumanEscalated は Controller または Worker が人の判断を要求したことを表す。
//
// Reason は語彙の code に限り、state machine が Run state から自ら導出する理由
// （review verdict、round 上限、retry budget）は指定できない。
type HumanEscalated struct {
	Reason EscalationReason
}

func (ClaimSucceeded) EventKind() EventKind       { return KindClaimSucceeded }
func (OperationStarted) EventKind() EventKind     { return KindOperationStarted }
func (TestsAuthored) EventKind() EventKind        { return KindTestsAuthored }
func (HeadPublished) EventKind() EventKind        { return KindHeadPublished }
func (ReviewCompleted) EventKind() EventKind      { return KindReviewCompleted }
func (ImplementationFixed) EventKind() EventKind  { return KindImplementationFixed }
func (TestRevisionRequired) EventKind() EventKind { return KindTestRevisionRequired }
func (PullRequestFinalized) EventKind() EventKind { return KindPullRequestFinalized }
func (PullRequestMerged) EventKind() EventKind    { return KindPullRequestMerged }
func (ObservationRecorded) EventKind() EventKind  { return KindObservationRecorded }
func (SemanticInputChanged) EventKind() EventKind { return KindSemanticInputChanged }
func (AttemptFailed) EventKind() EventKind        { return KindAttemptFailed }
func (HumanEscalated) EventKind() EventKind       { return KindHumanEscalated }
