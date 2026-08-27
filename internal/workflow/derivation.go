package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mrbaron3/kudo/internal/contract"
)

// PhaseCandidate は Run がまだ無く、claim の候補条件を満たしている観測を表す。
// durable phase ではないため phases には含めない（PhaseNew と同じ扱いである）。
const PhaseCandidate Phase = "candidate"

// PhaseNone は Run も候補条件も無い観測を表す。
//
// 明示的な値を置くのは、zero value と「導出した結果 Run が無い」を区別するためである。
// 空文字を返すと、呼び出し側の未初期化と導出結果が同じ形になる。
//
// PhaseCandidate と PhaseNone は durable 語彙（Phases）に無いため、Phase.Active /
// Terminal / Paused の述語はこの 2 値に対して意味を持たない。導出結果の分類には
// Next の ReconcileActionKind を使う。
const PhaseNone Phase = "none"

// derivedPhases は docs/spec/05_design/02_workflow.md の Derived phases 表の行を、表の
// 評価順で並べたものである。durable phase 語彙（phases）は publish 中の中間段階を
// 含むが、それらは観測に現れないためこの表には無い。
var derivedPhases = []Phase{
	PhaseNeedsHuman,
	PhaseMerged,
	PhaseSuperseded,
	PhaseMergingPullRequest,
	PhaseFinalizingPullRequest,
	PhaseAwaitingFinalReview,
	PhaseImplementing,
	PhaseAwaitingTestReview,
	PhaseAuthoringTests,
	PhaseClaimed,
	PhaseCandidate,
}

// DerivedPhases は Derived phases 表の phase を表の評価順で返す。
func DerivedPhases() []Phase { return slices.Clone(derivedPhases) }

// ReconcileActionKind は導出 phase に対して Controller が次に行う副作用の種類である。
type ReconcileActionKind string

const (
	ReconcileDispatchOperation ReconcileActionKind = "dispatch_operation"
	ReconcileRequestReview     ReconcileActionKind = "request_review"
	// ReconcileRecordCompletion は`ai-merged`の記録と Task Issue の close である。
	ReconcileRecordCompletion ReconcileActionKind = "record_completion"
	// ReconcileEscalateHuman は理由 code 付きで自動継続をやめる意図である。
	ReconcileEscalateHuman ReconcileActionKind = "escalate_human"
	// ReconcileAwaitHuman は既に`ai-needs-human`が付いた停止 Run である。resume と
	// supersede は`ai-ready`の再付与と ResumeIdentity の照合が決めるため、ここでは
	// 何も進めない。
	ReconcileAwaitHuman ReconcileActionKind = "await_human"
	// ReconcileAwaitDependency は dependency 未完了の待機である。`ai-ready`を消費せず、
	// 次の reconcile が再評価する。failure でも needs_human でもない。
	ReconcileAwaitDependency ReconcileActionKind = "await_dependency"
	// ReconcileAwaitMergeGate は required check の pending 待ちである。品質 verdict でも
	// execution failure でもなく、retry budget を消費しない backoff 再照合である。
	ReconcileAwaitMergeGate ReconcileActionKind = "await_merge_gate"
	// ReconcileSkipNotCandidate は Kudo の制御対象外の Issue に対する no-op である。
	ReconcileSkipNotCandidate ReconcileActionKind = "skip_not_candidate"
	// ReconcileNone は terminal な lineage に対して行うことが無いことを表す。
	ReconcileNone ReconcileActionKind = "none"
)

// ReconcileAction は導出結果に対する単一の次 action である。
//
// interface ではなく comparable な struct にしているのは、「同じ観測から常に同じ
// 次 action」という AC を等値比較で検査できるようにするためである。Kind に対応
// しない field は zero value のままにする。
type ReconcileAction struct {
	Kind      ReconcileActionKind
	Operation contract.OperationKind
	Review    contract.ReviewKind
	Reason    EscalationReason
	// StoppedAt は Kind が ReconcileEscalateHuman のとき、停止した時点の phase である。
	// Derivation.Phase は停止後の needs_human になるため、これが無いと status comment に
	// 必須の停止 phase を復元できない。表のどの行にも一致しない観測では zero value にし、
	// 「停止 phase を特定できない」ことも比較できるようにする。
	StoppedAt Phase
}

