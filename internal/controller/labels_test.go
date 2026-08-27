package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// fakeLabelSurface は GitHub 上の label / comment / Issue state を、adapter と同じ
// 「現在値を確認してから mutate する」性質で模す。
type fakeLabelSurface struct {
	mu     sync.Mutex
	labels []string
	// events は GitHub 上の label timeline を模す。現在値が消えても履歴は残る。
	events   []workflow.LabelEventObservation
	closed   bool
	comments map[string]string
	calls    []string
	failOn   map[string]error
}

func newFakeLabelSurface(labels ...string) *fakeLabelSurface {
	return &fakeLabelSurface{
		labels:   slices.Clone(labels),
		comments: map[string]string{},
		failOn:   map[string]error{},
	}
}

func (f *fakeLabelSurface) record(call string) error {
	f.calls = append(f.calls, call)
	if err, exists := f.failOn[call]; exists {
		return err
	}
	return nil
}

func (f *fakeLabelSurface) ConvergeLabels(_ context.Context, issue int64, add string,
	remove []string) (bool, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("converge_labels:%d:%s", issue, add)); err != nil {
		return false, nil, err
	}
	added := false
	if !slices.Contains(f.labels, add) {
		f.labels = append(f.labels, add)
		f.events = append(f.events, workflow.LabelEventObservation{
			Label: add, Added: true, ActorID: coordinatorActorID,
		})
		added = true
	}
	var removed []string
	for _, label := range remove {
		if err := f.record(fmt.Sprintf("remove_label:%d:%s", issue, label)); err != nil {
			return added, removed, err
		}
		index := slices.Index(f.labels, label)
		if index < 0 {
			continue
		}
		f.labels = slices.Delete(f.labels, index, index+1)
		f.events = append(f.events, workflow.LabelEventObservation{Label: label, ActorID: coordinatorActorID})
		removed = append(removed, label)
	}
	return added, removed, nil
}

func (f *fakeLabelSurface) EnsureIssueClosed(_ context.Context, issue int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("close_issue:%d", issue)); err != nil {
		return false, err
	}
	if f.closed {
		return false, nil
	}
	f.closed = true
	return true, nil
}

func (f *fakeLabelSurface) EnsureIssueComment(_ context.Context, issue int64, kind, body string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("ensure_comment:%d:%s", issue, kind)); err != nil {
		return false, err
	}
	key := fmt.Sprintf("%d:%s", issue, kind)
	if f.comments[key] == body {
		return false, nil
	}
	f.comments[key] = body
	return true, nil
}

// removeByHuman は人間が GitHub UI で label を外した状態を作る。
func (f *fakeLabelSurface) removeByHuman(label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index := slices.Index(f.labels, label); index >= 0 {
		f.labels = slices.Delete(f.labels, index, index+1)
	}
	f.events = append(f.events, workflow.LabelEventObservation{Label: label, ActorID: humanActorID})
}

// observe は現在の label set と timeline から Issue の観測を組み立てる。
func (f *fakeLabelSurface) observe(state workflow.IssueState) workflow.IssueObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return workflow.IssueObservation{
		Number:      19,
		State:       state,
		Labels:      slices.Clone(f.labels),
		LabelEvents: slices.Clone(f.events),
	}
}

func (f *fakeLabelSurface) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.labels)
}

func (f *fakeLabelSurface) closedIssue() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeLabelSurface) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}
	return count
}

func testIssueRef() contract.IssueRef {
	return contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 19}
}

// testLabelPolicy は既定 label と Coordinator identity を束ねた policy である。
func testLabelPolicy() LabelPolicy {
	return LabelPolicy{
		Labels:   DefaultLabelSet(),
		Recorder: workflow.ActorIdentity{CommentAuthorID: coordinatorActorID, CheckRunAppID: 202},
	}
}

const (
	coordinatorActorID = 101
	humanActorID       = 7
)

