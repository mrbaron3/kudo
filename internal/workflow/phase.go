// Package workflow は workflow 状態の pure core を提供する。
//
// 現在は二つの model が並存する。
//
//   - 導出 model（observation.go / derivation.go / round.go / derived_transition.go /
//     candidate.go / escalation.go）: GitHub の観測 snapshot から phase、次 action、
//     review round を導出する。phase も round も保存せず、process crash 後は再観測だけで
//     同じ継続が再現される。ADR-0001 の stateless reconciler の中核であり、Controller が
//     使うのはこちらである。
//   - 実行の付帯（trigger.go / dispatch.go / retry.go）: reconcile の起動経路を閉じた
//     語彙で表す入力型、Run 単位の単一 flight 排他、class 別 backoff。いずれも
//     process-local であり workflow 状態ではない。attempt counter を保存しないのは
//     ADR-0001 の帰結である（保存すると再起動が phase 導出へ影響する）。
//   - durable model（phase.go / event.go / transition.go / run.go）: Run aggregate へ
//     分類済み event を適用して次 state と action を決める。ADR-0001 より前の設計に由来し、
//     現在の利用者は退役予定の PostgreSQL run store だけである。退役の時期は
//     docs/spec/06_project/01_implementation-plan.md が別判断として記録している。
//
// 二つは action 語彙も別空間である（ReconcileAction と Action）。導出 model は「観測から
// 見た次の一手」を、durable model は「保存済み state へ event を適用した結果」を表す。
//
// 正本は docs/spec/05_design/02_workflow.md と docs/spec/05_design/01_architecture.md である。
// 本 package は network、clock、filesystem、Issue parser、canonical YAML reader を
// 呼ばない。Run、event、観測は解決済みの opaque な identity だけを運ぶ。
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
	// PhaseMergingPullRequest は merge gate が成立し、承認済み head を base へ
	// 統合している段階である。finalize と分けるのは、body 確定のやり直しなしに
	// merge だけを再試行できるようにするためである。
	PhaseMergingPullRequest Phase = "merging_pull_request"
	// PhaseMerged は Kudo の正常 terminal である。Task Issue の close と
	// `ai-merged` label は、この phase からの投影である。
	PhaseMerged Phase = "merged"
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
	PhaseMergingPullRequest,
	PhaseMerged,
	PhaseNeedsHuman,
	PhaseSuperseded,
}

// Phases は phase 語彙を宣言順で返す。
func Phases() []Phase { return slices.Clone(phases) }

// Terminal は Run がこれ以上進まない phase かを返す。
func (p Phase) Terminal() bool {
	return p == PhaseMerged || p == PhaseSuperseded
}

// Paused は人の対応を待って停止している phase かを返す。
// Terminal と区別するのは、resume しうる停止と、二度と進まない終端で
// Controller の扱いが違うためである。
func (p Phase) Paused() bool { return p == PhaseNeedsHuman }

// Active は Operation を進められる phase かを返す。
func (p Phase) Active() bool { return !p.Terminal() && !p.Paused() }