// Derivation は観測から導出した現在 phase と次 action である。
//
// Run の記録面 identity（Pull Request 番号と live head）を併せて返す。どの PR が Run か、
// open PR が複数ある観測をどう扱うかの規則は導出の一部であり、呼び出し側が同じ分類を
// 書き直すと観測組合せの穴がそちらへ漏れる。Run がまだ無い phase では zero value になる。
//
// round 予算は反映していない。上限は claim 時に pin した Escalation Policy 由来で観測に
// 現れないため、ApplyRoundBudget で合成する。
type Derivation struct {
	Phase       Phase
	Next        ReconcileAction
	PullRequest int64
	Head        string
}

// DeriveConfig は導出が使う deployment 固定の identity 境界である。
//
// label 名と assignee を値として受け取るのは、routing 対象が deployment 設定で
// 上書きできるためである（docs/spec/05_design/04_github-routing.md）。
type DeriveConfig struct {
	Issue           int64
	Assignee        string
	ReadyLabel      string
	NeedsHumanLabel string
	// Implementer と Reviewer は別 identity でなければならない。同じ値を許すと、
	// Implementer が作った`kudo/test-validity`が Reviewer 名義の verdict として gate を
	// 通り、自己承認の禁止が構造ではなく規約に退化する。
	//
	// implementation-plan の立ち上がりが認める単一 PAT identity は、verdict を評価しない
	// 区間（gateway と claim）のためのものであり、Review Worker 着手までに App を分離する
	// 前提である。verdict を読むこの core では分離を必須にする。
	Implementer ActorIdentity
	Reviewer    ActorIdentity
}

// ErrDeriveConfigIncomplete は導出の identity 境界が確定していないことを表す。
var ErrDeriveConfigIncomplete = errors.New("導出の identity 境界が確定していない")

// Validate は導出に必要な identity がすべて解決済みかを検証する。
//
// 欠けた identity を既定値で補わないのは、補うと「Reviewer 名義でない verdict」と
// 「Reviewer identity を知らない」が同じ結果になり、未設定の deployment が spoof された
// verdict を承認として通してしまうためである。
func (c DeriveConfig) Validate() error {
	fields := []struct {
		name  string
		empty bool
	}{
		{"issue", c.Issue <= 0},
		{"assignee", c.Assignee == ""},
		{"readyLabel", c.ReadyLabel == ""},
		{"needsHumanLabel", c.NeedsHumanLabel == ""},
		{"implementer.checkRunAppId", c.Implementer.CheckRunAppID <= 0},
		{"implementer.commentAuthorId", c.Implementer.CommentAuthorID <= 0},
		{"reviewer.checkRunAppId", c.Reviewer.CheckRunAppID <= 0},
		{"reviewer.commentAuthorId", c.Reviewer.CommentAuthorID <= 0},
	}
	for _, field := range fields {
		if field.empty {
			return fmt.Errorf("%w: %s", ErrDeriveConfigIncomplete, field.name)
		}
	}
	if c.Implementer.CheckRunAppID == c.Reviewer.CheckRunAppID ||
		c.Implementer.CommentAuthorID == c.Reviewer.CommentAuthorID {
		return fmt.Errorf("%w: Implementer と Reviewer は別 identity でなければならない",
			ErrDeriveConfigIncomplete)
	}
	return nil
}

// 導出が観測を受理できない理由は 2 種類あり、人間が取るべき対処が違う。
// 導出内部の制御にだけ使い、外へは needs_human の Derivation として出る。
var (
	// errExternalMutation は、Kudo の Operation では作れない形へ record surface が
	// 変えられた観測である（Run 中の Issue close、intent の無い merge、branch の消失等）。
	errExternalMutation = errors.New("Kudo の Operation では作れない観測である")
	// errInvalidRecord は、記録や観測そのものが protocol を満たさない場合である
	// （語彙外 verdict、同じ head の矛盾 verdict、live head を含まない系譜等）。
	// 同じ input の retry では復旧できないため、外部干渉と分けて人へ返す。
	errInvalidRecord = errors.New("記録が versioned protocol を満たさない")
)

