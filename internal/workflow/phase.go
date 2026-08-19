// Package workflow は Run の durable phase と、分類済み event から次 state と
// 必要 action を決める pure な transition function を提供する。
//
// 正本は docs/spec/05_design/02_workflow.md の Durable states と docs/spec/05_design/01_architecture.md である。
// 本 package は network、clock、filesystem、Issue parser、canonical YAML reader を
// 呼ばない。Run と event は解決済みの opaque な identity だけを運ぶ。
package workflow

import "slices"

// Phase は Run の durable な進行段階である。
//
// Operation の実行状況（queued、leased、retry_wait 等）は別の値空間であり、ここへ
// 混ぜない。retry 可能な失敗で phase が動くと、transport 障害が workflow の進行として
// 記録され、gate 判断と再開位置が壊れる。
type Phase string

const (
	// PhaseClaimed は claim が確定し、Run と semantic input が固定された段階である。
	PhaseClaimed Phase = "claimed"
	// PhaseAuthoringTests は test 作成と RED 固定を実行中の段階である。
	PhaseAuthoringTests Phase = "authoring_tests"
	// PhasePublishingTestHead は固定済み test head を push し draft PR を ensure する段階である。
	PhasePublishingTestHead Phase = "publishing_test_head"
	// PhaseAwaitingTestReview は published head に対する test validity review 待ちである。
	PhaseAwaitingTestReview Phase = "awaiting_test_review"
	// PhaseImplementing は承認済み test に対する実装と GREEN/refactor を実行中の段階である。
	PhaseImplementing Phase = "implementing"
	// PhasePublishingFinalHead は固定済み final head を同一 PR へ push する段階である。
	PhasePublishingFinalHead Phase = "publishing_final_head"
	// PhaseAwaitingFinalReview は published final head に対する最終 review 待ちである。
	PhaseAwaitingFinalReview Phase = "awaiting_final_review"
	// PhaseFinalizingPullRequest は required PR body 確定と draft 解除の段階である。
	PhaseFinalizingPullRequest Phase = "finalizing_pull_request"
	// PhaseAwaitingHumanReview は Kudo の正常 handoff terminal である。
	PhaseAwaitingHumanReview Phase = "awaiting_human_review"
	// PhaseNeedsHuman は実行を停止した paused Run である。terminal ではないが、
	// resume と supersede は再 reconciliation が排他的に決める。
	PhaseNeedsHuman Phase = "needs_human"
	// PhaseSuperseded は semantic input が変わって打ち切られた Run である。
	//
	// docs/spec/05_design/02_workflow.md の state 図には現れないが、同書の Escalation and resumption が
	// 「入力が変わった場合は古い Run を superseded とし、新しい Run と review lineage を
	// 作る」と定めている。stale を needs_human へ潰すと、人の判断を待つ停止と、
	// 入力が変わったので作り直す停止が同じ phase になり、再 claim が gate される。
	PhaseSuperseded Phase = "superseded"
)

// phases は宣言順の phase 語彙である。
var phases = []Phase{
	PhaseClaimed,
	PhaseAuthoringTests,
	PhasePublishingTestHead,
	PhaseAwaitingTestReview,
	PhaseImplementing,
	PhasePublishingFinalHead,
	PhaseAwaitingFinalReview,
	PhaseFinalizingPullRequest,
	PhaseAwaitingHumanReview,
	PhaseNeedsHuman,
	PhaseSuperseded,
}

// Phases は phase 語彙を宣言順で返す。
func Phases() []Phase { return slices.Clone(phases) }

// Terminal は Run がこれ以上進まない phase かを返す。
func (p Phase) Terminal() bool {
	return p == PhaseAwaitingHumanReview || p == PhaseSuperseded
}

// Paused は人の対応を待って停止している phase かを返す。
// Terminal と区別するのは、resume しうる停止と、二度と進まない終端で
// Controller の扱いが違うためである。
func (p Phase) Paused() bool { return p == PhaseNeedsHuman }

// Active は Operation を進められる phase かを返す。
func (p Phase) Active() bool { return !p.Terminal() && !p.Paused() }
