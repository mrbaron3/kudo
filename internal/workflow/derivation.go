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
}

// Derivation は観測から導出した現在 phase と次 action である。
type Derivation struct {
	Phase Phase
	Next  ReconcileAction
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
	// Implementer と Reviewer は同じ値でもよい。App 分離前の立ち上がり期間は単一
	// identity で動かすことを ADR-0001 が許容しており、その間は自己承認の禁止が
	// 内部規律へ退化する。identity が揃っていること自体は検証する。
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
	return nil
}

// errUnmappedObservation は Derived phases 表のどの行にも安全に一致しない観測である。
// 導出内部の制御にだけ使い、外へは needs_human の Derivation として出る。
var errUnmappedObservation = errors.New("観測が Derived phases 表のどの行にも一致しない")

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
		return escalated(EscalationExternalConfigurationRequired)
	}
	derivation, err := derive(observation, config)
	if err != nil {
		// 未知の観測は「継続できない外部干渉」として人へ返す。理由 code の語彙は
		// docs/spec/05_design/04_github-routing.md が正本であり、ここで増やさない。
		return escalated(EscalationExternalMutationConflict)
	}
	return derivation
}

func escalated(reason EscalationReason) Derivation {
	return Derivation{
		Phase: PhaseNeedsHuman,
		Next:  ReconcileAction{Kind: ReconcileEscalateHuman, Reason: reason},
	}
}

func derive(observation Observation, config DeriveConfig) (Derivation, error) {
	if slices.Contains(observation.Issue.Labels, config.NeedsHumanLabel) {
		return Derivation{Phase: PhaseNeedsHuman, Next: ReconcileAction{Kind: ReconcileAwaitHuman}}, nil
	}

	active, closed, err := classifyPullRequests(observation.PullRequests)
	if err != nil {
		return Derivation{}, err
	}
	switch {
	case closed == pullRequestLineageMerged:
		return Derivation{Phase: PhaseMerged, Next: ReconcileAction{Kind: ReconcileRecordCompletion}}, nil
	case active == nil && closed == pullRequestLineageClosed:
		return Derivation{Phase: PhaseSuperseded, Next: ReconcileAction{Kind: ReconcileNone}}, nil
	}

	branch, err := activeBranch(observation.Branch, config)
	if err != nil {
		return Derivation{}, err
	}
	// merge も supersede もしていない Run の途中で Issue が閉じられるのは外部干渉で
	// あり、安全な checkpoint で止める（docs/spec/05_design/04_github-routing.md）。
	if observation.Issue.State != IssueStateOpen && (branch != nil || active != nil) {
		return Derivation{}, errUnmappedObservation
	}

	switch {
	case active != nil:
		if branch == nil {
			// PR の記録面だけが残り、compare-and-push の対象が消えている。
			return Derivation{}, errUnmappedObservation
		}
		if !validLineage(*active) {
			return Derivation{}, errUnmappedObservation
		}
		return deriveRun(observation, config, *active)
	case branch != nil:
		return Derivation{
			Phase: PhaseClaimed,
			Next:  ReconcileAction{Kind: ReconcileDispatchOperation, Operation: contract.OperationClaim},
		}, nil
	default:
		return deriveCandidate(observation.Issue, config), nil
	}
}

// pullRequestLineage は Run の記録面が終端に達したかの分類である。
type pullRequestLineage int

const (
	pullRequestLineageNone pullRequestLineage = iota
	pullRequestLineageClosed
	pullRequestLineageMerged
)

