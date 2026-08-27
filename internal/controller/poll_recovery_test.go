package controller

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// fakeLiveState は branch ref create の atomicity だけを模した live state である。
// phase 導出は行わない。ここで確かめたいのは「何度発見されても Run は 1 つ」だけである。
type fakeLiveState struct {
	mu           sync.Mutex
	observations map[int]int
	branches     map[int]struct{}
	created      map[int]int
}

func newFakeLiveState(existingRuns ...int) *fakeLiveState {
	state := &fakeLiveState{
		observations: map[int]int{},
		branches:     map[int]struct{}{},
		created:      map[int]int{},
	}
	for _, issue := range existingRuns {
		state.branches[issue] = struct{}{}
	}
	return state
}

// reconcile は live state を観測し、branch がまだ無い場合だけ Run を作る。
func (s *fakeLiveState) reconcile(issue contract.IssueRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations[issue.Number]++
	if _, exists := s.branches[issue.Number]; exists {
		return
	}
	s.branches[issue.Number] = struct{}{}
	s.created[issue.Number]++
}

func (s *fakeLiveState) counts(issue int) (observations, created int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observations[issue], s.created[issue]
}

// AC-1 と M3 exit criteria: webhook を一つも受け取らなくても、polling が候補 Issue と
// 途中 phase の Run を同じ ReconcileIssue へ流し、poll cycle が重なっても二重 Run に
// ならない。
func TestPollingRecoversRunsWithoutWebhookAndKeepsASingleRun(t *testing.T) {
	t.Parallel()

	state := newFakeLiveState(17)
	dispatcher, err := NewTriggerDispatcher(
		func(_ context.Context, request workflow.ReconcileRequest) error {
			state.reconcile(request.Issue)
			return nil
		},
		TriggerDispatcherConfig{MaxInFlight: 2},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("NewTriggerDispatcher() error = %v", err)
	}

	// 19 は新規候補、17 は open な kudo PR を持つ途中 phase の Run である。
	discovery := &fakeDiscovery{candidates: issueRefs(19), runs: issueRefs(17)}
	clock := newFakeClock()
	// slot が埋まった瞬間に当たっても止まらないよう、capacity 待ちだけは自動で進める。
	clock.autoAdvance(t.Context(), DefaultCapacityRetryInterval)
	poller := newTestPoller(t, testPollerConfig(discovery, dispatcher, clock))

	poller.runCycle(t.Context(), workflow.TriggerStartup)
	poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)

	shutdown, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(shutdown); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	for _, testCase := range []struct {
		issue            int
		wantObservations int
		wantCreated      int
	}{
		{issue: 19, wantObservations: 2, wantCreated: 1},
		{issue: 17, wantObservations: 2, wantCreated: 0},
	} {
		observations, created := state.counts(testCase.issue)
		if observations != testCase.wantObservations || created != testCase.wantCreated {
			t.Fatalf("Issue %d: 観測 = %d, Run 作成 = %d, want %d, %d",
				testCase.issue, observations, created, testCase.wantObservations, testCase.wantCreated)
		}
	}
}

// shutdown 中に発見した IssueRef は落ちるが、次回起動の startup reconciliation が
// 同じ live state から回収する。
func TestPollingAfterRestartReobservesIssuesDroppedByShutdown(t *testing.T) {
	t.Parallel()

	state := newFakeLiveState()
	reconcile := func(_ context.Context, request workflow.ReconcileRequest) error {
		state.reconcile(request.Issue)
		return nil
	}
	stopped, err := NewTriggerDispatcher(reconcile, TriggerDispatcherConfig{MaxInFlight: 1},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewTriggerDispatcher() error = %v", err)
	}
	if err := stopped.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	beforeRestart := newTestPoller(t, testPollerConfig(discovery, stopped, newFakeClock()))
	report := beforeRestart.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.Submitted != 0 || report.Backlog != 1 {
		t.Fatalf("shutdown 中の cycle = %#v, want backlog 1", report)
	}
	if observations, _ := state.counts(19); observations != 0 {
		t.Fatalf("停止中に reconcile を実行した: %d", observations)
	}

	restarted, err := NewTriggerDispatcher(reconcile, TriggerDispatcherConfig{MaxInFlight: 1},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewTriggerDispatcher() error = %v", err)
	}
	afterRestart := newTestPoller(t, testPollerConfig(discovery, restarted, newFakeClock()))
	afterRestart.runCycle(t.Context(), workflow.TriggerStartup)
	if err := restarted.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if observations, created := state.counts(19); observations != 1 || created != 1 {
		t.Fatalf("再起動後の観測 = %d, Run 作成 = %d, want 1, 1", observations, created)
	}
}
