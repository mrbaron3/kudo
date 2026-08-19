package workflow

import "github.com/mrbaron3/kudo/internal/contract"

// ActionKind は Controller が実行すべき副作用の種類である。
type ActionKind string

const (
	ActionDispatchOperation ActionKind = "dispatch_operation"
	ActionRequestReview     ActionKind = "request_review"
	ActionProjectStatus     ActionKind = "project_status"
	ActionScheduleRetry     ActionKind = "schedule_retry"
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
