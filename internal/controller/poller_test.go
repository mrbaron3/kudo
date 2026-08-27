package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

type fakeDiscovery struct {
	mu         sync.Mutex
	candidates []contract.IssueRef
	runs       []contract.IssueRef
	// candidateErr / runErr は 1 回の呼び出しごとに先頭から消費する。
	labeled      map[string][]contract.IssueRef
	candidateErr []error
	runErr       []error
	labeledErr   []error
	labeledFor   []string
	calls        int
}

func (d *fakeDiscovery) ListCandidateIssueRefs(_ context.Context, filter workflow.CandidateFilter) ([]contract.IssueRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if filter.Assignee == "" || filter.ReadyLabel == "" {
		return nil, errors.New("candidate filter が空である")
	}
	if err := next(&d.candidateErr); err != nil {
		return nil, err
	}
	return slices.Clone(d.candidates), nil
}

func (d *fakeDiscovery) ListOpenRunIssueRefs(context.Context) ([]contract.IssueRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := next(&d.runErr); err != nil {
		return nil, err
	}
	return slices.Clone(d.runs), nil
}

func (d *fakeDiscovery) ListLabeledIssueRefs(_ context.Context, labels []string) ([]contract.IssueRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(labels) == 0 {
		return nil, errors.New("列挙する label が空である")
	}
	d.labeledFor = append(d.labeledFor, strings.Join(labels, "+"))
	if err := next(&d.labeledErr); err != nil {
		return nil, err
	}
	return slices.Clone(d.labeled[strings.Join(labels, "+")]), nil
}

func next(queue *[]error) error {
	if len(*queue) == 0 {
		return nil
	}
	err := (*queue)[0]
	*queue = (*queue)[1:]
	return err
}

type recordingTrigger struct {
	mu       sync.Mutex
	requests []workflow.ReconcileRequest
	// refuse は Issue number ごとに、その回数だけ capacity error を返す。
	refuse map[int]int
	// block は最初の呼び出しを channel が閉じるまで止める。
	block   chan struct{}
	blocked chan struct{}
	stopped bool
	// failWith は分類できない失敗を模す。
	failWith error
}

func newRecordingTrigger() *recordingTrigger {
	return &recordingTrigger{refuse: map[int]int{}}
}

func (r *recordingTrigger) TriggerReconcile(_ context.Context, request workflow.ReconcileRequest) error {
	r.mu.Lock()
	if r.block != nil {
		gate, signal := r.block, r.blocked
		r.block, r.blocked = nil, nil
		r.mu.Unlock()
		close(signal)
		<-gate
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if r.stopped {
		return ErrDispatcherStopped
	}
	if r.failWith != nil {
		return r.failWith
	}
	if remaining := r.refuse[request.Issue.Number]; remaining > 0 {
		r.refuse[request.Issue.Number] = remaining - 1
		return ErrDispatcherAtCapacity
	}
	r.requests = append(r.requests, request)
	return nil
}

func (r *recordingTrigger) snapshot() []workflow.ReconcileRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.requests)
}

func (r *recordingTrigger) count() int { return len(r.snapshot()) }

func issueRefs(numbers ...int) []contract.IssueRef {
	refs := make([]contract.IssueRef, 0, len(numbers))
	for _, number := range numbers {
		refs = append(refs, contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: number})
	}
	return refs
}

func newTestPoller(t *testing.T, config PollerConfig) *Poller {
	t.Helper()
	poller, err := NewPoller(config)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}
	return poller
}

func testPollerConfig(discovery Discovery, trigger ReconcileTrigger, clock Clock) PollerConfig {
	return PollerConfig{
		Sources: []PollSource{{Repository: "mrbaron3/kudo", Discovery: discovery}},
		Trigger: trigger,
		Clock:   clock,
		Poll:    DefaultPollConfig(),
		Logger:  slog.New(slog.DiscardHandler),
	}
}

