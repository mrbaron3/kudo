package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mrbaron3/kudo/internal/contract"
)

// TriggerSource は ReconcileIssue を起動した経路である。
//
// closed vocabulary にしているのは、webhook と polling が同じ ReconcileIssue へ収束
// していることを型で示すためである。値は observability にだけ使い、candidate 判定や
// Issue Contract の入力にしない（docs/spec/05_design/04_github-routing.md）。
type TriggerSource string

const (
	TriggerWebhookDelivery TriggerSource = "webhook_delivery"
	TriggerScheduledPoll   TriggerSource = "scheduled_poll"
	TriggerStartup         TriggerSource = "startup"
)

var triggerSources = []TriggerSource{TriggerWebhookDelivery, TriggerScheduledPoll, TriggerStartup}

// TriggerSources は起動経路の語彙を宣言順で返す。
func TriggerSources() []TriggerSource { return slices.Clone(triggerSources) }

func (s TriggerSource) Valid() bool { return slices.Contains(triggerSources, s) }

// Trigger は reconcile の起動元を相関するための audit metadata である。
type Trigger struct {
	Source TriggerSource
	// ID は webhook delivery ID、poll cycle ID、startup reconciliation ID である。
	// 経路をまたいで一意である必要はなく、log の相関に使う。
	ID string
	// Action は webhook delivery の issues action である。他の source は空にする。
	// 値を持てるのが webhook だけなのは、polling には action という観測が存在せず、
	// 空でない Action が「webhook 相当の情報がある」と誤読されるのを防ぐためである。
	Action string
}

// ErrInvalidReconcileRequest は reconcile 入力の identity 不足を表す。
// 呼び出し側は errors.Is で分類し、message 一致で分岐しない。
var ErrInvalidReconcileRequest = errors.New("invalid reconcile request")

// ReconcileRequest は ReconcileIssue の入力である。
//
// webhook adapter と poller はいずれもこの値だけを組み立てて同じ操作を呼ぶ。
// payload の Issue snapshot を持たないのは、実装入力を live GitHub state に限る
// ためである（webhook payload は source of truth ではない）。
type ReconcileRequest struct {
	Issue   contract.IssueRef
	Trigger Trigger
}

// Validate は identity の欠落だけを検査する。Issue が Kudo の対応候補かどうかは
// live state の観測から導出する判断であり、ここでは扱わない。
func (r ReconcileRequest) Validate() error {
	if r.Issue.Owner == "" || r.Issue.Repository == "" {
		return fmt.Errorf("%w: repository identity が欠落している", ErrInvalidReconcileRequest)
	}
	if r.Issue.Number <= 0 {
		return fmt.Errorf("%w: Issue number が正でない", ErrInvalidReconcileRequest)
	}
	if !r.Trigger.Source.Valid() {
		return fmt.Errorf("%w: trigger source %q が語彙に無い", ErrInvalidReconcileRequest, r.Trigger.Source)
	}
	if r.Trigger.ID == "" {
		return fmt.Errorf("%w: trigger ID が欠落している", ErrInvalidReconcileRequest)
	}
	if r.Trigger.Action != "" && r.Trigger.Source != TriggerWebhookDelivery {
		return fmt.Errorf("%w: trigger source %q は action を持たない", ErrInvalidReconcileRequest, r.Trigger.Source)
	}
	return nil
}
