package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// fakeLabelSurface は GitHub 上の label / comment / Issue state を、adapter と同じ
// 「現在値を確認してから mutate する」性質で模す。
type fakeLabelSurface struct {
	mu       sync.Mutex
	labels   []string
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

func (f *fakeLabelSurface) EnsureLabel(_ context.Context, issue int64, label string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("ensure_label:%d:%s", issue, label)); err != nil {
		return false, err
	}
	if slices.Contains(f.labels, label) {
		return false, nil
	}
	f.labels = append(f.labels, label)
	return true, nil
}

func (f *fakeLabelSurface) RemoveLabel(_ context.Context, issue int64, label string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("remove_label:%d:%s", issue, label)); err != nil {
		return false, err
	}
	index := slices.Index(f.labels, label)
	if index < 0 {
		return false, nil
	}
	f.labels = slices.Delete(f.labels, index, index+1)
	return true, nil
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

func (f *fakeLabelSurface) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.labels)
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

func newTestLabelRecorder(t *testing.T, surface LabelSurface) *LabelRecorder {
	t.Helper()
	recorder, err := NewLabelRecorder(surface, DefaultLabelSet(), nil)
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
		LabelEventAlreadyMergedRequest: false,
		LabelEventClaimCompleted:       false,
		LabelEventRunNeedsHuman:        false,
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
	if _, err := surface.RemoveLabel(t.Context(), 19, "ai-needs-human"); err != nil {
		t.Fatalf("RemoveLabel() error = %v", err)
	}
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
	add := slices.Index(surface.calls, "ensure_label:19:ai-in-progress")
	remove := slices.Index(surface.calls, "remove_label:19:ai-ready")
	if add < 0 || remove < 0 || add > remove {
		t.Fatalf("calls = %v", surface.calls)
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
			if _, err := NewLabelRecorder(newFakeLabelSurface(), labels, nil); err == nil {
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

	closedIssue := workflow.IssueObservation{Number: 19, State: workflow.IssueStateClosed}
	openWithReady := workflow.IssueObservation{
		Number: 19, State: workflow.IssueStateOpen, Labels: []string{"ai-ready"},
	}
	openRun := workflow.IssueObservation{Number: 19, State: workflow.IssueStateOpen}

	for name, testCase := range map[string]struct {
		derivation workflow.Derivation
		issue      workflow.IssueObservation
		want       LabelEvent
		wantOK     bool
	}{
		"進行中の Run":                   {runPhase, openRun, LabelEventClaimCompleted, true},
		"merge 完了":                   {merged, openRun, LabelEventMergeCompleted, true},
		"close 済みの merge 完了":         {merged, closedIssue, LabelEventMergeCompleted, true},
		"merged な Issue への ai-ready": {merged, openWithReady, LabelEventAlreadyMergedRequest, true},
		"escalation":                 {needsHuman, openRun, LabelEventRunNeedsHuman, true},
		"停止中の再観測":                    {awaitHuman, openRun, LabelEventRunNeedsHuman, true},
		"claim 前の候補":                 {candidate, openWithReady, "", false},
		"候補外":                        {notCandidate, closedIssue, "", false},
		"superseded":                 {superseded, openRun, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := DeriveLabelEvent(testCase.derivation, testCase.issue, DefaultLabelSet())
			if got != testCase.want || ok != testCase.wantOK {
				t.Fatalf("DeriveLabelEvent() = %q, %v, want %q, %v", got, ok, testCase.want, testCase.wantOK)
			}
		})
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