// AC-1: webhook が届かなくても、候補 Issue と open な kudo PR の両方が同じ
// ReconcileIssue へ投入される。
func TestPollCycleSubmitsCandidatesAndOpenRunsToTheSameReconcile(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24), runs: issueRefs(17, 19)}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	report := poller.runCycle(t.Context(), workflow.TriggerStartup)
	if report.FirstErr != nil {
		t.Fatalf("runCycle() error = %v", report.FirstErr)
	}

	var numbers []int
	for _, request := range trigger.snapshot() {
		numbers = append(numbers, request.Issue.Number)
		if request.Trigger.Source != workflow.TriggerStartup || request.Trigger.ID == "" {
			t.Fatalf("trigger = %#v", request.Trigger)
		}
		if request.Trigger.Action != "" {
			t.Fatalf("polling trigger が webhook の action を持っている: %#v", request.Trigger)
		}
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{17, 19, 24}) {
		t.Fatalf("投入した Issue = %v, want [17 19 24]", numbers)
	}
	if report.Discovered != 3 || report.Submitted != 3 || report.Backlog != 0 {
		t.Fatalf("report = %#v", report)
	}
}

// 同じ Issue が candidate と open Run の両方に現れても、1 cycle で 2 回投入しない。
func TestPollCycleSubmitsEachIssueOncePerCycle(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19), runs: issueRefs(19)}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if got := trigger.count(); got != 1 {
		t.Fatalf("投入回数 = %d, want 1", got)
	}
}

// poll cycle は同じ trigger ID を全 IssueRef へ付け、cycle ごとに別 ID にする。
func TestPollCycleCorrelatesEveryIssueWithOneTriggerIdentity(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24)}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	first := poller.runCycle(t.Context(), workflow.TriggerStartup)
	second := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if first.Trigger.ID == second.Trigger.ID {
		t.Fatalf("cycle をまたいで同じ trigger ID を使った: %q", first.Trigger.ID)
	}
	if first.Trigger.Source != workflow.TriggerStartup || second.Trigger.Source != workflow.TriggerScheduledPoll {
		t.Fatalf("trigger source = %q, %q", first.Trigger.Source, second.Trigger.Source)
	}
	for _, request := range trigger.snapshot()[:2] {
		if request.Trigger.ID != first.Trigger.ID {
			t.Fatalf("cycle 内で trigger ID が揃っていない: %#v", request.Trigger)
		}
	}
}

// 同時実行上限で落ちた IssueRef を成功として捨てない。捨てると、候補順が同じ poller では
// 末尾が毎 cycle 飢餓する（ErrDispatcherAtCapacity の doc comment が定める制約）。
func TestPollCycleResubmitsIssuesRefusedByCapacityInsteadOfDroppingThem(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24, 31)}
	trigger := newRecordingTrigger()
	trigger.refuse[24] = 2
	clock := newFakeClock()
	clock.autoAdvance(t.Context(), DefaultPollConfig().CapacityRetryInterval)
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.FirstErr != nil {
		t.Fatalf("runCycle() error = %v", report.FirstErr)
	}
	var numbers []int
	for _, request := range trigger.snapshot() {
		numbers = append(numbers, request.Issue.Number)
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{19, 24, 31}) {
		t.Fatalf("投入した Issue = %v, want [19 24 31]", numbers)
	}
	if report.Backlog != 0 || report.CapacityWaits == 0 {
		t.Fatalf("report = %#v, want backlog 0 と capacity 待ちの記録", report)
	}
}

// shutdown 後は cycle を続けない。落ちた IssueRef は次回起動の再観測が回収する。
func TestPollCycleStopsWhenTheDispatcherIsShuttingDown(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24, 31)}
	trigger := newRecordingTrigger()
	trigger.stopped = true
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if !errors.Is(report.FirstErr, ErrDispatcherStopped) {
		t.Fatalf("FirstErr = %v, want ErrDispatcherStopped", report.FirstErr)
	}
	if trigger.count() != 0 || report.Submitted != 0 || report.Backlog != 3 {
		t.Fatalf("report = %#v, 投入 = %d", report, trigger.count())
	}
}

