package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// LabelEvent は Controller が GitHub label として記録する導出 event である。
//
// 値は docs/spec/05_design/04_github-routing.md の Transition rules の行に 1:1 で対応する。
// closed vocabulary にしているのは、行が増えたときに既定分岐が誤った label 操作を
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
	EnsureLabel(ctx context.Context, issue int64, label string) (bool, error)
	RemoveLabel(ctx context.Context, issue int64, label string) (bool, error)
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

// labelTransition は Transition rules の 1 行である。
type labelTransition struct {
	add    func(LabelSet) string
	remove []func(LabelSet) string
	// closeIssue は merge completion だけが持つ副作用である。already-merged 再依頼で
	// close しないのは、reopen が人間の操作であり、Kudo が押し戻す対象ではないためである。
	closeIssue bool
	// guidance は案内 comment を収束させるかを表す。
	guidance bool
}

var labelTransitions = map[LabelEvent]labelTransition{
	LabelEventClaimCompleted: {
		add:    func(s LabelSet) string { return s.InProgress },
		remove: []func(LabelSet) string{ready, needsHuman},
	},
	LabelEventRunNeedsHuman: {
		add:    func(s LabelSet) string { return s.NeedsHuman },
		remove: []func(LabelSet) string{ready, inProgress, merged},
	},
	LabelEventMergeCompleted: {
		add:        func(s LabelSet) string { return s.Merged },
		remove:     []func(LabelSet) string{ready, inProgress, needsHuman},
		closeIssue: true,
	},
	LabelEventAlreadyMergedRequest: {
		add:      func(s LabelSet) string { return s.Merged },
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
	surface LabelSurface
	labels  LabelSet
	logger  *slog.Logger
}

// NewLabelRecorder は記録面と label 名を束縛した recorder を返す。
// logger が nil のときは slog.Default を使う。
func NewLabelRecorder(surface LabelSurface, labels LabelSet, logger *slog.Logger) (*LabelRecorder, error) {
	if surface == nil {
		return nil, errors.New("LabelSurface は必須")
	}
	if err := labels.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LabelRecorder{surface: surface, labels: labels, logger: logger}, nil
}

// Record は event に対応する label set へ収束させる。
//
// 追加を先に行い、置き換えられる label をその後で外す。逆順にすると、記録が中断した
// 瞬間に「Kudo の status を持たない Issue」が観測でき、人間にも polling にも
// 「Kudo が手を付けていない」と読める中間状態が生まれる。
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
	number := int64(issue.Number)
	record := LabelRecord{Event: event}

	added, err := r.surface.EnsureLabel(ctx, number, transition.add(r.labels))
	if err != nil {
		return record, err
	}
	if added {
		record.Added = transition.add(r.labels)
	}
	for _, resolve := range transition.remove {
		label := resolve(r.labels)
		removed, removeErr := r.surface.RemoveLabel(ctx, number, label)
		if removeErr != nil {
			return record, removeErr
		}
		if removed {
			record.Removed = append(record.Removed, label)
		}
	}
	if transition.closeIssue {
		closed, closeErr := r.surface.EnsureIssueClosed(ctx, number)
		if closeErr != nil {
			return record, closeErr
		}
		record.ClosedIssue = closed
	}
	if transition.guidance {
		changed, commentErr := r.surface.EnsureIssueComment(ctx, number,
			AlreadyMergedCommentKind, alreadyMergedGuidance(issue, r.labels))
		if commentErr != nil {
			return record, commentErr
		}
		record.CommentChanged = changed
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
// 文面は record kind で識別されるため、変更しても既存 comment の更新になる。
func alreadyMergedGuidance(issue contract.IssueRef, labels LabelSet) string {
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
// merge 完了と already-merged 再依頼を分けるのは、後者が「人間が reopen して
// `ai-ready`を付け直した」観測だからである。前者は Issue を close するが、後者は
// 人間の reopen を押し戻さず、案内 comment だけを収束させる。
func DeriveLabelEvent(derivation workflow.Derivation, issue workflow.IssueObservation,
	labels LabelSet) (LabelEvent, bool) {
	switch derivation.Next.Kind {
	case workflow.ReconcileRecordCompletion:
		if issue.State == workflow.IssueStateOpen && slices.Contains(issue.Labels, labels.Ready) {
			return LabelEventAlreadyMergedRequest, true
		}
		return LabelEventMergeCompleted, true
	case workflow.ReconcileEscalateHuman, workflow.ReconcileAwaitHuman:
		return LabelEventRunNeedsHuman, true
	}
	if activeRunPhase(derivation.Phase) {
		return LabelEventClaimCompleted, true
	}
	return "", false
}

// activeRunPhase は claim 済みで自動 workflow が進行中の phase かを返す。
//
// Phase.Active を使わないのは、その述語が durable 語彙に無い PhaseNone / PhaseCandidate
// に対して意味を持たず、claim 前の候補まで「進行中」に含めてしまうためである。
// claim 前に`ai-in-progress`を記録すると、claim が成立しなかった Issue が実行中として
// 表示され、`ai-ready`も外れて人間の trigger が失われる。
func activeRunPhase(phase workflow.Phase) bool {
	return slices.Contains(workflow.Phases(), phase) && phase.Active()
}