func newTestLabelRecorder(t *testing.T, surface LabelSurface) *LabelRecorder {
	t.Helper()
	recorder, err := NewLabelRecorder(surface, testIssueRef(), testLabelPolicy(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewLabelRecorder() error = %v", err)
	}
	return recorder
}

// docs/spec/05_design/04_github-routing.md の Transition rules をそのまま固定する。
func TestRecordLabelEventConvergesToTheDeclaredLabelSet(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		event   LabelEvent
		initial []string
		want    []string
	}{
		"claim 完了": {
			event:   LabelEventClaimCompleted,
			initial: []string{"ai-ready", "ai-needs-human", "bug"},
			want:    []string{"bug", "ai-in-progress"},
		},
		"Run needs human": {
			event:   LabelEventRunNeedsHuman,
			initial: []string{"ai-ready", "ai-in-progress", "ai-merged"},
			want:    []string{"ai-needs-human"},
		},
		"停止中の再観測": {
			event:   LabelEventNeedsHumanRecorded,
			initial: []string{"ai-ready", "ai-in-progress", "ai-merged"},
			want:    []string{"ai-ready", "ai-needs-human"},
		},
		"merge 完了の再観測": {
			event:   LabelEventMergeRecorded,
			initial: []string{"ai-ready", "ai-in-progress", "ai-needs-human", "ai-merged"},
			want:    []string{"ai-merged"},
		},
		"merge 完了": {
			event:   LabelEventMergeCompleted,
			initial: []string{"ai-ready", "ai-in-progress", "ai-needs-human"},
			want:    []string{"ai-merged"},
		},
		"already-merged 再依頼": {
			event:   LabelEventAlreadyMergedRequest,
			initial: []string{"ai-ready", "ai-merged"},
			want:    []string{"ai-merged"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			surface := newFakeLabelSurface(testCase.initial...)
			if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), testCase.event); err != nil {
				t.Fatalf("Record() error = %v", err)
			}
			got := surface.snapshot()
			slices.Sort(got)
			want := slices.Clone(testCase.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("labels = %v, want %v", got, want)
			}
		})
	}
}

// already-merged の再依頼で Issue を close しないのは、reopen が人間の操作だからである。
// 同じ理由で merge completion は close する（04_github-routing.md の Merge completion）。
func TestRecordLabelEventClosesOnlyOnMergeCompletion(t *testing.T) {
	t.Parallel()

	for event, wantClose := range map[LabelEvent]bool{
		LabelEventMergeCompleted:       true,
		LabelEventMergeRecorded:        false,
		LabelEventAlreadyMergedRequest: false,
		LabelEventClaimCompleted:       false,
		LabelEventRunNeedsHuman:        false,
		LabelEventNeedsHumanRecorded:   false,
	} {
		t.Run(string(event), func(t *testing.T) {
			t.Parallel()
			surface := newFakeLabelSurface("ai-ready")
			record, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), event)
			if err != nil {
				t.Fatalf("Record() error = %v", err)
			}
			if record.ClosedIssue != wantClose || surface.callCount("close_issue") != boolToInt(wantClose) {
				t.Fatalf("closed = %v, close 呼び出し = %d, want %v", record.ClosedIssue, surface.callCount("close_issue"), wantClose)
			}
		})
	}
}

func TestRecordMergeCompletionIsIdempotentAcrossRepeatedReconciles(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-in-progress")
	recorder := newTestLabelRecorder(t, surface)
	first, err := recorder.Record(t.Context(), testIssueRef(), LabelEventMergeCompleted)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	second, err := recorder.Record(t.Context(), testIssueRef(), LabelEventMergeCompleted)
	if err != nil {
		t.Fatalf("2 回目の Record() error = %v", err)
	}
	if first.Added != "ai-merged" || !first.ClosedIssue {
		t.Fatalf("1 回目 = %#v", first)
	}
	if second.Added != "" || len(second.Removed) != 0 || second.ClosedIssue {
		t.Fatalf("2 回目 = %#v, want mutation なし", second)
	}
	if got := surface.snapshot(); !slices.Equal(got, []string{"ai-merged"}) {
		t.Fatalf("labels = %v", got)
	}
}