// 列挙の失敗は cycle の失敗であり、同じ cycle の他 repository を止めない。
func TestPollCycleKeepsPollingOtherRepositoriesAfterAFailure(t *testing.T) {
	t.Parallel()

	failing := &fakeDiscovery{candidateErr: []error{errors.New("GitHub timeout")}}
	healthy := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	config := testPollerConfig(failing, trigger, newFakeClock())
	config.Sources = append(config.Sources, PollSource{Repository: "mrbaron3/other", Discovery: healthy})
	poller := newTestPoller(t, config)

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.FirstErr == nil || report.Failures != 1 {
		t.Fatalf("report = %#v, want 1 件の失敗", report)
	}
	if trigger.count() != 1 {
		t.Fatalf("投入 = %d, want 健全な repository の 1 件", trigger.count())
	}
}

// AC-2: 既定 configuration では 15 分に達するまで次 cycle を開始しない。
func TestScheduledPollWaitsForTheDefaultFifteenMinuteInterval(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	clock := newFakeClock()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	clock.awaitWaits(t, 1)
	if got := trigger.count(); got != 1 {
		t.Fatalf("startup reconciliation の投入 = %d, want 1", got)
	}

	clock.Advance(DefaultPollInterval - time.Second)
	if got := trigger.count(); got != 1 {
		t.Fatalf("15 分に達する前に cycle を開始した: 投入 = %d", got)
	}

	clock.Advance(time.Second)
	clock.awaitWaits(t, 2)
	if got := trigger.count(); got != 2 {
		t.Fatalf("15 分到達後の投入 = %d, want 2", got)
	}

	sources := []workflow.TriggerSource{}
	for _, request := range trigger.snapshot() {
		sources = append(sources, request.Trigger.Source)
	}
	if !slices.Equal(sources, []workflow.TriggerSource{workflow.TriggerStartup, workflow.TriggerScheduledPoll}) {
		t.Fatalf("trigger source = %v", sources)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// poll cycle が interval より長引いても、二つ目の cycle を重ねない。
func TestScheduledPollDoesNotOverlapCycles(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	gate := make(chan struct{})
	trigger.block, trigger.blocked = gate, make(chan struct{})
	blocked := trigger.blocked
	clock := newFakeClock()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	<-blocked
	clock.Advance(4 * DefaultPollInterval)
	if got := trigger.count(); got != 0 {
		t.Fatalf("進行中の cycle と重ねて投入した: %d", got)
	}
	close(gate)

	clock.awaitWaits(t, 1)
	if got := trigger.count(); got < 1 {
		t.Fatalf("cycle 完了後の投入 = %d, want 1 以上", got)
	}
	cancel()
	<-done
}

// 失敗した cycle は jitter 付き backoff で待ち、成功で既定 interval へ戻る。
func TestScheduledPollBacksOffAfterFailureAndRecoversOnSuccess(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		candidates:   issueRefs(19),
		candidateErr: []error{errors.New("GitHub 5xx"), errors.New("GitHub 5xx")},
	}
	trigger := newRecordingTrigger()
	clock := newFakeClock()
	config := testPollerConfig(discovery, trigger, clock)
	config.Jitter = func(d time.Duration) time.Duration { return d }
	poller := newTestPoller(t, config)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	for wait := 1; wait <= 3; wait++ {
		clock.awaitWaits(t, wait)
		clock.Advance(DefaultPollInterval)
	}
	clock.awaitWaits(t, 4)
	cancel()
	<-done

	want := []time.Duration{
		DefaultPollBackoffInitial,
		2 * DefaultPollBackoffInitial,
		DefaultPollInterval,
	}
	if got := clock.requestedDelays(); !slices.Equal(got[:3], want) {
		t.Fatalf("待機 = %v, want %v", got[:3], want)
	}
}

// backoff の上限を超えない。上限が無いと、rate limit が長引いた repository の polling が
// 事実上停止し、webhook 欠落の回復経路が失われる。
func TestBackoffDelayIsBoundedByTheConfiguredMaximum(t *testing.T) {
	t.Parallel()

	config := DefaultPollConfig()
	for failures := 1; failures <= 20; failures++ {
		delay := backoffDelay(config, failures, func(d time.Duration) time.Duration { return d })
		if delay < MinPollBackoff || delay > config.BackoffMax {
			t.Fatalf("failures = %d, delay = %v, want [%v, %v]", failures, delay, MinPollBackoff, config.BackoffMax)
		}
	}
}

// 既定の jitter は初回 backoff にも効かなければならない。初回が一点に張り付くと、
// 同時に失敗した instance が揃って再試行し、jitter が防ぐはずの状況そのものになる。
func TestDefaultJitterSpreadsEvenTheFirstBackoff(t *testing.T) {
	t.Parallel()

	config := DefaultPollConfig()
	seen := make(map[time.Duration]struct{})
	for range 200 {
		delay := backoffDelay(config, 1, equalJitter)
		if delay < config.BackoffInitial/2 || delay > config.BackoffInitial {
			t.Fatalf("delay = %v, want [%v, %v]", delay, config.BackoffInitial/2, config.BackoffInitial)
		}
		seen[delay] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("初回 backoff が %d 通りしか出ない: %v", len(seen), seen)
	}
}

// 同時実行上限による中断は列挙の失敗ではない。失敗へ潰すと、GitHub 側が健全でも
// last_success_at が凍り、poll の cadence が backoff へ置き換わる。
func TestCapacityInterruptionKeepsTheCycleIntervalAndLastSuccess(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	trigger.refuse[19] = 1_000_000
	clock := newFakeClock()
	clock.autoAdvance(t.Context(), DefaultPollConfig().CapacityRetryInterval)
	buffer := newSyncBuffer()
	config := testPollerConfig(discovery, trigger, clock)
	config.Logger = slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	poller := newTestPoller(t, config)

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.failed() {
		t.Fatalf("中断を列挙の失敗として扱った: %#v", report)
	}
	if poller.lastSuccess.IsZero() {
		t.Fatal("中断した cycle が最終成功時刻を更新していない")
	}
	record := findLast(logRecords(t, buffer), EventPollCycle)
	if record[telemetry.FieldOutcome] != string(OutcomePollInterrupted) {
		t.Fatalf("outcome = %v, want %q", record[telemetry.FieldOutcome], OutcomePollInterrupted)
	}
}

// 複数 repository が失敗したとき、最初の失敗ではなく最長の hint を待つ。
// 指示された時刻より早く再試行すると同じ失敗を確実に繰り返す。
func TestCycleWaitsForTheLongestRetryHintAcrossSources(t *testing.T) {
	t.Parallel()

	plain := &fakeDiscovery{candidateErr: []error{errors.New("GitHub 502")}}
	limited := &fakeDiscovery{candidateErr: []error{&stubRetryHint{after: 20 * time.Minute}}}
	trigger := newRecordingTrigger()
	config := testPollerConfig(plain, trigger, newFakeClock())
	config.Sources = append(config.Sources, PollSource{Repository: "mrbaron3/other", Discovery: limited})
	poller := newTestPoller(t, config)

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.Failures != 2 || report.RetryHint != 20*time.Minute {
		t.Fatalf("report = %#v, want failures 2 / hint 20m", report)
	}
	if got := applyRetryHint(poller.poll, backoffDelay(poller.poll, 1, poller.jitter), report.RetryHint); got != 20*time.Minute {
		t.Fatalf("待機 = %v, want 20m", got)
	}
}

// shutdown 済みの dispatcher は解除されない終端条件である。待って再試行しても
// GitHub の列挙を消費するだけなので、poller 自身が止まる。
func TestRunStopsWhenTheDispatcherIsPermanentlyStopped(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	trigger.stopped = true
	clock := newFakeClock()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	done := make(chan error, 1)
	go func() { done <- poller.Run(t.Context()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Run() error = %v, want ErrDispatcherStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停止した dispatcher に対して Run が戻らない")
	}
	if clock.pendingWaits() != 0 {
		t.Fatalf("終端条件で backoff を待った: %d", clock.pendingWaits())
	}
}

// poll.source の repository は telemetry.Issue と同じ規則で正規化する。揃わないと、
// 列挙の失敗と reconcile の結果が別 repository として集計される。
func TestPollSourceRepositoryIsCanonicalizedForCorrelation(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	buffer := newSyncBuffer()
	config := testPollerConfig(discovery, trigger, newFakeClock())
	config.Sources[0].Repository = "MrBaron3/Kudo"
	config.Logger = slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	poller := newTestPoller(t, config)

	poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	record := findRecord(t, logRecords(t, buffer), EventPollSource)
	if record[telemetry.FieldRepository] != "mrbaron3/kudo" {
		t.Fatalf("repository = %v, want mrbaron3/kudo", record[telemetry.FieldRepository])
	}
}

// 記録が途中で失敗した投影は、候補でも open PR でもない状態で残る。Kudo 所有の
// status label が付いたまま open であることだけが、その Issue を再発見できる観測である。
func TestPollCycleRediscoversIssuesLeftWithKudoOwnedLabels(t *testing.T) {
	t.Parallel()

	// 進行中 label が残る Issue（記録が途中で失敗した投影）と、完了 label と ready label を
	// 併せ持つ Issue（closed のままの再依頼）。どちらも候補でも open PR でもない。
	discovery := &fakeDiscovery{labeled: map[string][]contract.IssueRef{
		"ai-in-progress":     issueRefs(19),
		"ai-merged+ai-ready": issueRefs(24),
	}}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	var numbers []int
	for _, request := range trigger.snapshot() {
		numbers = append(numbers, request.Issue.Number)
	}
	slices.Sort(numbers)
	if report.Submitted != 2 || !slices.Equal(numbers, []int{19, 24}) {
		t.Fatalf("report = %#v, 投入 = %v", report, numbers)
	}
	slices.Sort(discovery.labeledFor)
	if !slices.Equal(discovery.labeledFor, []string{"ai-in-progress", "ai-merged+ai-ready"}) {
		t.Fatalf("列挙した label 条件 = %v", discovery.labeledFor)
	}
}

// stubRetryHint は GitHub が示した再試行間隔を持つ transport failure を模す。
type stubRetryHint struct{ after time.Duration }

func (e *stubRetryHint) Error() string { return "rate limited" }

func (e *stubRetryHint) RetryAfterHint(time.Time) (time.Duration, bool) {
	return e.after, e.after > 0
}

// rate limit が示す再試行時刻は backoff の下限になる。示された時刻より早く再試行すると
// 同じ失敗を確実に繰り返す。
func TestBackoffHonoursTheRetryHintAsALowerBound(t *testing.T) {
	t.Parallel()

	config := DefaultPollConfig()
	hint := config.BackoffMax + 5*time.Minute
	delay := applyRetryHint(config, config.BackoffInitial, hint)
	if delay != hint {
		t.Fatalf("delay = %v, want %v", delay, hint)
	}
	if got := applyRetryHint(config, config.BackoffMax, time.Second); got != config.BackoffMax {
		t.Fatalf("短い hint で backoff を縮めた: %v", got)
	}
}

// Scope が定める相関 field を固定する。Issue 本文、token、secret は載せない。
func TestPollCycleLogCarriesCorrelationFieldsWithoutSecrets(t *testing.T) {
	t.Parallel()

	// collaborator の error message に secret と Issue 本文を混ぜる。log へ message を
	// 載せる regression が入れば、この sentinel が現れる。sentinel を渡さない assertion は
	// 実装が何をしても成功するため、証拠にならない。
	leaky := errors.New("GitHub GET /issues failed: token=actor-token body=契約本文")
	discovery := &fakeDiscovery{
		candidates: issueRefs(19), runs: issueRefs(24),
		labeledErr: []error{leaky},
	}
	trigger := newRecordingTrigger()
	buffer := newSyncBuffer()
	config := testPollerConfig(discovery, trigger, newFakeClock())
	config.Logger = slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	config.Sources[0].RateLimit = func() (int, bool) { return 4321, true }
	poller := newTestPoller(t, config)

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if report.Failures != 1 {
		t.Fatalf("report = %#v, want 列挙の失敗を 1 件", report)
	}

	records := logRecords(t, buffer)
	source := findRecord(t, records, EventPollSource)
	if source[telemetry.FieldRepository] != "mrbaron3/kudo" {
		t.Fatalf("repository = %v", source[telemetry.FieldRepository])
	}
	if source[telemetry.FieldRateLimitRemaining] != float64(4321) {
		t.Fatalf("rate limit remaining = %v", source[telemetry.FieldRateLimitRemaining])
	}
	if source[telemetry.FieldErrorType] == nil {
		t.Fatalf("失敗した列挙に error type が無い: %v", source)
	}
	cycle := findRecord(t, records, EventPollCycle)
	triggerAttrs, ok := cycle[telemetry.FieldTrigger].(map[string]any)
	if !ok || triggerAttrs[telemetry.FieldTriggerID] != report.Trigger.ID {
		t.Fatalf("trigger = %v, want ID %q", cycle[telemetry.FieldTrigger], report.Trigger.ID)
	}
	for _, field := range []string{telemetry.FieldDurationMillis, telemetry.FieldBacklog, telemetry.FieldDiscovered} {
		if _, exists := cycle[field]; !exists {
			t.Fatalf("cycle log に %q が無い: %v", field, cycle)
		}
	}
	if _, exists := findLast(records, EventPollCycle)[telemetry.FieldLastSuccessAt]; !exists {
		t.Fatalf("2 cycle 目の log に最終成功時刻が無い: %v", findLast(records, EventPollCycle))
	}
	if text := string(buffer.Bytes()); strings.Contains(text, "actor-token") || strings.Contains(text, "契約本文") {
		t.Fatalf("log に secret / Issue 本文が混ざった: %s", text)
	}
}

// 上限に張り付いたまま cycle の持ち時間を使い切ったら、残りを backlog として記録して
// 終える。無期限に待つと、slot を占有したまま戻らない reconcile が polling を止める。
func TestPollCycleStopsWaitingForCapacityAtTheCycleDeadline(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24)}
	trigger := newRecordingTrigger()
	trigger.refuse[19] = 1_000_000
	clock := newFakeClock()
	clock.autoAdvance(t.Context(), DefaultPollConfig().CapacityRetryInterval)
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if !errors.Is(report.FirstErr, ErrPollCycleDeadline) {
		t.Fatalf("FirstErr = %v, want ErrPollCycleDeadline", report.FirstErr)
	}
	if report.Backlog != 2 || report.Submitted != 0 {
		t.Fatalf("report = %#v, want backlog 2", report)
	}
}

