package workflow

import "github.com/mrbaron3/kudo/internal/contract"

// ActionKind は Controller が実行すべき副作用の種類である。
type ActionKind string

const (
	ActionDispatchOperation ActionKind = "dispatch_operation"
	ActionRequestReview     ActionKind = "request_review"
	ActionProjectStatus     ActionKind = "project_status"
	ActionScheduleRetry     ActionKind = "schedule_retry"
	ActionEscalateHuman     ActionKind = "escalate_human"
	ActionSupersedeRun      ActionKind = "supersede_run"
)

// StatusLabel は GitHub Issue へ投影する status である。
// 投影自体は outbox が行い、本 package は意図だけを返す。
type StatusLabel string

const (
	StatusInProgress    StatusLabel = "ai-in-progress"
	StatusNeedsHuman    StatusLabel = "ai-needs-human"
	StatusReviewWaiting StatusLabel = "ai-review-waiting"
)

// Action は transition が要求する副作用である。
//
// review verdict を書き換える action は定義しない。Controller は binding と
// staleness を検証するが、reviewer の品質判断を approve へ変えられない。
type Action interface {
	ActionKind() ActionKind
}

// DispatchOperation は次の Worker Operation を queue へ記録する意図である。
type DispatchOperation struct {
	Kind contract.OperationKind
}

// RequestReview は published head へ繋留した Review Request を発行する意図である。
type RequestReview struct {
	Kind contract.ReviewKind
	Head string
}

// ProjectStatus は GitHub status label の投影意図である。
type ProjectStatus struct {
	Label StatusLabel
}

// ScheduleRetry は同じ logical Operation を backoff 後に再実行する意図である。
// backoff 幅と jitter は clock を持つ層が決める。
type ScheduleRetry struct {
	Class contract.FailureClass
}

// EscalateHuman は Run ID、停止 phase、理由 code、evidence reference を含む
// 日本語 status comment を投影する意図である。ProjectStatus が label を担い、
// この action が comment の内容を決める。
//
// StoppedAt を運ぶのは、Decision の Run が既に needs_human へ動いており、
// どの phase で止まったかを Decision 単体から復元できないためである。
type EscalateHuman struct {
	Reason    EscalationReason
	StoppedAt Phase
	// Rounds は今回の無人区間で確定した gate ごとの round 数である。
	// escalate が Run の Rounds を 0 へ戻すため Decision の Run からは復元できない。
	// ledger が「今回何 round 回したか」を書けるよう action が運ぶ。
	Rounds ReviewRounds
}

// SupersedeRun は Run を打ち切り、Input の identity で再 claim させる意図である。
// 終了した Run 自体の Input は監査 lineage として元の値を保持する。
type SupersedeRun struct {
	ChangedFields []string
	Input         InputIdentity
}

func (DispatchOperation) ActionKind() ActionKind { return ActionDispatchOperation }
func (RequestReview) ActionKind() ActionKind     { return ActionRequestReview }
func (ProjectStatus) ActionKind() ActionKind     { return ActionProjectStatus }
func (ScheduleRetry) ActionKind() ActionKind     { return ActionScheduleRetry }
func (SupersedeRun) ActionKind() ActionKind      { return ActionSupersedeRun }
func (EscalateHuman) ActionKind() ActionKind     { return ActionEscalateHuman }