// AC-4: 案内 comment は再依頼を何度検出しても 1 件のままでなければならない。
func TestRecordAlreadyMergedRequestKeepsASingleGuidanceComment(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready", "ai-merged")
	recorder := newTestLabelRecorder(t, surface)
	first, err := recorder.Record(t.Context(), testIssueRef(), LabelEventAlreadyMergedRequest)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if _, err := recorder.Record(t.Context(), testIssueRef(), LabelEventAlreadyMergedRequest); err != nil {
		t.Fatalf("2 回目の Record() error = %v", err)
	}
	if !first.CommentChanged {
		t.Fatalf("1 回目 = %#v, want 案内 comment の作成", first)
	}
	if got := len(surface.comments); got != 1 {
		t.Fatalf("comment 件数 = %d, want 1", got)
	}
	body := surface.comments[fmt.Sprintf("%d:%s", 19, AlreadyMergedCommentKind)]
	for _, phrase := range []string{"新しい Task Issue", "ai-merged"} {
		if !strings.Contains(body, phrase) {
			t.Fatalf("案内 comment = %q, want %q を含む", body, phrase)
		}
	}
}

// AC-5: 記録が途中で失敗しても導出 phase は巻き戻らず、次の reconcile が同じ label set へ収束する。
func TestRecordRetriesConvergeAfterATransientFailure(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready", "ai-in-progress")
	transient := errors.New("GitHub timeout")
	surface.failOn["remove_label:19:ai-in-progress"] = transient
	recorder := newTestLabelRecorder(t, surface)

	if _, err := recorder.Record(t.Context(), testIssueRef(), LabelEventMergeCompleted); !errors.Is(err, transient) {
		t.Fatalf("Record() error = %v, want %v", err, transient)
	}
	delete(surface.failOn, "remove_label:19:ai-in-progress")
	if _, err := recorder.Record(t.Context(), testIssueRef(), LabelEventMergeCompleted); err != nil {
		t.Fatalf("再試行の Record() error = %v", err)
	}
	if got := surface.snapshot(); !slices.Equal(got, []string{"ai-merged"}) {
		t.Fatalf("labels = %v, want [ai-merged]", got)
	}
}

// AC-5: Kudo 所有の label を人間が外しても、再観測が同じ label set へ戻す。
func TestRecordRestoresLabelRemovedByAHuman(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface()
	recorder := newTestLabelRecorder(t, surface)
	if _, err := recorder.Record(t.Context(), testIssueRef(), LabelEventRunNeedsHuman); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	surface.removeByHuman("ai-needs-human")
	record, err := recorder.Record(t.Context(), testIssueRef(), LabelEventRunNeedsHuman)
	if err != nil {
		t.Fatalf("再記録の Record() error = %v", err)
	}
	if record.Added != "ai-needs-human" || !slices.Equal(surface.snapshot(), []string{"ai-needs-human"}) {
		t.Fatalf("record = %#v, labels = %v", record, surface.snapshot())
	}
}

// 記録は導出 phase の投影を先に置いてから、置き換えられた label を外す。逆順にすると
// 中断時に「Kudo の status が何も付いていない Issue」が観測できる。
func TestRecordAddsTheDerivedLabelBeforeRemovingReplacedOnes(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready")
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventClaimCompleted); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	add := slices.Index(surface.calls, "converge_labels:19:ai-in-progress")
	remove := slices.Index(surface.calls, "remove_label:19:ai-ready")
	if add < 0 || remove < 0 || add > remove {
		t.Fatalf("calls = %v", surface.calls)
	}
}

// close は label 収束より前に行う。逆順にすると、close が失敗した時点の観測が
// 「完了記録済み」に見え、次の reconcile が close を再試行しなくなる。
func TestRecordClosesTheIssueBeforeRecordingTheMergedLabel(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-in-progress")
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventMergeCompleted); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	closeIndex := slices.Index(surface.calls, "close_issue:19")
	labelIndex := slices.Index(surface.calls, "converge_labels:19:ai-merged")
	if closeIndex < 0 || labelIndex < 0 || closeIndex > labelIndex {
		t.Fatalf("calls = %v", surface.calls)
	}
}

// close が失敗した cycle では `ai-merged` を記録しない。記録してしまうと、次の
// reconcile が完了記録済みと判定して close を二度と再試行しない。
func TestRecordDoesNotRecordMergedLabelWhenCloseFails(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-in-progress")
	transient := errors.New("GitHub timeout")
	surface.failOn["close_issue:19"] = transient
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventMergeCompleted); !errors.Is(err, transient) {
		t.Fatalf("Record() error = %v, want %v", err, transient)
	}
	if slices.Contains(surface.snapshot(), "ai-merged") {
		t.Fatalf("labels = %v, want ai-merged 無し", surface.snapshot())
	}
}