// 上限に張り付いた状態が続いても、次の cycle は投入し切れなかった位置から始める。
// 1 件ずつしか進めないと、発見件数が多いほど末尾の待ち時間が線形に伸びる。
func TestPollCycleResumesFromTheUnsubmittedPositionNextCycle(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24, 31, 42)}
	trigger := newRecordingTrigger()
	clock := newFakeClock()
	clock.autoAdvance(t.Context(), DefaultPollConfig().CapacityRetryInterval)
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	// 1 cycle 目は先頭 2 件だけ投入でき、31 で持ち時間を使い切る。
	trigger.refuse[31] = 1_000_000
	first := poller.runCycle(t.Context(), workflow.TriggerStartup)
	if first.Submitted != 2 || first.Backlog != 2 {
		t.Fatalf("1 cycle 目 = %#v, want submitted 2 / backlog 2", first)
	}
	delete(trigger.refuse, 31)

	before := trigger.count()
	poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	var second []int
	for _, request := range trigger.snapshot()[before:] {
		second = append(second, request.Issue.Number)
	}
	if len(second) == 0 || second[0] != 31 {
		t.Fatalf("2 cycle 目の投入順 = %v, want 31 から再開", second)
	}
}

// 起点が進んでも、1 cycle で投入し切れる限りは毎 cycle 全件が届く。
func TestPollCycleSubmitsEveryIssueEvenAfterRotating(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24, 31)}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	for range 3 {
		before := trigger.count()
		poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
		var submitted []int
		for _, request := range trigger.snapshot()[before:] {
			submitted = append(submitted, request.Issue.Number)
		}
		slices.Sort(submitted)
		if !slices.Equal(submitted, []int{19, 24, 31}) {
			t.Fatalf("投入 = %v, want 全件", submitted)
		}
	}
}