// classifyPullRequests は open な Run の PR と、終端した lineage を切り分ける。
//
// open な PR が複数ある観測は排他が壊れている（1 IssueRef の active Run は最大一つ）。
// どちらを Run とみなしても片方の記録が捨てられるため、選ばずに fail-closed へ倒す。
func classifyPullRequests(pullRequests []PullRequestObservation) (*PullRequestObservation, pullRequestLineage, error) {
	var active *PullRequestObservation
	lineage := pullRequestLineageNone
	for index, pullRequest := range pullRequests {
		switch pullRequest.State {
		case PullRequestStateMerged:
			lineage = pullRequestLineageMerged
		case PullRequestStateClosed:
			if lineage != pullRequestLineageMerged {
				lineage = pullRequestLineageClosed
			}
		case PullRequestStateDraft, PullRequestStateReady:
			if active != nil {
				return nil, lineage, errUnmappedObservation
			}
			active = &pullRequests[index]
		default:
			return nil, lineage, errUnmappedObservation
		}
	}
	return active, lineage, nil
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
func activeBranch(branch *BranchObservation, config DeriveConfig) (*BranchObservation, error) {
	if branch == nil {
		return nil, nil
	}
	if branch.Name != IssueBranchName(config.Issue) || branch.Head == "" {
		return nil, errUnmappedObservation
	}
	return branch, nil
}

func deriveCandidate(issue IssueObservation, config DeriveConfig) Derivation {
	candidate := issue.State == IssueStateOpen &&
		slices.Contains(issue.Assignees, config.Assignee) &&
		slices.Contains(issue.Labels, config.ReadyLabel)
	if !candidate {
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
		if pullRequest.State == PullRequestStateReady {
			return dispatch(PhaseMergingPullRequest, contract.OperationMergePullRequest), nil
		}
		return dispatch(PhaseFinalizingPullRequest, contract.OperationFinalizePullRequest), nil
	case contract.VerdictRequestChanges:
		return dispatch(PhaseImplementing, contract.OperationRepairImplementation), nil
	case contract.VerdictNeedsHuman:
		return escalated(EscalationReviewNeedsHuman), nil
	}

	if hasEvidence(checks, config, CheckRunEvidenceGreen, head) &&
		hasEvidence(checks, config, CheckRunEvidenceChecks, head) {
		return Derivation{
			Phase: PhaseAwaitingFinalReview,
			Next: ReconcileAction{
				Kind:   ReconcileRequestReview,
				Review: contract.ReviewFinalImplementation,
			},
		}, nil
	}

	testVerdict, err := verdictOn(checks, config, CheckRunTestValidity, head)
	if err != nil {
		return Derivation{}, err
	}
	switch testVerdict {
	case contract.VerdictApprove:
		return dispatch(PhaseImplementing, implementOperation(checks, config, pullRequest)), nil
	case contract.VerdictRequestChanges:
		return dispatch(PhaseAuthoringTests, contract.OperationReviseTests), nil
	case contract.VerdictNeedsHuman:
		return escalated(EscalationReviewNeedsHuman), nil
	}

	// live head に未 review の RED evidence がある間は、系譜上の approve を実装の
	// 根拠にしない。test を書き換えた head は新しい approval を得るまで implementation
	// へ戻れない（docs/spec/05_design/02_workflow.md の GREEN and refactor）。
	redOnHead := hasEvidence(checks, config, CheckRunEvidenceRed, head)
	if !redOnHead && verdictInLineage(checks, config, CheckRunTestValidity,
		contract.VerdictApprove, pullRequest.HeadLineage) {
		return dispatch(PhaseImplementing, implementOperation(checks, config, pullRequest)), nil
	}
	if redOnHead {
		return Derivation{
			Phase: PhaseAwaitingTestReview,
			Next:  ReconcileAction{Kind: ReconcileRequestReview, Review: contract.ReviewTestValidity},
		}, nil
	}
	if pullRequest.State == PullRequestStateDraft {
		return dispatch(PhaseAuthoringTests, authorOperation(observation, config, pullRequest)), nil
	}
	// draft でない PR が final approve を持たない。ready 化は final approve だけが
	// gate であり、外形が合わない Run を進めない。
	return Derivation{}, errUnmappedObservation
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
	pullRequest PullRequestObservation) contract.OperationKind {
	if verdictInLineage(checks, config, CheckRunFinalImplementation,
		contract.VerdictRequestChanges, pullRequest.HeadLineage) {
		return contract.OperationRepairImplementation
	}
	return contract.OperationImplement
}

// authorOperation は test lane の Operation を選ぶ。差し戻し（reviewer の
// request_changes、または implement lane の test-revision-report）が一度でもあれば、
// 以後は先行 artifact を入力に取る revise_tests である。
func authorOperation(observation Observation, config DeriveConfig,
	pullRequest PullRequestObservation) contract.OperationKind {
	if verdictInLineage(observation.CheckRuns, config, CheckRunTestValidity,
		contract.VerdictRequestChanges, pullRequest.HeadLineage) {
		return contract.OperationReviseTests
	}
	for _, comment := range observation.Comments {
		if comment.AuthorID == config.Implementer.CommentAuthorID && comment.Marker != nil &&
			comment.Marker.Kind == CommentMarkerTestRevisionReport {
			return contract.OperationReviseTests
		}
	}
	return contract.OperationAuthorTests
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
			return "", errUnmappedObservation
		}
		if found != "" && found != check.Verdict {
			return "", errUnmappedObservation
		}
		found = check.Verdict
	}
	return found, nil
}

// verdictInLineage は live head から辿れる commit に、指定 verdict の Reviewer 名義
// 記録があるかを返す。系譜外の head への verdict は現在の Run の gate 入力ではない。
func verdictInLineage(checks []CheckRunObservation, config DeriveConfig,
	name string, verdict contract.ReviewVerdict, lineage []string) bool {
	for _, check := range checks {
		if check.Name == name && check.AppID == config.Reviewer.CheckRunAppID &&
			check.Verdict == verdict && slices.Contains(lineage, check.Head) {
			return true
		}
	}
	return false
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