// 停止中の再観測では `ai-ready` を消費しない。消費すると、人間が契約を直して付け直した
// escalation 解除の唯一の trigger が resume 判定の前に消える。
func TestRecordNeedsHumanReobservationKeepsTheHumanReadyTrigger(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-needs-human", "ai-ready")
	record, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventNeedsHumanRecorded)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if !slices.Contains(surface.snapshot(), "ai-ready") {
		t.Fatalf("labels = %v, want ai-ready を保持", surface.snapshot())
	}
	if record.Changed() {
		t.Fatalf("record = %#v, want 収束済み", record)
	}
}

// 完了記録済みの merge を再観測しても Issue を閉じ直さない。close は完了を初めて
// 記録したときの一度きりの副作用であり、その後の open は人間の操作である。
func TestRecordMergeReobservationDoesNotCloseTheIssueAgain(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-merged")
	record, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventMergeRecorded)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if record.ClosedIssue || surface.callCount("close_issue") != 0 {
		t.Fatalf("record = %#v, close 呼び出し = %d", record, surface.callCount("close_issue"))
	}
}

// 別 repository の IssueRef を渡しても mutation を出さない。LabelSurface は Issue number
// しか受け取らないため、取り違えると別 repository の同番号 Issue を変更してしまう。
func TestRecordRejectsIssuesOutsideTheBoundRepository(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready")
	recorder := newTestLabelRecorder(t, surface)
	_, err := recorder.Record(t.Context(),
		contract.IssueRef{Owner: "other", Repository: "project", Number: 19}, LabelEventClaimCompleted)
	if !errors.Is(err, ErrIssueOutsideRepository) || len(surface.calls) != 0 {
		t.Fatalf("error = %v, calls = %v", err, surface.calls)
	}
}

func TestRecordRejectsUnknownEventWithoutMutating(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready")
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEvent("ai-reviewing")); err == nil {
		t.Fatal("語彙外の label event を受理した")
	}
	if len(surface.calls) != 0 {
		t.Fatalf("calls = %v, want 呼び出しなし", surface.calls)
	}
}

func TestRecordRejectsIncompleteIssueIdentity(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface()
	_, err := newTestLabelRecorder(t, surface).Record(t.Context(),
		contract.IssueRef{Owner: "mrbaron3", Repository: "kudo"}, LabelEventClaimCompleted)
	if err == nil || len(surface.calls) != 0 {
		t.Fatalf("error = %v, calls = %v", err, surface.calls)
	}
}

func TestNewLabelRecorderRejectsAmbiguousLabelSets(t *testing.T) {
	t.Parallel()

	for name, labels := range map[string]LabelSet{
		"空の label":   {Ready: "", InProgress: "ai-in-progress", NeedsHuman: "ai-needs-human", Merged: "ai-merged"},
		"重複した label": {Ready: "ai-ready", InProgress: "ai-ready", NeedsHuman: "ai-needs-human", Merged: "ai-merged"},
		"大文字違いの重複":   {Ready: "ai-ready", InProgress: "AI-Ready", NeedsHuman: "ai-needs-human", Merged: "ai-merged"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			policy := testLabelPolicy()
			policy.Labels = labels
			if _, err := NewLabelRecorder(newFakeLabelSurface(), testIssueRef(), policy, nil); err == nil {
				t.Fatalf("不正な LabelSet %#v を受理した", labels)
			}
		})
	}
}