// hint に上限が無いと、clock skew や異常な Retry-After だけで回復経路が任意長停止する。
func TestRetryHintIsCappedSoPollingCannotStopIndefinitely(t *testing.T) {
	t.Parallel()

	config := DefaultPollConfig()
	got := applyRetryHint(config, config.BackoffInitial, 8*time.Hour)
	if got != MaxRetryHint {
		t.Fatalf("delay = %v, want %v", got, MaxRetryHint)
	}
}

// cycle 間隔は「開始間隔が interval」である。経過分を引かないと、持ち時間を使い切った
// cycle の直後だけ間隔が 2 倍になり、最も回収を急ぐべき状態で回収が遅くなる。
func TestScheduledPollSubtractsTheCycleDurationFromTheNextWait(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	trigger.refuse[19] = 1_000_000
	clock := newFakeClock()
	clock.autoAdvance(t.Context(), DefaultPollConfig().CapacityRetryInterval)
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, clock))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	// 持ち時間（interval）を使い切った cycle の直後の待機は 0 でなければならない。
	waitFor(t, func() bool {
		for _, delay := range clock.requestedDelays() {
			if delay == 0 {
				return true
			}
		}
		return false
	})
	cancel()
	<-done
}

// shutdown 中は新しい reconcile を起動しない。TriggerReconcile は呼び出し元の context を
// 意図的に無視するため、投入の直前に自分で確認する必要がある。
func TestPollCycleStopsSubmittingOnceTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19, 24, 31)}
	trigger := newRecordingTrigger()
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report := poller.runCycle(ctx, workflow.TriggerScheduledPoll)
	if trigger.count() != 0 || report.Submitted != 0 || report.Backlog != 3 {
		t.Fatalf("report = %#v, 投入 = %d", report, trigger.count())
	}
}

