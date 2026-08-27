// Package telemetry は structured log の field 語彙を固定する。
//
// field 名を呼び出し側の literal に散らさないのは、webhook / polling / claim / review が
// 同じ Run を別名で記録すると相関できなくなるためである。値は identity と機械可読な
// 分類に限り、Issue 本文、provider transcript、credential、signature を載せない
// （docs/spec/05_design/01_architecture.md の Telemetry）。
package telemetry

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// 共通 field。event は記録した事象、outcome はその機械可読な結果分類である。
const (
	FieldEvent   = "event"
	FieldOutcome = "outcome"
	// FieldError は**自分が構築した分類済み error** の診断文である。注入された
	// collaborator の error message には使わない（ErrorType を使う）。送信者へ返す
	// response body にも使わない。
	FieldError = "error"
	// FieldErrorType は注入された値の型名だけを記録する field である。
	FieldErrorType = "error_type"
	// FieldReason は失敗の機械可読な理由 code である。
	FieldReason = "reason"
	// FieldStack は containment した panic の stack trace である。
	FieldStack = "stack"
	// FieldHTTPStatus は ingress が返した HTTP status である。
	FieldHTTPStatus = "http_status"
	// FieldWebhookEvent は GitHub webhook の event 名である。
	FieldWebhookEvent = "webhook_event"
	// FieldLabelEvent は Controller が label として記録した導出 event である。
	FieldLabelEvent = "label_event"
)

// polling の運用 field。webhook 欠落の回復経路が生きているかは、cycle の間隔・所要時間・
// 最終成功時刻・backlog・rate limit 残量の組でしか判断できない
// （docs/spec/05_design/03_runtime-platform.md の Observability and operations）。
const (
	// FieldRepository は poll 対象の`owner/name`である。列挙が失敗して IssueRef が
	// 一つも得られない record では、これが唯一の対象 identity になる。
	FieldRepository = "repository"
	// FieldDurationMillis は cycle の所要時間である。
	FieldDurationMillis = "duration_ms"
	// FieldLastSuccessAt は直近に成功した poll cycle の終了時刻である。
	FieldLastSuccessAt = "last_success_at"
	// FieldRateLimitRemaining は直近の GitHub response が報告した残量である。
	FieldRateLimitRemaining = "rate_limit_remaining"
	// FieldBacklog は cycle が投入し切れなかった IssueRef の件数である。
	FieldBacklog = "backlog"
	// FieldDiscovered は重複排除後に発見した IssueRef の件数である。
	FieldDiscovered = "discovered"
	// FieldSubmitted は ReconcileIssue へ投入できた件数である。
	FieldSubmitted = "submitted"
	// FieldCandidates は candidate query が返した件数である。
	FieldCandidates = "candidates"
	// FieldOpenRuns は open な kudo Pull Request から再発見した件数である。
	FieldOpenRuns = "open_runs"
	// FieldActiveRunLabel は進行中 Run の label を持つ open Issue の件数である。
	// 記録が途中で失敗した投影はこの経路でしか再発見できない。
	FieldActiveRunLabel = "active_run_label"
	// FieldCapacityWaits は同時実行上限で再投入を待った回数である。
	FieldCapacityWaits = "capacity_waits"
	// FieldFailures は列挙に失敗した repository の件数である。
	FieldFailures = "failures"
	// FieldDelayMillis は次の cycle までの待機である。回復経路が止まっている時間は
	// この値でしか観測できない。
	FieldDelayMillis = "delay_ms"
	// FieldRetryable は失敗が一時障害として分類されているかである。恒久的な設定不備と
	// 一時障害は backoff の挙動が同じなので、この分類だけが運用上の区別になる。
	FieldRetryable = "retryable"
)

// Issue の correlation field。repository は GitHub の`owner/name`表記へ canonicalize する。
const (
	FieldIssue           = "issue"
	FieldIssueRepository = "repository"
	FieldIssueNumber     = "number"
)

// Trigger の correlation field。reconcile の起動経路を record 間で突き合わせる。
const (
	FieldTrigger       = "trigger"
	FieldTriggerSource = "source"
	FieldTriggerID     = "id"
	FieldTriggerAction = "action"
)

// ErrorType は注入された error や recover した panic 値を、message を載せずに記録する。
//
// 型名だけにするのは、message の中身を書いた側が telemetry の制約を知らないからである。
// os.ReadFile 由来の error は credential path を、reconcile 由来の error は Issue の
// 非公開本文や upstream response を含み得る。いずれも telemetry へ送らない
// （docs/spec/05_design/01_architecture.md の Telemetry、
// docs/spec/05_design/03_runtime-platform.md の Secrets and credentials）。
// 分類が必要なら、値を作る側が機械可読な code を別 field で渡す。
func ErrorType(value any) slog.Attr {
	return slog.String(FieldErrorType, fmt.Sprintf("%T", value))
}

// Issue は Issue identity を group attr として返す。
// owner / repository は GitHub の case-insensitive な identity に合わせて小文字へ揃え、
// 同じ Issue の record が表記差で別 key にならないようにする。
func Issue(ref contract.IssueRef) slog.Attr {
	repository := strings.ToLower(ref.Owner) + "/" + strings.ToLower(ref.Repository)
	return slog.Group(FieldIssue,
		slog.String(FieldIssueRepository, repository),
		slog.Int(FieldIssueNumber, ref.Number),
	)
}

// Trigger は reconcile の起動元を group attr として返す。
// Action は webhook delivery だけが持つ観測なので、空のときは field 自体を出さない。
func Trigger(trigger workflow.Trigger) slog.Attr {
	attrs := []any{
		slog.String(FieldTriggerSource, string(trigger.Source)),
		slog.String(FieldTriggerID, trigger.ID),
	}
	if trigger.Action != "" {
		attrs = append(attrs, slog.String(FieldTriggerAction, trigger.Action))
	}
	return slog.Group(FieldTrigger, attrs...)
}