// Derive は観測 snapshot から現在 phase と次 action を導出する。
//
// pure かつ全域である。network、clock、filesystem、Issue parser を呼ばず、引数を
// 書き換えず、同じ snapshot からは常に同じ結果を返す。表のどの行にも安全に一致しない
// 観測は`needs_human`へ写像し、既定で継続へ倒す分岐を持たない。
//
// 保存された phase を引数に取らないのは ADR-0001 の中核である。process が消えても
// 再観測だけで同じ継続が再現できることを、引数の形で保証する。
func Derive(observation Observation, config DeriveConfig) Derivation {
	if err := config.Validate(); err != nil {
		return escalated(EscalationExternalConfigurationRequired, PhaseNew)
	}
	derivation, err := derive(observation, config)
	if err != nil {
		// 理由 code の語彙は docs/spec/05_design/04_github-routing.md が正本であり、
		// ここで増やさない。分類できない error も含め、既定は停止側へ倒す。
		if errors.Is(err, errInvalidRecord) {
			return escalated(EscalationProtocolValidationFailed, PhaseNew)
		}
		return escalated(EscalationExternalMutationConflict, PhaseNew)
	}
	return derivation
}

// ApplyRoundBudget は差し戻し dispatch を無人区間の round 予算と突き合わせる。
//
// pure である。Derive は観測だけから次 action を決めるが、round 上限は claim 時に pin した
// Escalation Policy 由来で観測に現れない。上限に達した`request_changes`は修正 Operation を
// 発行せず`review_round_limit_exceeded`で needs_human へ送る（docs/spec/05_design/02_workflow.md の
// Review round 上限）。この合成点が無いと、無人 loop の唯一の防波堤が呼び出し側の
// 書き忘れで開く。
//
// 予算を消費するのは差し戻し（revise_tests / repair_implementation）だけである。approve は
// 上限に達していても次の gate へ進む。差し戻し以外の action はそのまま返す。
func ApplyRoundBudget(derivation Derivation, rounds DerivedReviewRounds,
	limits contract.ReviewRoundLimits) Derivation {
	if derivation.Next.Kind != ReconcileDispatchOperation {
		return derivation
	}
	var reached bool
	var stoppedAt Phase
	switch derivation.Next.Operation {
	case contract.OperationReviseTests:
		reached = roundLimitReached(rounds.CurrentStretch.TestValidity, limits.TestValidity)
		stoppedAt = PhaseAwaitingTestReview
	case contract.OperationRepairImplementation:
		reached = roundLimitReached(rounds.CurrentStretch.FinalImplementation,
			limits.FinalImplementation)
		stoppedAt = PhaseAwaitingFinalReview
	default:
		return derivation
	}
	if !reached {
		return derivation
	}
	stopped := escalated(EscalationReviewRoundLimitExceeded, stoppedAt)
	stopped.PullRequest = derivation.PullRequest
	stopped.Head = derivation.Head
	return stopped
}

func escalated(reason EscalationReason, stoppedAt Phase) Derivation {
	return Derivation{
		Phase: PhaseNeedsHuman,
		Next:  ReconcileAction{Kind: ReconcileEscalateHuman, Reason: reason, StoppedAt: stoppedAt},
	}
}

