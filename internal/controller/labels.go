package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// LabelEvent は Controller が GitHub label として記録する導出 event である。
//
// 先頭 4 値は docs/spec/05_design/04_github-routing.md の Transition rules の行に 1:1 で
// 対応する。残る 2 値は「同じ行を、その一度きりの副作用が済んだ後に再観測した」ことを
// 表し、同書の Transition rules に再観測の差分として明記してある。差分は 2 点だけである。
// merge 完了の再観測は Issue を close しない（close は初回記録の副作用）。needs human の
// 再観測は`ai-ready`を除去対象から外す（人間所有の resume trigger を消費しない）。
// 行を増やしているのではなく、行の再適用が人間の操作を押し戻さないように分けている。
//
// closed vocabulary にしているのは、値が増えたときに既定分岐が誤った label 操作を
// しないようにするためである。label は workflow state の正本ではなく、check run /
// comment / PR / branch の観測から導出した phase の記録である。
type LabelEvent string

const (
	// LabelEventClaimCompleted は Run が claim 済みで自動 workflow が進行中であることの記録である。
	LabelEventClaimCompleted LabelEvent = "claim_completed"
	// LabelEventRunNeedsHuman は人間の契約・authority・環境対応を待つ停止の記録である。
	LabelEventRunNeedsHuman LabelEvent = "run_needs_human"
	// LabelEventMergeCompleted は承認済み head が base へ入った外形事実の記録である。
	LabelEventMergeCompleted LabelEvent = "merge_completed"
	// LabelEventAlreadyMergedRequest は merged な kudo PR を持つ Issue への`ai-ready`
	// 再付与（reopen 後を含む）の記録である。新しい Run は始めない。
	LabelEventAlreadyMergedRequest LabelEvent = "already_merged_request"

	// LabelEventNeedsHumanRecorded は既に停止中の Run の再観測である。
	//
	// 停止（LabelEventRunNeedsHuman）と分けるのは、`ai-ready`が escalation 解除の
	// 唯一の trigger だからである。停止中の再観測で`ai-ready`を消すと、人間が契約を
	// 直して付け直した trigger を、resume 判定が走る前に Kudo 自身が消してしまう
	// （docs/spec/05_design/04_github-routing.md の Labels / Human escalation）。
	LabelEventNeedsHumanRecorded LabelEvent = "needs_human_recorded"
	// LabelEventMergeRecorded は完了記録済みの merge の再観測である。
	//
	// close を伴わないのは、Task Issue の close が merge completion を初めて記録した
	// 一度きりの副作用だからである。その後に Issue が open であることは人間の操作の
	// 結果であり、Kudo が押し戻す対象ではない。
	LabelEventMergeRecorded LabelEvent = "merge_recorded"
)

// AlreadyMergedCommentKind は already-merged 再依頼の案内 comment を識別する record kind である。
// 本文ではなくこの kind が comment の identity なので、文面を変えても重複を作らない。
const AlreadyMergedCommentKind = "already-merged-guidance"

// log の event 語彙。
const EventLabelRecord = "label.record"

// Outcome に加える label 記録の結果分類。
const (
	// OutcomeLabelConverged は label set が既に導出結果と一致していたことを表す。
	OutcomeLabelConverged Outcome = "label_converged"
	// OutcomeLabelRecorded はこの記録が label / close / comment を変えたことを表す。
	OutcomeLabelRecorded Outcome = "label_recorded"
)

// ErrUnknownLabelEvent は語彙外の label event を表す。
var ErrUnknownLabelEvent = errors.New("label event が語彙に無い")

// ErrIssueOutsideRepository は recorder が束縛していない repository の Issue を表す。
var ErrIssueOutsideRepository = errors.New("Issue が recorder の repository に属さない")

// LabelPolicy は label の記録と判定に必要な deployment 固定値である。
//
// label 名と記録者 identity を 1 つにまとめているのは、両方が揃って初めて
// 「この label は Kudo が記録したものか」を判断できるからである。名前だけでは、
// 人間が付けた同じ label と区別できない。
type LabelPolicy struct {
	Labels LabelSet
	// Recorder は Kudo 所有 label を記録する actor の identity である。
	// label event の作成者は GitHub user なので、CommentAuthorID（bot user）で照合する。
	Recorder workflow.ActorIdentity
}

