package workflow

import (
	"strconv"
	"strings"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

// 本 file は gateway observer が組み立てる snapshot の型を定義する。
// 正本は docs/spec/05_design/02_workflow.md の Derived phases と
// docs/spec/05_design/04_github-routing.md である。
//
// 観測は GitHub 上の外形事実だけを運び、phase、round、attempt といった導出結果を
// 持たない（ADR-0001）。judgement を観測へ埋めると、同じ snapshot から別の結論が
// 出る余地が adapter 側に生まれ、導出の全域性を pure function で検査できなくなる。

// Kudo が記録する check run の名前空間である。gate 判定は name と作成 App identity の
// 両方で行うため、name だけを信頼する分岐を作らない。
const (
	CheckRunEvidenceRed         = "kudo/evidence-red"
	CheckRunEvidenceGreen       = "kudo/evidence-green"
	CheckRunEvidenceChecks      = "kudo/evidence-checks"
	CheckRunTestValidity        = "kudo/test-validity"
	CheckRunFinalImplementation = "kudo/final-implementation"
)

// LabelReady は人間が所有する one-shot execution request の label である。
// Kudo が投影する StatusLabel とは所有者が違うため、同じ値空間へ混ぜない。
const LabelReady = "ai-ready"

// IssueBranchName は Issue に対する claim branch の名前である。
//
// claim の排他は この名前の ref create の atomicity で成立する。観測側と mutation 側で
// 規則がずれると排他が効かないため、名前の決定はここ一箇所に置く。
func IssueBranchName(issue int64) string {
	return "kudo/issue-" + strconv.FormatInt(issue, 10)
}

// IssueNumberFromBranch は claim branch 名から Issue number を復元する。
//
// polling は open な Pull Request の head branch からしか Run の Issue を知れないため、
// IssueBranchName の逆写像をここへ置く。canonical な形（正の 10 進、leading zero なし）
// だけを受理するのは、`kudo/issue-019`のような別名を同じ Issue の branch として扱うと、
// claim の排他が名前の一意性で成立しなくなるためである。
func IssueNumberFromBranch(branch string) (int64, bool) {
	suffix, ok := strings.CutPrefix(branch, "kudo/issue-")
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || number <= 0 || suffix != strconv.FormatInt(number, 10) {
		return 0, false
	}
	return number, true
}

// ActorIdentity は一つの actor が GitHub 上で持つ immutable な numeric identity である。
// App ID（check run の作成者）と bot user ID（comment の author）は別 namespace なので
// 両方を明示する。
type ActorIdentity struct {
	CheckRunAppID   int64
	CommentAuthorID int64
}

// Observation は 1 IssueRef に対する live GitHub 観測の snapshot である。
//
// Comments は Run の記録面である Pull Request 上の comment であり、Issue 本文側の
// 会話ではない。round 導出が Issue の議論を数えないための境界である。
type Observation struct {
	Issue        IssueObservation
	Branch       *BranchObservation
	PullRequests []PullRequestObservation
	CheckRuns    []CheckRunObservation
	Comments     []CommentObservation
}

type IssueState string

const (
	IssueStateOpen   IssueState = "open"
	IssueStateClosed IssueState = "closed"
)

// IssueObservation は Issue の routing metadata である。raw body、Contract block、
// Acceptance Criteria は含まない。それらは Issue Compiler の入力であり、
// phase 導出が読むと workflow が Task Context の version ごとに分岐する parser になる。
type IssueObservation struct {
	Number    int64
	State     IssueState
	Assignees []string
	Labels    []string
	// LabelEvents は label の付与・除去の timeline である。無人区間の起点
	// （直近の`ai-ready`付与）を確定するために必要で、現在の label set では復元できない。
	LabelEvents []LabelEventObservation
	// Dependencies は native relationship から観測した依存 Issue の完了状態である。
	// Contract block の`dependsOn`との整合は claim（#17）が検証する。
	Dependencies []DependencyObservation
}

type LabelEventObservation struct {
	Label string
	// Added は付与なら true、除去なら false である。
	Added      bool
	OccurredAt time.Time
}

type DependencyObservation struct {
	Number int64
	Closed bool
}

// BranchObservation は claim branch の観測である。
// Head が空なのは ref が commit を解決できない状態（壊れた claim）であり、
// 「branch が無い」とは区別する。
type BranchObservation struct {
	Name string
	Head string
}

type PullRequestState string

const (
	// PullRequestStateDraft は open かつ draft の PR である。
	PullRequestStateDraft PullRequestState = "draft"
	// PullRequestStateReady は open かつ draft 解除済みの PR である。
	PullRequestStateReady PullRequestState = "ready"
	// PullRequestStateClosed は merge されずに closed された PR である。
	PullRequestStateClosed PullRequestState = "closed"
	PullRequestStateMerged PullRequestState = "merged"
)

// PullRequestObservation は Run の記録面である Pull Request の観測である。
//
// HeadLineage は live head から base 直後までの commit を新しい順に並べた系譜である。
// Derived phases 表の「live head 系譜に verdict がある」を pure に判定するために観測へ
// 含める。系譜を持たずに ancestry を adapter へ問い合わせる形にすると、導出が I/O を
// 持つか、adapter 側の判断へ分岐が漏れる。
type PullRequestObservation struct {
	Number      int64
	State       PullRequestState
	Head        string
	HeadLineage []string
	// MergeGate は merge の外形条件の観測である。open な PR でだけ意味を持ち、
	// merge 直前の gate 評価にだけ使う。
	MergeGate MergeGateObservation
}

// CheckRollupState は required status check の集約状態である。
//
// 「required check が設定されていない」は success へ写像する（gate 条件が空集合に対して
// 成立するため）。この写像は adapter の責務であり、core は結果だけを読む。
type CheckRollupState string

const (
	CheckRollupPending CheckRollupState = "pending"
	CheckRollupSuccess CheckRollupState = "success"
	CheckRollupFailure CheckRollupState = "failure"
)

// MergeabilityState は GitHub が計算した merge 可能性である。
// GitHub は計算中に不定値を返すため、それを`computing`として明示的に観測する。
type MergeabilityState string

const (
	MergeabilityComputing   MergeabilityState = "computing"
	MergeabilityMergeable   MergeabilityState = "mergeable"
	MergeabilityConflicting MergeabilityState = "conflicting"
)

// MergeGateObservation は docs/spec/05_design/02_workflow.md の Merge と完了投影 が定める
// 外形条件のうち、read-only な pull request / check 観測で確かめられるものである。
//
// 承認済み head と live head の一致は導出が head binding で確かめ、base の一致は
// mutation 直前に merge_pull_request が live 照合する。ここに畳むと、claim checkpoint に
// pin した base を導出が持つことになる。
//
// zero value は語彙外であり、gate 評価時に protocol 違反として拒否する。observer が
// 埋め忘れた観測を「まだ待つ」へ倒すと、merge が黙って進まない Run になる。
type MergeGateObservation struct {
	RequiredChecks CheckRollupState
	Mergeable      MergeabilityState
}

// CheckRunObservation は App 所有の check run 観測である。
//
// AppID を必ず運ぶ。name だけで verdict を判定すると、Implementer 名義の
// `kudo/test-validity`が gate を通してしまい、自己承認の禁止が構造で担保されない。
// Verdict は verdict check run の machine block から読んだ値であり、evidence check run
// では空になる。comment 本文の自然文から推測した値を入れてはならない。
type CheckRunObservation struct {
	Name    string
	Head    string
	AppID   int64
	Verdict contract.ReviewVerdict
}

// CommentMarkerKind は round 計数の対象になる record marker の種類である。
// 生の marker 文字列の encode / parse は record surface adapter の責務であり、
// core は分類済みの観測だけを受け取る。
type CommentMarkerKind string

const (
	// CommentMarkerFinding は Review Worker が gate ごとに記録する finding comment である。
	CommentMarkerFinding CommentMarkerKind = "finding"
	// CommentMarkerTestRevisionReport は implement lane が返した`test_revision_required`の
	// 根拠 report である。quality verdict ではないが test gate を再び開くため、
	// test_validity の無人 round 予算を消費し、実装 lane を止めて revise_tests へ戻す。
	CommentMarkerTestRevisionReport CommentMarkerKind = "test-revision-report"
	// CommentMarkerMergeIntent は Issue Worker が merge 直前に記録する intent である。
	// merged 観測がこの intent と一致するかどうかが、自分の mutation の再観測と
	// 外部干渉を分ける（docs/spec/05_design/02_workflow.md の Merge と完了投影）。
	CommentMarkerMergeIntent CommentMarkerKind = "merge-intent"
)

// CommentMarkerObservation は comment に埋め込まれた marker の分類結果である。
//
// marker が書いている round 番号は保持しない。round は marker 付き comment の計数から
// 導出するものであり、記録された数値を信じると人為的な編集が counter を動かせる。
// Head は逆に保持する。rollback checkpoint と merge intent は「どの commit に対する
// 記録か」が意味そのものであり、head を落とすと過去の記録と現在の head を区別できない。
type CommentMarkerObservation struct {
	Kind CommentMarkerKind
	// Review は Kind が CommentMarkerFinding のとき、どの gate の finding かを表す。
	Review contract.ReviewKind
	// Head はその記録が束縛される commit SHA である。差し戻し checkpoint と merge intent
	// では意味そのものであり、finding では対応する verdict check run を引くための join key
	// になる（advisory finding だけの approve を round から外すために使う）。
	Head string
}

// CommentObservation は Pull Request 上の comment 観測である。
//
// Body を持たないのは、gate 判断が marker と作成 identity だけを使うためである。
// PullRequest を持つのは、Run の lineage に closed / merged な旧 PR が残るためである。
// どの PR の記録かを型で表さないと、旧 Run の finding が現在の無人区間へ混入し、
// round 予算が実際より早く尽きる。
type CommentObservation struct {
	ID          int64
	PullRequest int64
	AuthorID    int64
	CreatedAt   time.Time
	Marker      *CommentMarkerObservation
}