func derive(observation Observation, config DeriveConfig) (Derivation, error) {
	// snapshot が別 Issue のものなら、それ以上の判断は全部あてにならない。config 側の
	// identity だけを信じて claim を dispatch すると、取り違えた観測が mutation になる。
	if observation.Issue.Number != config.Issue {
		return Derivation{}, errInvalidRecord
	}
	if containsIdentity(observation.Issue.Labels, config.NeedsHumanLabel) {
		return Derivation{Phase: PhaseNeedsHuman, Next: ReconcileAction{Kind: ReconcileAwaitHuman}}, nil
	}

	active, lineage, err := classifyPullRequests(observation.PullRequests)
	if err != nil {
		return Derivation{}, err
	}
	if lineage.merged != nil {
		return deriveMerged(observation, config, *lineage.merged)
	}

	branch, err := activeBranch(observation.Branch, config)
	if err != nil {
		return Derivation{}, err
	}

	if active == nil && lineage.closed {
		// 後始末（PR close と branch 削除）が終わった lineage だけが superseded である。
		// branch が残っている限り新しい claim は ref create で失敗するため、ここを
		// 「行うことが無い」で返すと誰も branch を消さず、新しい Run も escalation も
		// 起きない無言の停止になる。中断した supersede と、進行中 Run の PR を人間が
		// close した観測（intent に紐付かない close）は、どちらもこの形で現れる。
		// workflow.md は close intent の marker を定義していないため両者を区別できず、
		// 人へ返す側へ倒す（Escalation and resumption / Merge と完了投影）。
		if branch != nil {
			return Derivation{}, errExternalMutation
		}
		// 後始末が済んだ closed lineage は Run の履歴であって現在の Run ではない。
		// observer は state=all で列挙するため closed な kudo PR は永続的に観測され続け、
		// ここで PhaseSuperseded に固定すると人間が`ai-ready`を付け直しても claim が
		// 二度と dispatch されない。claimable 条件が拒むのは merged な kudo PR と
		// active Run であり、closed lineage ではない
		// （docs/spec/05_design/04_github-routing.md の Candidate selection、
		// docs/spec/05_design/02_workflow.md の Escalation and resumption）。
		if isCandidate(observation.Issue, config) {
			return deriveCandidate(observation.Issue, config), nil
		}
		return Derivation{Phase: PhaseSuperseded, Next: ReconcileAction{Kind: ReconcileNone}}, nil
	}
	// merge も supersede もしていない Run の途中で Issue が閉じられるのは外部干渉で
	// あり、安全な checkpoint で止める（docs/spec/05_design/04_github-routing.md）。
	if observation.Issue.State != IssueStateOpen && (branch != nil || active != nil) {
		return Derivation{}, errExternalMutation
	}

	switch {
	case active != nil:
		if branch == nil {
			// PR の記録面だけが残り、compare-and-push の対象が消えている。
			return Derivation{}, errExternalMutation
		}
		if !validLineage(*active) {
			return Derivation{}, errInvalidRecord
		}
		derivation, deriveErr := deriveRun(observation, config, *active)
		if deriveErr != nil {
			return Derivation{}, deriveErr
		}
		derivation.PullRequest = active.Number
		derivation.Head = active.Head
		return derivation, nil
	case branch != nil:
		return Derivation{
			Phase: PhaseClaimed,
			Next:  ReconcileAction{Kind: ReconcileDispatchOperation, Operation: contract.OperationClaim},
		}, nil
	default:
		return deriveCandidate(observation.Issue, config), nil
	}
}

// pullRequestLineage は終端した記録面の分類である。merged は「どの head が base へ
// 入ったか」を intent と照合するため、真偽ではなく観測そのものを保持する。
type pullRequestLineage struct {
	merged *PullRequestObservation
	closed bool
}

// classifyPullRequests は open な Run の PR と、終端した lineage を切り分ける。
//
// open な PR が複数ある観測は排他が壊れている（1 IssueRef の active Run は最大一つ）。
// どちらを Run とみなしても片方の記録が捨てられるため、選ばずに fail-closed へ倒す。
// merged が複数ある観測も同じ理由で受理しない。
func classifyPullRequests(pullRequests []PullRequestObservation) (*PullRequestObservation, pullRequestLineage, error) {
	var active *PullRequestObservation
	var lineage pullRequestLineage
	for index, pullRequest := range pullRequests {
		switch pullRequest.State {
		case PullRequestStateMerged:
			if lineage.merged != nil {
				return nil, lineage, errExternalMutation
			}
			lineage.merged = &pullRequests[index]
		case PullRequestStateClosed:
			lineage.closed = true
		case PullRequestStateDraft, PullRequestStateReady:
			if active != nil {
				return nil, lineage, errExternalMutation
			}
			active = &pullRequests[index]
		default:
			return nil, lineage, errInvalidRecord
		}
	}
	return active, lineage, nil
}

// deriveMerged は merged 観測が Kudo 自身の merge の再観測かを確かめる。
//
// merge は取り消せない mutation なので、外形が merged であることだけを根拠に完了へ
// 投影しない。Issue Worker は merge 直前に対象 head を含む intent comment を自分の
// 名義で記録する。intent と一致しない merged は Kudo の merge ではなく、品質 verdict
// にも変換できない外部干渉である（docs/spec/05_design/02_workflow.md の Merge と完了投影）。
func deriveMerged(observation Observation, config DeriveConfig,
	merged PullRequestObservation) (Derivation, error) {
	if !hasMarker(observation, config.Implementer.CommentAuthorID,
		CommentMarkerMergeIntent, merged.Number, merged.Head) {
		return Derivation{}, errExternalMutation
	}
	return Derivation{
		Phase:       PhaseMerged,
		Next:        ReconcileAction{Kind: ReconcileRecordCompletion},
		PullRequest: merged.Number,
		Head:        merged.Head,
	}, nil
}