// Validate は label 名と記録者 identity が揃っているかを検査する。
//
// identity を必須にするのは、欠けた場合に「誰の付与か分からない」と「Kudo の付与」が
// 同じ結果になり、人間が手で付けた`ai-merged`を完了記録として読んでしまうためである。
func (p LabelPolicy) Validate() error {
	if err := p.Labels.Validate(); err != nil {
		return err
	}
	if p.Recorder.CommentAuthorID <= 0 {
		return fmt.Errorf("label を記録する actor identity が確定していない")
	}
	return nil
}

// LabelSet は deployment が固定する 4 label の名前である。
//
// name を値として持つのは、`ai-ready`と assignee が configuration で上書き可能だからである
// （docs/spec/05_design/04_github-routing.md の Candidate selection）。deployment 内では
// 一意に固定する。
type LabelSet struct {
	Ready      string
	InProgress string
	NeedsHuman string
	Merged     string
}

// DefaultLabelSet は spec が定める既定の label 名を返す。
func DefaultLabelSet() LabelSet {
	return LabelSet{
		Ready:      workflow.LabelReady,
		InProgress: string(workflow.StatusInProgress),
		NeedsHuman: string(workflow.StatusNeedsHuman),
		Merged:     string(workflow.StatusMerged),
	}
}

// Validate は 4 label が揃い、互いに区別できることを検査する。
//
// GitHub の label name は case-insensitive に一致するため、大文字違いの重複も拒否する。
// 重複を許すと「追加してから削除する」収束が自分の追加を消し、記録が空になる。
func (s LabelSet) Validate() error {
	names := map[string]string{
		"ready":      s.Ready,
		"inProgress": s.InProgress,
		"needsHuman": s.NeedsHuman,
		"merged":     s.Merged,
	}
	seen := make(map[string]string, len(names))
	for _, field := range []string{"ready", "inProgress", "needsHuman", "merged"} {
		value := names[field]
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("label set の %s が空である", field)
		}
		// comma を拒むのは、Kudo 所有 label の列挙（回復経路の query）が label を
		// comma 区切りで渡すためである。値に comma を含めると列挙が必ず失敗し、
		// 起動時には通った設定が polling の恒久的な停止になる。
		if strings.Contains(value, ",") {
			return fmt.Errorf("label set の %s に comma は使えない: %q", field, value)
		}
		key := strings.ToLower(value)
		if other, exists := seen[key]; exists {
			return fmt.Errorf("label set の %s と %s が同じ label name である", other, field)
		}
		seen[key] = field
	}
	return nil
}

// LabelSurface は Controller が Issue 上の記録面を収束させる境界である。
//
// 実装は Coordinator identity の credential で構築した adapter に限る。Issue Worker や
// Review Worker の capability をここへ注入すると、記録の作成者が phase の導出者と
// 食い違い、監査で「誰が記録したか」を identity から確定できなくなる。
//
// すべての操作は現在値を確認してから mutate し、戻り値は「この呼び出しが変えたか」を返す。
// 既に収束している状態は成功であり、失敗ではない。
type LabelSurface interface {
	// ConvergeLabels は add を付け、remove を外した状態へ収束させる。
	//
	// 単一 label ごとの port にしないのは、1 回の記録が同じ label 一覧を 3〜4 回
	// 読み直すためである。polling は rate limit 予算の制約下にある回復経路であり、
	// 収束済みの Run を毎 cycle 記録するこの経路の読み取りは定常的な支出になる。
	ConvergeLabels(ctx context.Context, issue int64, add string, remove []string) (added bool, removed []string, err error)
	EnsureIssueClosed(ctx context.Context, issue int64) (bool, error)
	// EnsureIssueComment は kind で識別される Coordinator 名義の comment を、本文が
	// 違う場合だけ更新して収束させる。marker の形式は adapter の所有であり、
	// Controller は record kind と本文だけを決める。
	EnsureIssueComment(ctx context.Context, issue int64, kind, body string) (bool, error)
}

// LabelRecord は一度の記録で実際に起きた mutation である。
type LabelRecord struct {
	Event LabelEvent
	// Added はこの呼び出しが追加した label である。既に付いていた場合は空になる。
	Added string
	// Removed はこの呼び出しが外した label である。
	Removed []string
	// ClosedIssue はこの呼び出しが Task Issue を close したかを表す。
	ClosedIssue bool
	// CommentChanged はこの呼び出しが案内 comment を作成または更新したかを表す。
	CommentChanged bool
}