func TestDeriveLabelEventMapsDerivationsToRecordedEvents(t *testing.T) {
	t.Parallel()

	runPhase := workflow.Derivation{
		Phase: workflow.PhaseAwaitingTestReview,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileRequestReview},
	}
	merged := workflow.Derivation{
		Phase: workflow.PhaseMerged,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileRecordCompletion},
	}
	needsHuman := workflow.Derivation{
		Phase: workflow.PhaseNeedsHuman,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileEscalateHuman},
	}
	awaitHuman := workflow.Derivation{
		Phase: workflow.PhaseNeedsHuman,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileAwaitHuman},
	}
	candidate := workflow.Derivation{
		Phase: workflow.PhaseCandidate,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileDispatchOperation},
	}
	notCandidate := workflow.Derivation{
		Phase: workflow.PhaseNone,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileSkipNotCandidate},
	}
	superseded := workflow.Derivation{Phase: workflow.PhaseSuperseded}

	recordedByKudo := []workflow.LabelEventObservation{
		{Label: "ai-merged", Added: true, ActorID: coordinatorActorID},
	}
	closedRecorded := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateClosed, Labels: []string{"ai-merged"},
		LabelEvents: recordedByKudo,
	}
	openWithReady := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-ready"},
	}
	reopenedRequest := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"AI-Merged", "AI-Ready"},
		LabelEvents: recordedByKudo,
	}
	reopenedQuiet := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-merged"},
		LabelEvents: recordedByKudo,
	}
	// 人間が ai-merged を外して reopen し、ai-ready を付け直した再依頼。現在値からは
	// 完了記録が消えているが、付与履歴が残る。
	mergedLabelRemoved := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-ready"},
		LabelEvents: append(slices.Clone(recordedByKudo),
			workflow.LabelEventObservation{Label: "ai-merged", ActorID: humanActorID}),
	}
	// 人間が merge 前に手で ai-merged を付けた場合。付与履歴はあるが Kudo 名義ではない。
	humanAppliedMerged := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-merged", "ai-in-progress"},
		LabelEvents: []workflow.LabelEventObservation{
			{Label: "ai-merged", Added: true, ActorID: humanActorID},
		},
	}
	// reopen せずに ai-ready だけを付け直した再依頼。GitHub は closed な Issue にも
	// label を付けられる。
	closedRerequest := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateClosed, Labels: []string{"ai-merged", "ai-ready"},
		LabelEvents: recordedByKudo,
	}
	openRun := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-in-progress"},
	}

	for name, testCase := range map[string]struct {
		derivation workflow.Derivation
		issue      workflow.IssueObservation
		want       LabelEvent
		wantOK     bool
	}{
		"進行中の Run": {runPhase, openRun, LabelEventClaimCompleted, true},
		"merge 完了": {merged, openRun, LabelEventMergeCompleted, true},
		"進行中に ai-ready が付いたまま merge": {merged, openWithReady, LabelEventMergeCompleted, true},
		"完了記録済みの再観測":                 {merged, closedRecorded, LabelEventMergeRecorded, true},
		"reopen 後の ai-ready 再付与":     {merged, reopenedRequest, LabelEventAlreadyMergedRequest, true},
		"再依頼を処理した後の reopen 済み Issue": {merged, reopenedQuiet, LabelEventMergeRecorded, true},
		"ai-merged を外した再依頼":          {merged, mergedLabelRemoved, LabelEventAlreadyMergedRequest, true},
		"closed のままの再依頼":             {merged, closedRerequest, LabelEventAlreadyMergedRequest, true},
		"人間が手で付けた ai-merged":         {merged, humanAppliedMerged, LabelEventMergeCompleted, true},
		"escalation":                 {needsHuman, openRun, LabelEventRunNeedsHuman, true},
		"停止中の再観測":                    {awaitHuman, openRun, LabelEventNeedsHumanRecorded, true},
		"claim 前の候補":                 {candidate, openWithReady, "", false},
		"候補外":                        {notCandidate, closedRecorded, "", false},
		"superseded":                 {superseded, openRun, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := DeriveLabelEvent(testCase.derivation, testCase.issue, testLabelPolicy())
			if got != testCase.want || ok != testCase.wantOK {
				t.Fatalf("DeriveLabelEvent() = %q, %v, want %q, %v", got, ok, testCase.want, testCase.wantOK)
			}
		})
	}
}

