package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

func webhookRequest(deliveryID, action string) workflow.ReconcileRequest {
	return workflow.ReconcileRequest{
		Issue: contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18},
		Trigger: workflow.Trigger{
			Source: workflow.TriggerWebhookDelivery,
			ID:     deliveryID,
			Action: action,
		},
	}
}

// syncBuffer は reconcile goroutine が書く log を test から安全に読むための sink である。
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func newTestDispatcher(t *testing.T, maxInFlight int, reconcile ReconcileIssue) (*TriggerDispatcher, *syncBuffer) {
	t.Helper()

	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dispatcher, err := NewTriggerDispatcher(reconcile, TriggerDispatcherConfig{MaxInFlight: maxInFlight}, logger)
	if err != nil {
		t.Fatalf("NewTriggerDispatcher() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	})
	return dispatcher, logs
}

func TestNewTriggerDispatcherRejectsIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	reconcile := func(context.Context, workflow.ReconcileRequest) error { return nil }
	if _, err := NewTriggerDispatcher(nil, TriggerDispatcherConfig{MaxInFlight: 1}, nil); err == nil {
		t.Error("NewTriggerDispatcher(nil reconcile) = nil error, want error")
	}
	if _, err := NewTriggerDispatcher(reconcile, TriggerDispatcherConfig{MaxInFlight: 0}, nil); err == nil {
		t.Error("NewTriggerDispatcher(0 in-flight) = nil error, want error")
	}
	if _, err := NewTriggerDispatcher(reconcile, TriggerDispatcherConfig{MaxInFlight: -1}, nil); err == nil {
		t.Error("NewTriggerDispatcher(negative in-flight) = nil error, want error")
	}
}

// webhook は低遅延経路である。trigger は reconcile の完了を待たずに戻らなければならない。
func TestTriggerReconcileReturnsBeforeReconcileCompletes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	observed := make(chan workflow.ReconcileRequest, 1)
	dispatcher, _ := newTestDispatcher(t, 2, func(_ context.Context, request workflow.ReconcileRequest) error {
		<-release
		observed <- request
		return nil
	})

	request := webhookRequest("delivery-1", "opened")
	if err := dispatcher.TriggerReconcile(t.Context(), request); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	select {
	case got := <-observed:
		t.Fatalf("TriggerReconcile() waited for reconcile completion: %+v", got)
	default:
	}

	close(release)
	select {
	case got := <-observed:
		if got != request {
			t.Errorf("reconcile received %+v, want %+v", got, request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile was never invoked")
	}
}

// reconcile は HTTP request の lifetime から切り離す。request context を引き継ぐと、
// 応答した瞬間に進行中の reconcile が cancel される。
func TestReconcileContextOutlivesTheCaller(t *testing.T) {
	t.Parallel()

	live := make(chan error, 1)
	dispatcher, _ := newTestDispatcher(t, 1, func(ctx context.Context, _ workflow.ReconcileRequest) error {
		live <- ctx.Err()
		return nil
	})

	callerCtx, cancelCaller := context.WithCancel(t.Context())
	if err := dispatcher.TriggerReconcile(callerCtx, webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	cancelCaller()
	select {
	case err := <-live:
		if err != nil {
			t.Errorf("reconcile context error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile was never invoked")
	}
}

// 呼び出し元の cancel は reconcile の要否と無関係である。webhook client が切断した
// ことを理由に trigger を落とすと、live state の変化が観測されないまま残る。
func TestTriggerReconcileDispatchesEvenWhenTheCallerContextIsCanceled(t *testing.T) {
	t.Parallel()

	observed := make(chan workflow.ReconcileRequest, 1)
	dispatcher, _ := newTestDispatcher(t, 1, func(_ context.Context, request workflow.ReconcileRequest) error {
		observed <- request
		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := webhookRequest("delivery-1", "opened")
	if err := dispatcher.TriggerReconcile(ctx, request); err != nil {
		t.Fatalf("TriggerReconcile(canceled caller) error = %v, want nil", err)
	}
	select {
	case got := <-observed:
		if got != request {
			t.Errorf("reconcile received %+v, want %+v", got, request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile was never invoked")
	}
}

func TestTriggerReconcileRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	dispatcher, _ := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		calls.Add(1)
		return nil
	})

	err := dispatcher.TriggerReconcile(t.Context(), workflow.ReconcileRequest{})
	if !errors.Is(err, workflow.ErrInvalidReconcileRequest) {
		t.Fatalf("TriggerReconcile(zero request) error = %v, want ErrInvalidReconcileRequest", err)
	}
	if calls.Load() != 0 {
		t.Errorf("reconcile invoked %d times, want 0", calls.Load())
	}
}

// 同時実行の上限は Controller が持つ。上限超過の delivery は落とすが、webhook 欠落は
// polling が回収するため、これは安全な degradation であって escalation ではない。
func TestTriggerReconcileDropsDeliveriesBeyondCapacity(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var calls atomic.Int64
	dispatcher, logs := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		calls.Add(1)
		<-release
		return nil
	})

	if err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile(first) error = %v", err)
	}
	err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-2", "labeled"))
	if !errors.Is(err, ErrDispatcherAtCapacity) {
		t.Fatalf("TriggerReconcile(second) error = %v, want ErrDispatcherAtCapacity", err)
	}
	close(release)

	waitForLogRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeTriggerDropped) &&
			triggerID(record) == "delivery-2"
	})
}