// Changed は GitHub 上の状態が実際に変わったかを返す。
func (r LabelRecord) Changed() bool {
	return r.Added != "" || len(r.Removed) > 0 || r.ClosedIssue || r.CommentChanged
}

// labelTransition は Transition rules の 1 行と、その一度きりの副作用である。
type labelTransition struct {
	add    func(LabelSet) string
	remove []func(LabelSet) string
	// closeIssue は merge completion を初めて記録するときだけ true になる。
	// 完了記録済みの再観測（LabelEventMergeRecorded）では false であり、人間が
	// reopen した Issue を Kudo が閉じ直さない。
	closeIssue bool
	// guidance は案内 comment を収束させるかを表す。
	guidance bool
}

var labelTransitions = map[LabelEvent]labelTransition{
	LabelEventClaimCompleted: {
		add:    inProgress,
		remove: []func(LabelSet) string{ready, needsHuman},
	},
	LabelEventRunNeedsHuman: {
		add:    needsHuman,
		remove: []func(LabelSet) string{ready, inProgress, merged},
	},
	// 停止中の再観測では`ai-ready`を外さない。人間が付け直した resume trigger を、
	// resume / supersede の判定が走る前に消費しないためである。
	LabelEventNeedsHumanRecorded: {
		add:    needsHuman,
		remove: []func(LabelSet) string{inProgress, merged},
	},
	LabelEventMergeCompleted: {
		add:        merged,
		remove:     []func(LabelSet) string{ready, inProgress, needsHuman},
		closeIssue: true,
	},
	LabelEventMergeRecorded: {
		add:    merged,
		remove: []func(LabelSet) string{ready, inProgress, needsHuman},
	},
	LabelEventAlreadyMergedRequest: {
		add:      merged,
		remove:   []func(LabelSet) string{ready},
		guidance: true,
	},
}

func ready(s LabelSet) string      { return s.Ready }
func inProgress(s LabelSet) string { return s.InProgress }
func needsHuman(s LabelSet) string { return s.NeedsHuman }
func merged(s LabelSet) string     { return s.Merged }

// LabelRecorder は導出 event を GitHub 上の label / Issue state / 案内 comment へ
// 冪等に記録する。
//
// 記録の失敗で導出 phase は巻き戻らない。失敗した記録は次の reconcile が同じ観測から
// 同じ event を導出して retry する（docs/spec/05_design/04_github-routing.md の Labels）。
type LabelRecorder struct {
	surface    LabelSurface
	repository contract.IssueRef
	labels     LabelSet
	logger     *slog.Logger
}