// DeriveLabelEvent は自分の Record 結果を反映した観測に対して不動点でなければならない。
// そうでないと、AC-4 の到達状態が次の reconcile で壊れる。
func TestDeriveLabelEventIsAFixedPointAfterRecordingAlreadyMergedRequest(t *testing.T) {
	t.Parallel()

	// Kudo が完了を記録して close 済みの Issue を、人間が reopen して ai-ready を付け直した状態。
	surface := newFakeLabelSurface()
	surface.events = []workflow.LabelEventObservation{
		{Label: "ai-merged", Added: true, ActorID: coordinatorActorID},
	}
	surface.labels = []string{"ai-merged", "ai-ready"}
	recorder := newTestLabelRecorder(t, surface)
	derivation := workflow.Derivation{
		Phase: workflow.PhaseMerged,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileRecordCompletion},
	}
	for round := range 3 {
		event, ok := DeriveLabelEvent(derivation, surface.observe(workflow.IssueStateOpen), testLabelPolicy())
		if !ok {
			t.Fatalf("round %d: label event を導出できない", round)
		}
		if _, err := recorder.Record(t.Context(), testIssueRef(), event); err != nil {
			t.Fatalf("round %d: Record() error = %v", round, err)
		}
		if surface.closedIssue() {
			t.Fatalf("round %d: reopen された Issue を close した", round)
		}
	}
	if got := surface.snapshot(); !slices.Equal(got, []string{"ai-merged"}) {
		t.Fatalf("labels = %v, want [ai-merged]", got)
	}
	if surface.callCount("ensure_comment") != 1 {
		t.Fatalf("案内 comment の収束 = %d 回, want 1（2 回目以降は再依頼ではない）",
			surface.callCount("ensure_comment"))
	}
}

// 記録を完成させる副作用は、再発見の手掛かりになる label を消費する前に済ませる。
// 逆順にすると、案内 comment が失敗した時点で ai-ready の無い Issue が残り、polling の
// どの列挙にも現れなくなる。
func TestRecordWritesTheGuidanceCommentBeforeConsumingTheReadyLabel(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-merged", "ai-ready")
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(), LabelEventAlreadyMergedRequest); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	comment := slices.Index(surface.calls, "ensure_comment:19:"+AlreadyMergedCommentKind)
	remove := slices.Index(surface.calls, "remove_label:19:ai-ready")
	if comment < 0 || remove < 0 || comment > remove {
		t.Fatalf("calls = %v", surface.calls)
	}
}

// 案内 comment が失敗した cycle では ai-ready を消費しない。消費すると、再依頼が
// あったことが GitHub 上のどこにも残らないまま、次の reconcile も気付けない。
func TestRecordKeepsTheReadyLabelWhenTheGuidanceCommentFails(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-merged", "ai-ready")
	transient := errors.New("GitHub timeout")
	surface.failOn["ensure_comment:19:"+AlreadyMergedCommentKind] = transient
	if _, err := newTestLabelRecorder(t, surface).Record(t.Context(), testIssueRef(),
		LabelEventAlreadyMergedRequest); !errors.Is(err, transient) {
		t.Fatalf("Record() error = %v, want %v", err, transient)
	}
	if !slices.Contains(surface.snapshot(), "ai-ready") {
		t.Fatalf("labels = %v, want ai-ready を保持", surface.snapshot())
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// PhaseClaimed は「branch はあるが Pull Request がまだ無い」＝ claim 続行中または中断の
// 観測である（02_workflow.md の Derived phases）。04_github-routing.md の Result 表は
// `claimed` を「branch と draft PR を確定した」と定義しており、Transition rules の
// 「claim 完了」行はその状態にだけ対応する。記録面が成立する前に ai-ready を消費すると、
// claim が transport failure を繰り返す間も人間の trigger が戻らない（同書は
// 「一時 transport failure では ai-ready を消費しない」と定める）。
func TestDeriveLabelEventDoesNotRecordClaimCompletionBeforeTheRecordSurfaceExists(t *testing.T) {
	t.Parallel()

	claimInProgress := workflow.Derivation{
		Phase: workflow.PhaseClaimed,
		Next: workflow.ReconcileAction{
			Kind:      workflow.ReconcileDispatchOperation,
			Operation: contract.OperationClaim,
		},
	}
	issue := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-ready"},
	}
	got, ok := DeriveLabelEvent(claimInProgress, issue, testLabelPolicy())
	if ok {
		t.Fatalf("DeriveLabelEvent() = %q, %v, want no label event", got, ok)
	}
}

// merge 完了の記録は ai-merged の付与に成功してから ai-ready の削除に失敗し得る。
// 残った ai-ready を「人間の再依頼」と読むと、再依頼が無いのに「新しい実行を開始しません」
// という案内 comment が Issue へ永続的に残る。付与時刻が完了記録より後の ai-ready だけを
// 再依頼とする。同時刻は再依頼にしない（誤った永続記録より、案内の欠落を選ぶ）。
func TestDeriveLabelEventTreatsPreMergeReadyLabelAsResidue(t *testing.T) {
	t.Parallel()

	merged := workflow.Derivation{
		Phase: workflow.PhaseMerged,
		Next:  workflow.ReconcileAction{Kind: workflow.ReconcileRecordCompletion},
	}
	completion := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	issueWith := func(readyAddedAt time.Time) workflow.IssueObservation {
		return workflow.IssueObservation{
			Number: 19, State: workflow.IssueStateOpen,
			Labels: []string{"ai-merged", "ai-ready"},
			LabelEvents: []workflow.LabelEventObservation{
				{Label: "ai-ready", Added: true, ActorID: humanActorID, OccurredAt: readyAddedAt},
				{Label: "ai-merged", Added: true, ActorID: coordinatorActorID, OccurredAt: completion},
			},
		}
	}
	for name, testCase := range map[string]struct {
		readyAddedAt time.Time
		want         LabelEvent
	}{
		"完了記録より前に付いた残骸": {completion.Add(-time.Minute), LabelEventMergeRecorded},
		"完了記録と同時刻":      {completion, LabelEventMergeRecorded},
		"完了記録より後の再依頼":   {completion.Add(time.Minute), LabelEventAlreadyMergedRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := DeriveLabelEvent(merged, issueWith(testCase.readyAddedAt), testLabelPolicy())
			if !ok || got != testCase.want {
				t.Fatalf("DeriveLabelEvent() = %q, %v, want %q, true", got, ok, testCase.want)
			}
		})
	}
}