// hasMarker は指定 actor 名義で、指定 PR の指定 head へ束縛された marker があるかを返す。
// comment 本文は repository write 権限者が編集できるため、marker の存在だけで
// gate を通さず、作成 identity と head binding を必ず併せて確認する。
func hasMarker(observation Observation, authorID int64, kind CommentMarkerKind,
	pullRequest int64, head string) bool {
	if head == "" {
		return false
	}
	for _, comment := range observation.Comments {
		if comment.AuthorID == authorID && comment.PullRequest == pullRequest &&
			comment.Marker != nil && comment.Marker.Kind == kind && comment.Marker.Head == head {
			return true
		}
	}
	return false
}

// validLineage は系譜が live head から始まっているかを検証する。live head を含まない
// 系譜では「live head 系譜に verdict がある」を判定できず、承認済みでない head を
// 承認済みとみなす余地が生まれる。
func validLineage(pullRequest PullRequestObservation) bool {
	return pullRequest.Head != "" && len(pullRequest.HeadLineage) > 0 &&
		pullRequest.HeadLineage[0] == pullRequest.Head
}

// activeBranch は claim branch の観測を検証して返す。名前が claim 対象と違う観測は、
// 別 Issue の branch を Run の排他として扱わないために拒否する。
//
// branch head と Pull Request head の一致は要求しない。両者は別 request で観測され、
// GitHub は push 後の Pull Request head を非同期に更新するため、compare-and-push の
// 直後には正常運転でも不一致が観測される。ここを fail-closed にすると、自分の publish の
// たびに`ai-needs-human`が付く。外部 push による head 不一致の検出は、mutation 時の
// compare-and-push と merge 時の期待 head 照合が担う
// （docs/spec/05_design/02_workflow.md の Idempotency and recovery）。導出は Run の
// 記録面である Pull Request の head を live head として使う。
func activeBranch(branch *BranchObservation, config DeriveConfig) (*BranchObservation, error) {
	if branch == nil {
		return nil, nil
	}
	if branch.Name != IssueBranchName(config.Issue) || branch.Head == "" {
		return nil, errExternalMutation
	}
	return branch, nil
}

// isCandidate は routing の候補条件（open / target assignee / ready label）だけを返す。
// dependency の readiness は含まない。候補条件は「Kudo が扱う Issue か」であり、
// dependency は「今 claim してよいか」だからである。
func isCandidate(issue IssueObservation, config DeriveConfig) bool {
	return issue.State == IssueStateOpen &&
		containsIdentity(issue.Assignees, config.Assignee) &&
		containsIdentity(issue.Labels, config.ReadyLabel)
}

func deriveCandidate(issue IssueObservation, config DeriveConfig) Derivation {
	if !isCandidate(issue, config) {
		return Derivation{Phase: PhaseNone, Next: ReconcileAction{Kind: ReconcileSkipNotCandidate}}
	}
	for _, dependency := range issue.Dependencies {
		if !dependency.Closed {
			return Derivation{Phase: PhaseNone, Next: ReconcileAction{Kind: ReconcileAwaitDependency}}
		}
	}
	return Derivation{
		Phase: PhaseCandidate,
		Next:  ReconcileAction{Kind: ReconcileDispatchOperation, Operation: contract.OperationClaim},
	}
}