// NewLabelRecorder は記録面、対象 repository、label 名を束縛した recorder を返す。
//
// repository を束縛するのは、LabelSurface が Issue number しか受け取らないためである。
// 複数 repository を扱う composition で recorder を取り違えると、別 repository の
// 同じ番号の Issue を Coordinator 名義で mutate できてしまう。identity の照合を
// 呼び出し側の注意深さに委ねない。
//
// owner / repository は GitHub の case-insensitive な identity に合わせて小文字で
// 比較する。logger が nil のときは slog.Default を使う。
func NewLabelRecorder(surface LabelSurface, repository contract.IssueRef, policy LabelPolicy,
	logger *slog.Logger) (*LabelRecorder, error) {
	if surface == nil {
		return nil, errors.New("LabelSurface は必須")
	}
	if strings.TrimSpace(repository.Owner) == "" || strings.TrimSpace(repository.Repository) == "" {
		return nil, errors.New("recorder の repository identity は必須")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LabelRecorder{
		surface:    surface,
		repository: contract.IssueRef{Owner: strings.ToLower(repository.Owner), Repository: strings.ToLower(repository.Repository)},
		labels:     policy.Labels,
		logger:     logger,
	}, nil
}

// Record は event に対応する label set へ収束させる。
//
// 収束は「追加してから、置き換えられる label を外す」順で行う。逆順にすると、記録が
// 中断した瞬間に「Kudo の status を持たない Issue」が観測でき、人間にも polling にも
// 「Kudo が手を付けていない」と読める中間状態が生まれる。
//
// Issue の close と案内 comment はこの収束より前に行う。どちらも「記録を完成させる」
// 副作用であり、それが済む前に`ai-ready`や`ai-in-progress`を外すと、失敗した時点の
// Issue が polling のどの列挙にも現れなくなる。close については、失敗時に`ai-merged`が
// まだ付いていないことが「完了は未記録」という次の reconcile の判断材料にもなる。
//
// 途中で失敗した場合はそこで止めて error を返す。残りの mutation を続けないのは、
// transport failure が続いていれば同じ失敗を繰り返すだけであり、次の reconcile が
// 現在値の確認からやり直す方が収束が速いためである。
func (r *LabelRecorder) Record(ctx context.Context, issue contract.IssueRef, event LabelEvent) (LabelRecord, error) {
	transition, known := labelTransitions[event]
	if !known {
		return LabelRecord{}, fmt.Errorf("%w: %q", ErrUnknownLabelEvent, event)
	}
	if issue.Owner == "" || issue.Repository == "" || issue.Number <= 0 {
		return LabelRecord{}, fmt.Errorf("%w: label 記録の Issue identity が欠落している",
			workflow.ErrInvalidReconcileRequest)
	}
	if !strings.EqualFold(issue.Owner, r.repository.Owner) ||
		!strings.EqualFold(issue.Repository, r.repository.Repository) {
		return LabelRecord{}, fmt.Errorf("%w: %s は %s/%s の recorder では記録できない",
			ErrIssueOutsideRepository, issue.String(), r.repository.Owner, r.repository.Repository)
	}
	number := int64(issue.Number)
	record := LabelRecord{Event: event}

	// 記録を完成させる副作用を、再発見の手掛かりになる label を消費する前に済ませる。
	// 逆順にすると、close や案内 comment が失敗した時点で ai-ready も ai-in-progress も
	// 無い Issue が残り、polling のどの列挙にも現れなくなる。
	if transition.closeIssue {
		closed, err := r.surface.EnsureIssueClosed(ctx, number)
		if err != nil {
			return record, err
		}
		record.ClosedIssue = closed
	}
	if transition.guidance {
		changed, err := r.surface.EnsureIssueComment(ctx, number,
			AlreadyMergedCommentKind, alreadyMergedGuidance(r.labels))
		if err != nil {
			return record, err
		}
		record.CommentChanged = changed
	}
	removals := make([]string, 0, len(transition.remove))
	for _, resolve := range transition.remove {
		removals = append(removals, resolve(r.labels))
	}
	added, removed, err := r.surface.ConvergeLabels(ctx, number, transition.add(r.labels), removals)
	// 途中失敗でも、そこまでに起きた mutation は record に残す。捨てると呼び出し側の
	// 記録が「何も変えていない」と読め、失敗した reconcile の後に GitHub 上で何が
	// 変わっているかを record から追えなくなる。
	if added {
		record.Added = transition.add(r.labels)
	}
	record.Removed = removed
	if err != nil {
		return record, err
	}
	r.logRecord(ctx, issue, record)
	return record, nil
}

func (r *LabelRecorder) logRecord(ctx context.Context, issue contract.IssueRef, record LabelRecord) {
	outcome := OutcomeLabelConverged
	level := slog.LevelDebug
	if record.Changed() {
		outcome = OutcomeLabelRecorded
		level = slog.LevelInfo
	}
	r.logger.LogAttrs(ctx, level, "導出 phase を label へ記録した",
		slog.String(telemetry.FieldEvent, EventLabelRecord),
		slog.String(telemetry.FieldOutcome, string(outcome)),
		slog.String(telemetry.FieldLabelEvent, string(record.Event)),
		telemetry.Issue(issue),
	)
}

// alreadyMergedGuidance は再依頼を受け付けない理由と、人間が取れる選択肢を伝える。
//
// Issue 本文や token を含めないのは、この comment が public な記録面だからである。
// Issue ごとに変わる値を入れていないのは意図であり、comment の identity は record kind
// なので、文面を変えても既存 comment の更新になる。
func alreadyMergedGuidance(labels LabelSet) string {
	var builder strings.Builder
	builder.WriteString("この Issue には merge 済みの kudo Pull Request が既に存在するため、")
	builder.WriteString("`" + labels.Ready + "` の再付与では新しい実行を開始しません。\n\n")
	builder.WriteString("- 検出結果: `skipped_already_merged`\n")
	builder.WriteString("- 記録した label: `" + labels.Merged + "`（`" + labels.Ready + "` は外しました）\n\n")
	builder.WriteString("同じ変更をやり直す場合は、新しい Task Issue を作成してください。")
	builder.WriteString("再実装、cancel、revert、merge 後の Pull Request review comment への対応は、")
	builder.WriteString("この workflow へ versioned command を追加する別の decision まで人間が扱います。\n")
	return builder.String()
}

// DeriveLabelEvent は導出結果と live Issue 観測から、記録すべき label event を決める。
//
// pure である。label は導出 phase の記録なので、この写像だけが「どの label を書くか」を
// 決め、GitHub 呼び出し側に分岐を作らない。
//
// merge 完了と already-merged 再依頼を、この写像自身が書き換える`ai-ready`で区別しない。
// 前者で分けると、記録した直後の観測で分岐が反転し、人間が reopen した Issue を次の
// reconcile が close で押し戻す。
//
// 「Kudo が完了を既に記録したか」の判定に現在の`ai-merged`だけを使わないのは、Kudo 所有
// でも人間が外せる label だからである。docs/spec/05_design/04_github-routing.md は
// 「label を手で外しても merged な kudo PR という観測が正本であり、再依頼は
// `skipped_already_merged`として処理される」と定めている。label 付与の timeline
// （IssueObservation.LabelEvents）は現在値が消えても残るため、現在値と履歴の両方を見る。
//
// `ai-ready`の有無は案内 comment を収束させるかだけを決める。Issue の open / closed は
// 見ない。GitHub は closed な Issue にも label を付けられるため、reopen を伴わない再依頼も
// 起こり得る（AC-4 は Issue state を条件にしていない）。close は冪等なので、状態で分岐
// させる理由が無い。
//
// label 名の比較は case-insensitive である。GitHub 上の label identity が case-insensitive
// であり、記録側（ConvergeLabels）と導出側（internal/workflow の containsIdentity）が
// 同じ規則で照合するためである。
//
// 第 2 返り値が false になるのは、この写像が記録すべき label を持たない観測である。
// 具体的には claim 前の候補 / 候補外（Kudo の記録対象ではない）と、PhaseSuperseded
// （spec の Transition rules に対応する行が無い）である。後者は「進行中でない Run に
// `ai-in-progress`が残る」状態を作るが、supersede に対する label 行の追加は
// Issue #19 の Decision Authority では扱えない（label 体系の変更）ため、別 Issue の
// 判断に委ねている。
func DeriveLabelEvent(derivation workflow.Derivation, issue workflow.IssueObservation,
	policy LabelPolicy) (LabelEvent, bool) {
	switch derivation.Next.Kind {
	case workflow.ReconcileRecordCompletion:
		recordedAt, recorded := completionRecordedAt(issue, policy)
		if !recorded {
			return LabelEventMergeCompleted, true
		}
		if hasLabel(issue.Labels, policy.Labels.Ready) && !readyLabelIsMergeResidue(issue, policy, recordedAt) {
			return LabelEventAlreadyMergedRequest, true
		}
		return LabelEventMergeRecorded, true
	case workflow.ReconcileEscalateHuman:
		// deployment 全体の設定不備は Issue 固有の瑕疵ではない。観測ではなく process 設定が
		// 原因なので、reconcile された全 Issue が同じ結果になる。label へ記録すると 1 cycle で
		// repository 中の候補から人間所有の`ai-ready`が全部外れ、設定を直しても Issue ごとに
		// 付け直すまで一件も再開しない。docs/spec/05_design/04_github-routing.md は
		// 「dependency 待ち、capacity 待ち、一時 transport failure では`ai-ready`を消費しない」
		// と定めており、Issue 側に瑕疵の無い停止で人間の trigger を消費しない原則を示す。
		// 運用者への通知は telemetry と readiness が担う（接続は #24）。
		if derivation.Next.Reason == workflow.EscalationExternalConfigurationRequired {
			return "", false
		}
		return LabelEventRunNeedsHuman, true
	case workflow.ReconcileAwaitHuman:
		return LabelEventNeedsHumanRecorded, true
	}
	if activeRunPhase(derivation.Phase) {
		return LabelEventClaimCompleted, true
	}
	return "", false
}

// completionRecordedAt は Kudo が merge completion を初めて記録した時刻を返す。
//
// 判断の材料は「Kudo 名義の`ai-merged`付与が一度でもあったか」だけである。現在値を
// 見ないのは、`ai-merged`が Kudo 所有でも人間に外せる label だからである
// （docs/spec/05_design/04_github-routing.md の Transition rules）。付与履歴は現在値が
// 消えても残る。
//
// actor を照合するのは、人間が merge 前に手で`ai-merged`を付けた場合に、その付与を
// 「Kudo の完了記録」として読まないためである。読んでしまうと、正規の merge で
// Task Issue が close されないまま open で残る。記録の作成者を identity で確かめる
// のは、comment や PR body と同じ理由である（AGENTS.md の Architecture boundaries）。
//
// 返す時刻は「完了を初めて記録した」付与である。以後の再記録ではなく初回を基準にするのは、
// 再依頼の判定が「完了記録より後に人間が`ai-ready`を付けたか」だからである。最後の再記録を
// 基準にすると、その直前に付いた正当な再依頼が残骸へ誤分類される。
func completionRecordedAt(issue workflow.IssueObservation, policy LabelPolicy) (time.Time, bool) {
	var first time.Time
	found := false
	for _, event := range issue.LabelEvents {
		if !event.Added || event.ActorID != policy.Recorder.CommentAuthorID ||
			!strings.EqualFold(event.Label, policy.Labels.Merged) {
			continue
		}
		if !found || event.OccurredAt.Before(first) {
			first = event.OccurredAt
			found = true
		}
	}
	return first, found
}

// readyLabelIsMergeResidue は、現在残っている`ai-ready`が完了記録より前から付いていた
// 残骸だと観測から確定できるかを返す。
//
// 完了の記録は`ai-merged`の付与に成功してから`ai-ready`の削除に失敗し得る。残った
// `ai-ready`を人間の再依頼として読むと、再依頼が無いのに「新しい実行を開始しません」と
// いう案内 comment が Issue へ永続的に残る（comment は record kind 単位の収束なので
// 後から消えない）。label set 自体はどちらの分岐でも`ai-merged`単独へ収束するため、
// 食い違うのは記録の内容だけである。
//
// 残骸だと**確定できる**場合にだけ true を返す。付与 event が観測できない場合は
// 従来どおり再依頼として扱う。案内の欠落は次の再付与で回復するが、誤った案内は残る。
// 同時刻を残骸に含めるのも同じ向きの判断である（GitHub の timestamp は秒解像度であり、
// merge 記録と同じ秒に付いた`ai-ready`はどちらとも決められない）。
func readyLabelIsMergeResidue(issue workflow.IssueObservation, policy LabelPolicy,
	recordedAt time.Time) bool {
	latest, found := latestLabelAdd(issue.LabelEvents, policy.Labels.Ready)
	return found && !latest.After(recordedAt)
}

func latestLabelAdd(events []workflow.LabelEventObservation, label string) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, event := range events {
		if !event.Added || !strings.EqualFold(event.Label, label) {
			continue
		}
		if !found || event.OccurredAt.After(latest) {
			latest = event.OccurredAt
			found = true
		}
	}
	return latest, found
}