// 分類できない投入失敗を成功として扱わない。倒すと、回復経路が死んでいるのに
// last_success_at が進み続ける。
func TestUnclassifiedSubmitFailureCountsAsACycleFailure(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{candidates: issueRefs(19)}
	trigger := newRecordingTrigger()
	trigger.failWith = errors.New("reconcile trigger の実装が壊れている")
	poller := newTestPoller(t, testPollerConfig(discovery, trigger, newFakeClock()))

	report := poller.runCycle(t.Context(), workflow.TriggerScheduledPoll)
	if !report.failed() || !poller.lastSuccess.IsZero() {
		t.Fatalf("report = %#v, lastSuccess = %v", report, poller.lastSuccess)
	}
}

func TestNewPollerRejectsIncompleteWiring(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{}
	trigger := newRecordingTrigger()
	clock := newFakeClock()

	for name, mutate := range map[string]func(*PollerConfig){
		"source なし":     func(c *PollerConfig) { c.Sources = nil },
		"discovery なし":  func(c *PollerConfig) { c.Sources[0].Discovery = nil },
		"repository なし": func(c *PollerConfig) { c.Sources[0].Repository = "" },
		"trigger なし":    func(c *PollerConfig) { c.Trigger = nil },
		"clock なし":      func(c *PollerConfig) { c.Clock = nil },
		"interval が負":   func(c *PollerConfig) { c.Poll.Interval = -time.Second },
		"interval が短い":  func(c *PollerConfig) { c.Poll.Interval = time.Second },
		"filter が空":     func(c *PollerConfig) { c.Poll.Filter = workflow.CandidateFilter{} },
		"再発見条件が空":       func(c *PollerConfig) { c.Poll.RecoveryQueries = nil },
		"再発見条件に空 label": func(c *PollerConfig) { c.Poll.RecoveryQueries = [][]string{{""}} },
		"backoff の逆転":   func(c *PollerConfig) { c.Poll.BackoffMax = c.Poll.BackoffInitial - time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := testPollerConfig(discovery, trigger, clock)
			mutate(&config)
			if _, err := NewPoller(config); err == nil {
				t.Fatalf("不正な configuration を受理した: %s", name)
			}
		})
	}
}

func logRecords(t *testing.T, buffer *syncBuffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(buffer.Bytes())), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log 行を decode できない: %q", line)
		}
		records = append(records, record)
	}
	return records
}

func findRecord(t *testing.T, records []map[string]any, event string) map[string]any {
	t.Helper()
	for _, record := range records {
		if record[telemetry.FieldEvent] == event {
			return record
		}
	}
	t.Fatalf("event %q の log が無い", event)
	return nil
}

func findLast(records []map[string]any, event string) map[string]any {
	var found map[string]any
	for _, record := range records {
		if record[telemetry.FieldEvent] == event {
			found = record
		}
	}
	return found
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("条件が成立しなかった")
		}
		time.Sleep(time.Millisecond)
	}
}