// deriveRun は open な Run の観測を Derived phases 表の順で評価する。
//
// 表に無い分岐は`request_changes`と`needs_human`の verdict だけであり、これは
// docs/spec/05_design/02_workflow.md の Test validity review / Final implementation review が
// 定める自動修正 loop である。表は verdict が approve の場合の phase 名だけを列挙して
// いるため、verdict を読まずに次の行へ落ちると差し戻しが観測から消える。
func deriveRun(observation Observation, config DeriveConfig, pullRequest PullRequestObservation) (Derivation, error) {
	head := pullRequest.Head
	checks := observation.CheckRuns

	finalVerdict, err := verdictOn(checks, config, CheckRunFinalImplementation, head)
	if err != nil {
		return Derivation{}, err
	}
	switch finalVerdict {
	case contract.VerdictApprove:
		// approve が live head にあるだけでは finalize / merge の根拠にしない。approve は
		// GREEN/check evidence と承認済み test の上にしか成立せず、それらを欠いた記録は
		// gate を二つ飛ばして merge まで到達しうる。直後の evidence 分岐だけを守っても、
		// approve 経路が迂回路として残る。
		if err := checkFinalApprovalPrerequisites(observation, config, pullRequest); err != nil {
			return Derivation{}, err
		}
		if pullRequest.State == PullRequestStateReady {
			return deriveMergeGate(pullRequest)
		}
		return dispatch(PhaseFinalizingPullRequest, contract.OperationFinalizePullRequest), nil
	case contract.VerdictRequestChanges:
		return dispatch(PhaseImplementing, contract.OperationRepairImplementation), nil
	case contract.VerdictNeedsHuman:
		return escalated(EscalationReviewNeedsHuman, PhaseAwaitingFinalReview), nil
	}

	if hasEvidence(checks, config, CheckRunEvidenceGreen, head) &&
		hasEvidence(checks, config, CheckRunEvidenceChecks, head) {
		// 承認済み test を系譜に持たない final evidence は、test gate を通さずに
		// 実装が進んだ記録である。final review へ回すと gate を一つ飛ばして
		// merge まで到達しうるため、記録側の protocol 違反として止める。
		//
		// 存在判定ではなく最新記録で見る。approve があったことだけを根拠にすると、
		// その後の request_changes や test-revision-report で一度閉じた gate を、
		// 古い approve で開き直せる（testGateApproved の doc comment を参照）。
		approved, approvedErr := testGateApproved(observation, config, pullRequest)
		if approvedErr != nil {
			return Derivation{}, approvedErr
		}
		if !approved {
			return Derivation{}, errInvalidRecord
		}
		return Derivation{
			Phase: PhaseAwaitingFinalReview,
			Next: ReconcileAction{
				Kind:   ReconcileRequestReview,
				Review: contract.ReviewFinalImplementation,
			},
		}, nil
	}

	// 承認済み test の変更が必要になった implement は、承認済み checkpoint へ rollback して
	// test-revision-report を記録する。rollback 先は承認済み test head そのものなので、
	// verdict を先に読むと approve を見つけて implement を再 dispatch し続けてしまう。
	// report の head binding は「その head の approval はもう実装の根拠にならない」を表す
	// （docs/spec/05_design/02_workflow.md の GREEN and refactor）。
	if hasMarker(observation, config.Implementer.CommentAuthorID,
		CommentMarkerTestRevisionReport, pullRequest.Number, head) {
		return dispatch(PhaseAuthoringTests, contract.OperationReviseTests), nil
	}

	testVerdict, err := verdictOn(checks, config, CheckRunTestValidity, head)
	if err != nil {
		return Derivation{}, err
	}
	switch testVerdict {
	case contract.VerdictApprove:
		operation, operationErr := implementOperation(checks, config, pullRequest)
		if operationErr != nil {
			return Derivation{}, operationErr
		}
		return dispatch(PhaseImplementing, operation), nil
	case contract.VerdictRequestChanges:
		return dispatch(PhaseAuthoringTests, contract.OperationReviseTests), nil
	case contract.VerdictNeedsHuman:
		return escalated(EscalationReviewNeedsHuman, PhaseAwaitingTestReview), nil
	}

	// live head に未 review の RED evidence がある間は、系譜上の approve を実装の
	// 根拠にしない。test を書き換えた head は新しい approval を得るまで implementation
	// へ戻れない（docs/spec/05_design/02_workflow.md の GREEN and refactor）。
	redOnHead := hasEvidence(checks, config, CheckRunEvidenceRed, head)
	if !redOnHead {
		approved, approvedErr := testGateApproved(observation, config, pullRequest)
		if approvedErr != nil {
			return Derivation{}, approvedErr
		}
		if approved {
			operation, operationErr := implementOperation(checks, config, pullRequest)
			if operationErr != nil {
				return Derivation{}, operationErr
			}
			return dispatch(PhaseImplementing, operation), nil
		}
	}
	if redOnHead {
		return Derivation{
			Phase: PhaseAwaitingTestReview,
			Next:  ReconcileAction{Kind: ReconcileRequestReview, Review: contract.ReviewTestValidity},
		}, nil
	}
	if pullRequest.State == PullRequestStateDraft {
		operation, operationErr := authorOperation(observation, config, pullRequest)
		if operationErr != nil {
			return Derivation{}, operationErr
		}
		return dispatch(PhaseAuthoringTests, operation), nil
	}
	// draft でない PR が final approve を持たない。ready 化は final approve だけが
	// gate であり、外形が合わない Run を進めない。
	return Derivation{}, errExternalMutation
}