func hasLabel(labels []string, target string) bool {
	return slices.ContainsFunc(labels, func(label string) bool {
		return strings.EqualFold(label, target)
	})
}

// activeRunPhase は claim 済みで自動 workflow が進行中の phase かを返す。
//
// Phase.Active を使わないのは、その述語が durable 語彙に無い PhaseNone / PhaseCandidate
// に対して意味を持たず、claim 前の候補まで「進行中」に含めてしまうためである。
// claim 前に`ai-in-progress`を記録すると、claim が成立しなかった Issue が実行中として
// 表示され、`ai-ready`も外れて人間の trigger が失われる。
//
// PhaseClaimed を除くのは同じ理由である。導出 model の`claimed`は「branch はあるが
// Pull Request がまだ無い」＝ claim 続行中または中断であり、durable model の同名 phase
// （claim が確定した段階）とは意味が違う。docs/spec/05_design/04_github-routing.md の
// Result 表は`claimed`を「branch と draft PR を確定した」と定義し、Transition rules の
// 「claim 完了」行はその状態にだけ対応する。記録面が成立する前に`ai-ready`を消費すると、
// claim が transport failure を繰り返す間も人間の trigger が戻らない（同書は
// 「一時 transport failure では`ai-ready`を消費しない」と定める）。branch だけが残る
// Issue は`ai-ready`を保ったまま候補 query で再発見され、claim が完了した時点で
// Transition rules どおりに trigger が消費される。
func activeRunPhase(phase workflow.Phase) bool {
	return phase != workflow.PhaseClaimed &&
		slices.Contains(workflow.Phases(), phase) && phase.Active()
}
