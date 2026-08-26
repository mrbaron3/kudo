// Package telemetry は structured log の field 語彙を固定する。
//
// field 名を呼び出し側の literal に散らさないのは、webhook / polling / claim / review が
// 同じ Run を別名で記録すると相関できなくなるためである。値は identity と機械可読な
// 分類に限り、Issue 本文、provider transcript、credential、signature を載せない
// （docs/spec/05_design/01_architecture.md の Telemetry）。
package telemetry

import (
	"log/slog"
	"strings"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// 共通 field。event は記録した事象、outcome はその機械可読な結果分類である。
const (
	FieldEvent   = "event"
	FieldOutcome = "outcome"
	// FieldError は運用者向けの内部診断文である。送信者へ返す response body には使わない。
	FieldError = "error"
	// FieldHTTPStatus は ingress が返した HTTP status である。
	FieldHTTPStatus = "http_status"
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