// checkFinalApprovalPrerequisites は final approve が立つための証跡が揃っているかを確かめる。
//
// test gate は存在判定ではなく最新記録で見る。final approve は「承認済み test の上に
// 立った実装」への approve であり、その後 test が差し戻されていれば前提が崩れている
// （docs/spec/05_design/02_workflow.md の GREEN and refactor）。
func checkFinalApprovalPrerequisites(observation Observation, config DeriveConfig,
	pullRequest PullRequestObservation) error {
	head := pullRequest.Head
	if !hasEvidence(observation.CheckRuns, config, CheckRunEvidenceGreen, head) ||
		!hasEvidence(observation.CheckRuns, config, CheckRunEvidenceChecks, head) {
		return errInvalidRecord
	}
	approved, err := testGateApproved(observation, config, pullRequest)
	if err != nil {
		return err
	}
	if !approved {
		return errInvalidRecord
	}
	return nil
}

// deriveMergeGate は merge の外形条件を read-only の観測だけで評価する。
//
// review に merge 判断をさせず、外形条件を Controller が評価する
// （docs/spec/05_design/02_workflow.md の Merge と完了投影）。pending は品質 verdict でも
// execution failure でもない待機であり、check failure と conflict は`merge_blocked`として
// 人へ返す。語彙外の観測は、埋め忘れを「待機」へ倒して merge が黙って止まらないよう
// protocol 違反として拒否する。
func deriveMergeGate(pullRequest PullRequestObservation) (Derivation, error) {
	gate := pullRequest.MergeGate
	switch gate.RequiredChecks {
	case CheckRollupFailure:
		return escalated(EscalationMergeBlocked, PhaseMergingPullRequest), nil
	case CheckRollupPending:
		return Derivation{
			Phase: PhaseMergingPullRequest,
			Next:  ReconcileAction{Kind: ReconcileAwaitMergeGate},
		}, nil
	case CheckRollupSuccess:
	default:
		return Derivation{}, errInvalidRecord
	}
	switch gate.Mergeable {
	case MergeabilityConflicting:
		return escalated(EscalationMergeBlocked, PhaseMergingPullRequest), nil
	case MergeabilityComputing:
		return Derivation{
			Phase: PhaseMergingPullRequest,
			Next:  ReconcileAction{Kind: ReconcileAwaitMergeGate},
		}, nil
	case MergeabilityMergeable:
		return dispatch(PhaseMergingPullRequest, contract.OperationMergePullRequest), nil
	default:
		return Derivation{}, errInvalidRecord
	}
}

func dispatch(phase Phase, operation contract.OperationKind) Derivation {
	return Derivation{
		Phase: phase,
		Next:  ReconcileAction{Kind: ReconcileDispatchOperation, Operation: operation},
	}
}

// implementOperation は実装 lane の Operation を選ぶ。final review が一度でも
// blocking finding を返していれば、以後の実装はその finding の修復であり、
// 先行 artifact を入力に取る repair_implementation でなければならない。
func implementOperation(checks []CheckRunObservation, config DeriveConfig,
	pullRequest PullRequestObservation) (contract.OperationKind, error) {
	repairing, err := verdictInLineage(checks, config, CheckRunFinalImplementation,
		contract.VerdictRequestChanges, pullRequest.HeadLineage)
	if err != nil {
		return "", err
	}
	if repairing {
		return contract.OperationRepairImplementation, nil
	}
	return contract.OperationImplement, nil
}

// authorOperation は test lane の Operation を選ぶ。差し戻し（reviewer の
// request_changes、または implement lane の test-revision-report）が一度でもあれば、
// 以後は先行 artifact を入力に取る revise_tests である。
func authorOperation(observation Observation, config DeriveConfig,
	pullRequest PullRequestObservation) (contract.OperationKind, error) {
	revising, err := verdictInLineage(observation.CheckRuns, config, CheckRunTestValidity,
		contract.VerdictRequestChanges, pullRequest.HeadLineage)
	if err != nil {
		return "", err
	}
	if revising {
		return contract.OperationReviseTests, nil
	}
	for _, comment := range observation.Comments {
		if comment.AuthorID == config.Implementer.CommentAuthorID &&
			comment.PullRequest == pullRequest.Number && comment.Marker != nil &&
			comment.Marker.Kind == CommentMarkerTestRevisionReport {
			return contract.OperationReviseTests, nil
		}
	}
	return contract.OperationAuthorTests, nil
}