// 重複配送は観測の再実行になるだけである。adapter 側に受信記録を持たせない。
func TestTriggerReconcileDoesNotDeduplicateDeliveries(t *testing.T) {
	t.Parallel()

	observed := make(chan workflow.ReconcileRequest, 4)
	dispatcher, _ := newTestDispatcher(t, 4, func(_ context.Context, request workflow.ReconcileRequest) error {
		observed <- request
		return nil
	})

	duplicate := webhookRequest("delivery-1", "opened")
	for range 2 {
		if err := dispatcher.TriggerReconcile(t.Context(), duplicate); err != nil {
			t.Fatalf("TriggerReconcile() error = %v", err)
		}
	}
	for range 2 {
		select {
		case got := <-observed:
			if got != duplicate {
				t.Errorf("reconcile received %+v, want %+v", got, duplicate)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("duplicate delivery did not re-run reconcile")
		}
	}
}

func TestShutdownWaitsForInFlightReconcileAndStopsAcceptingTriggers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	finished := make(chan struct{})
	dispatcher, _ := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		<-release
		close(finished)
		return nil
	})

	if err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		shutdownDone <- dispatcher.Shutdown(ctx)
	}()

	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown() did not return")
	}
	select {
	case <-finished:
	default:
		t.Error("Shutdown() returned before the in-flight reconcile finished")
	}

	err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-2", "opened"))
	if !errors.Is(err, ErrDispatcherStopped) {
		t.Errorf("TriggerReconcile(after shutdown) error = %v, want ErrDispatcherStopped", err)
	}
}

func TestShutdownCancelsReconcileWhenGracePeriodExpires(t *testing.T) {
	t.Parallel()

	canceled := make(chan struct{})
	dispatcher, _ := newTestDispatcher(t, 1, func(ctx context.Context, _ workflow.ReconcileRequest) error {
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})

	if err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Millisecond)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown(expired grace) error = %v, want DeadlineExceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight reconcile was not canceled after the grace period")
	}
}

// reconcile の panic は ingress process を落とさない。落とすと、一つの Issue の
// 導出 bug が全 repository の低遅延経路を止める。
func TestReconcilePanicIsContainedAndReleasesCapacity(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	done := make(chan struct{}, 2)
	dispatcher, logs := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		defer func() { done <- struct{}{} }()
		if calls.Add(1) == 1 {
			panic("derivation bug")
		}
		return nil
	})

	if err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile(first) error = %v", err)
	}
	<-done
	waitForCapacity(t, dispatcher, webhookRequest("delivery-2", "opened"))
	<-done

	waitForLogRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeReconcilePanicked) &&
			triggerID(record) == "delivery-1"
	})
}