// LabelSet も同じ理由で comma を拒む。回復経路の query は Kudo 所有 label を
// comma 区切りで渡すため、値に comma を含む設定は列挙側が必ず拒否する。
func TestLabelSetRejectsCommaInLabelName(t *testing.T) {
	t.Parallel()

	labels := DefaultLabelSet()
	labels.Ready = "ready,bot"
	if err := labels.Validate(); err == nil {
		t.Fatalf("Validate() = nil, want error")
	}
}

// 途中失敗でも、そこまでに起きた mutation は LabelRecord に残さなければならない。
// 捨てると呼び出し側の記録が「何も変えていない」と読め、失敗した reconcile の後に
// GitHub 上で何が変わっているかを record から追えなくなる。
func TestRecordReportsMutationsThatHappenedBeforeAFailure(t *testing.T) {
	t.Parallel()

	surface := newFakeLabelSurface("ai-ready", "ai-in-progress")
	transient := errors.New("GitHub timeout")
	surface.failOn["remove_label:19:ai-in-progress"] = transient
	recorder := newTestLabelRecorder(t, surface)

	record, err := recorder.Record(t.Context(), testIssueRef(), LabelEventMergeCompleted)
	if !errors.Is(err, transient) {
		t.Fatalf("Record() error = %v, want %v", err, transient)
	}
	if record.Added != "ai-merged" {
		t.Errorf("Added = %q, want %q", record.Added, "ai-merged")
	}
	if !slices.Contains(record.Removed, "ai-ready") {
		t.Errorf("Removed = %v, want it to contain ai-ready", record.Removed)
	}
	if !record.Changed() {
		t.Errorf("Changed() = false, want true")
	}
}

// deployment 全体の設定不備は Issue 固有の瑕疵ではない。escalation として label へ
// 記録すると、1 cycle で repository 中の候補 Issue から人間所有の ai-ready が全部外れ、
// 設定を直しても人間が Issue ごとに付け直すまで一件も再開しない。
// 04_github-routing.md は「dependency 待ち、capacity 待ち、一時 transport failure では
// ai-ready を消費しない」と定めており、Issue 側に瑕疵の無い停止で人間の trigger を
// 消費しない原則を示している。
func TestDeriveLabelEventDoesNotRecordDeploymentConfigurationFailures(t *testing.T) {
	t.Parallel()

	configurationRequired := workflow.Derivation{
		Phase: workflow.PhaseNew,
		Next: workflow.ReconcileAction{
			Kind:   workflow.ReconcileEscalateHuman,
			Reason: workflow.EscalationExternalConfigurationRequired,
		},
	}
	issue := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen,
		Labels: []string{"ai-ready", "ai-in-progress"},
	}
	got, ok := DeriveLabelEvent(configurationRequired, issue, testLabelPolicy())
	if ok {
		t.Fatalf("DeriveLabelEvent() = %q, %v, want no label event", got, ok)
	}
}