// verdictOn は head へ束縛された Reviewer 名義の verdict を返す。verdict が無ければ
// 空を返す。
//
// 同じ head に矛盾する verdict がある観測は error にする。どちらかを選ぶ規則を置くと、
// 記録の再作成や外部操作で gate の結論を選べてしまう。語彙外の verdict も同様に
// 解釈せず error にする。
func verdictOn(checks []CheckRunObservation, config DeriveConfig,
	name, head string) (contract.ReviewVerdict, error) {
	var found contract.ReviewVerdict
	for _, check := range checks {
		if check.Name != name || check.Head != head || check.AppID != config.Reviewer.CheckRunAppID {
			continue
		}
		switch check.Verdict {
		case contract.VerdictApprove, contract.VerdictRequestChanges, contract.VerdictNeedsHuman:
		default:
			return "", errInvalidRecord
		}
		if found != "" && found != check.Verdict {
			return "", errInvalidRecord
		}
		found = check.Verdict
	}
	return found, nil
}

// verdictInLineage は live head から辿れる commit に、指定 verdict の Reviewer 名義
// 記録があるかを返す。系譜外の head への verdict は現在の Run の gate 入力ではない。
//
// 各 head の verdict は verdictOn で読む。系譜上の記録も live head と同じ強さで検証しないと、
// 語彙外や矛盾した verdict が check run の並び順しだいで gate を通る。
func verdictInLineage(checks []CheckRunObservation, config DeriveConfig,
	name string, verdict contract.ReviewVerdict, lineage []string) (bool, error) {
	for _, commit := range lineage {
		found, err := verdictOn(checks, config, name, commit)
		if err != nil {
			return false, err
		}
		if found == verdict {
			return true, nil
		}
	}
	return false, nil
}

// testGateApproved は、系譜上でもっとも新しい test gate の記録が approve かを返す。
//
// approve の存在だけを見ると、その後の request_changes や test-revision-report で
// 一度閉じた gate を、古い approve を根拠に開き直せてしまう。差し戻し後に新しい
// test head を push した直後（RED evidence をまだ記録していない窓）で process が
// 落ちると、再観測が implement を再 dispatch して test gate を迂回する。
//
// 系譜は新しい順なので、index が小さいほど新しい記録である。同じ head に approve と
// 差し戻し report が並ぶ場合は report を新しいものとして扱う。implement は approve の
// 後にしか走らないためである。
func testGateApproved(observation Observation, config DeriveConfig,
	pullRequest PullRequestObservation) (bool, error) {
	verdictAt, verdict, err := latestVerdictInLineage(observation.CheckRuns, config,
		CheckRunTestValidity, pullRequest.HeadLineage)
	if err != nil {
		return false, err
	}
	if verdictAt < 0 || verdict != contract.VerdictApprove {
		return false, nil
	}
	revisionAt := latestRevisionReportInLineage(observation, config, pullRequest)
	return revisionAt < 0 || verdictAt < revisionAt, nil
}

// latestVerdictInLineage は系譜上でもっとも新しい Reviewer 名義 verdict の位置と値を返す。
// 記録が無ければ index に -1 を返す。
func latestVerdictInLineage(checks []CheckRunObservation, config DeriveConfig,
	name string, lineage []string) (int, contract.ReviewVerdict, error) {
	for index, commit := range lineage {
		verdict, err := verdictOn(checks, config, name, commit)
		if err != nil {
			return -1, "", err
		}
		if verdict != "" {
			return index, verdict, nil
		}
	}
	return -1, "", nil
}

// latestRevisionReportInLineage は系譜上でもっとも新しい test-revision-report の位置を返す。
func latestRevisionReportInLineage(observation Observation, config DeriveConfig,
	pullRequest PullRequestObservation) int {
	for index, commit := range pullRequest.HeadLineage {
		if hasMarker(observation, config.Implementer.CommentAuthorID,
			CommentMarkerTestRevisionReport, pullRequest.Number, commit) {
			return index
		}
	}
	return -1
}

// hasEvidence は head へ束縛された Implementer 名義の evidence があるかを返す。
// evidence は自分の実行証跡であり、他 actor 名義の同名 check run は証跡ではない。
func hasEvidence(checks []CheckRunObservation, config DeriveConfig, name, head string) bool {
	for _, check := range checks {
		if check.Name == name && check.Head == head &&
			check.AppID == config.Implementer.CheckRunAppID {
			return true
		}
	}
	return false
}