// waitForCapacity は「slot が解放されたこと」という条件を待つ。経過時間を入力にせず、
// deadline は test を hang させないための watchdog としてだけ使う。slot の解放は
// reconcile goroutine の defer で起きるため、test 側に同期点を作る手段が無い。
func waitForCapacity(t *testing.T, dispatcher *TriggerDispatcher, request workflow.ReconcileRequest) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := dispatcher.TriggerReconcile(t.Context(), request)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrDispatcherAtCapacity) || time.Now().After(deadline) {
			t.Fatalf("TriggerReconcile() error = %v, want capacity to be released", err)
		}
	}
}

// reconcile の失敗は trigger 側の応答に影響しないが、log では delivery と Issue に
// 相関できなければ運用で追えない。
func TestReconcileFailureIsLoggedWithCorrelationFields(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	dispatcher, logs := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		defer close(done)
		return errors.New("reconcile failed")
	})

	if err := dispatcher.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	<-done

	record := waitForLogRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeReconcileFailed)
	})
	issue, ok := record[telemetry.FieldIssue].(map[string]any)
	if !ok || issue[telemetry.FieldIssueNumber] != float64(18) {
		t.Errorf("record %v does not correlate the Issue", record)
	}
	trigger, ok := record[telemetry.FieldTrigger].(map[string]any)
	if !ok || trigger[telemetry.FieldTriggerID] != "delivery-1" {
		t.Errorf("record %v does not correlate the delivery", record)
	}
}

// waitForLogRecord は非同期に書かれる log record を polling で待つ。log の出力は
// reconcile goroutine 側で起きるため、test 側の観測点と同期していない。
func waitForLogRecord(t *testing.T, logs *syncBuffer, match func(map[string]any) bool) map[string]any {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		captured := logs.Bytes()
		for _, line := range bytes.Split(bytes.TrimSpace(captured), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("log line is not JSON: %v (%s)", err, line)
			}
			if match(record) {
				return record
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no matching log record in %s", captured)
		}
		time.Sleep(time.Millisecond)
	}
}

func triggerID(record map[string]any) string {
	trigger, ok := record[telemetry.FieldTrigger].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := trigger[telemetry.FieldTriggerID].(string)
	return id
}

// ReconcileIssue は注入された collaborator である。error message と panic 値には
// GitHub の response 本文や Issue の非公開本文が入り得るため、型と stack だけを残す。
func TestReconcileFailureAndPanicRecordTypeAndStackWithoutMessages(t *testing.T) {
	t.Parallel()

	const leak = "issue prose and /run/secrets/kudo_token"
	failed := make(chan struct{})
	failing, failingLogs := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		defer close(failed)
		return errors.New(leak)
	})
	if err := failing.TriggerReconcile(t.Context(), webhookRequest("delivery-1", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	<-failed
	record := waitForLogRecord(t, failingLogs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeReconcileFailed)
	})
	if record[telemetry.FieldErrorType] != "*errors.errorString" {
		t.Errorf("record %v does not carry the error type", record)
	}
	if bytes.Contains(failingLogs.Bytes(), []byte(leak)) {
		t.Errorf("logs %s contain the reconcile error message", failingLogs.Bytes())
	}

	panicked := make(chan struct{})
	panicking, panicLogs := newTestDispatcher(t, 1, func(context.Context, workflow.ReconcileRequest) error {
		defer close(panicked)
		panic(leak)
	})
	if err := panicking.TriggerReconcile(t.Context(), webhookRequest("delivery-2", "opened")); err != nil {
		t.Fatalf("TriggerReconcile() error = %v", err)
	}
	<-panicked
	record = waitForLogRecord(t, panicLogs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeReconcilePanicked)
	})
	stack, _ := record[telemetry.FieldStack].(string)
	if !strings.Contains(stack, "internal/controller") {
		t.Errorf("record %v does not carry the panic stack", record)
	}
	if bytes.Contains(panicLogs.Bytes(), []byte(leak)) {
		t.Errorf("logs %s contain the panic value", panicLogs.Bytes())
	}
}
