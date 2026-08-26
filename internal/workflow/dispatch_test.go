package workflow

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s が起きない", what)
	}
}

// AC-5: 同じ Run の同時 dispatch は一つの flight を共有し、依存のない別 Run は
// その flight の完了を待たずに進む。repository 全体を直列化しない。
func TestRunDispatcherCoalescesOneRunWithoutSerializingOthers(t *testing.T) {
	dispatcher := NewRunDispatcher()
	release := make(chan struct{})
	started := make(chan struct{})
	otherStarted := make(chan struct{})
	wantErr := errors.New("run-700 の結果")
	var primaryCalls, duplicateCalls atomic.Int32

	first := dispatcher.Dispatch("run-700", func() error {
		primaryCalls.Add(1)
		close(started)
		<-release
		return wantErr
	})
	waitForSignal(t, started, "run-700 の開始")

	second := dispatcher.Dispatch("run-700", func() error {
		duplicateCalls.Add(1)
		return nil
	})
	if first != second {
		t.Fatal("同じ Run の同時 dispatch が同じ flight を共有していない")
	}

	other := dispatcher.Dispatch("run-701", func() error {
		close(otherStarted)
		return nil
	})
	waitForSignal(t, otherStarted, "別 Run の開始")
	if err := other.Wait(); err != nil {
		t.Fatalf("run-701: %v", err)
	}

	close(release)
	if err := first.Wait(); !errors.Is(err, wantErr) {
		t.Fatalf("first wait = %v, want %v", err, wantErr)
	}
	if err := second.Wait(); !errors.Is(err, wantErr) {
		t.Fatalf("joined wait = %v, want %v", err, wantErr)
	}
	if primaryCalls.Load() != 1 || duplicateCalls.Load() != 0 {
		t.Fatalf("同一 Run の呼び出し = primary:%d duplicate:%d, want 1/0",
			primaryCalls.Load(), duplicateCalls.Load())
	}

	// 完了した flight は registry から外れ、次の Operation を開始できる。
	third := dispatcher.Dispatch("run-700", func() error { return nil })
	if third == first {
		t.Fatal("完了済み flight が次の Operation へ再利用された")
	}
	if err := third.Wait(); err != nil {
		t.Fatalf("third wait: %v", err)
	}
}

// 排他は Run 単位でなければならない。Run key を持たない dispatch を通すと、
// 無関係な Run が一つの flight へ畳まれる。
func TestRunDispatcherRejectsUnkeyedOrEmptyOperations(t *testing.T) {
	dispatcher := NewRunDispatcher()

	if err := dispatcher.Dispatch("", func() error { return nil }).Wait(); !errors.Is(err, ErrRunKeyRequired) {
		t.Fatalf("空 key の結果 = %v, want %v", err, ErrRunKeyRequired)
	}
	if err := dispatcher.Dispatch("run-700", nil).Wait(); !errors.Is(err, ErrOperationRequired) {
		t.Fatalf("nil Operation の結果 = %v, want %v", err, ErrOperationRequired)
	}
	// 失敗した dispatch が registry を汚さない。
	if err := dispatcher.Dispatch("run-700", func() error { return nil }).Wait(); err != nil {
		t.Fatalf("後続 dispatch: %v", err)
	}
}

// 多数の並行 dispatch が同じ Run へ来ても、state-advancing Operation は一度しか
// 走らない。競合を race detector 付きで固定する。
func TestRunDispatcherRunsOneFlightUnderConcurrentDispatch(t *testing.T) {
	dispatcher := NewRunDispatcher()
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	var dispatched, finished sync.WaitGroup

	primary := dispatcher.Dispatch("run-700", func() error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})
	waitForSignal(t, started, "run-700 の開始")

	const joiners = 32
	dispatched.Add(joiners)
	finished.Add(joiners)
	for range joiners {
		go func() {
			defer finished.Done()
			flight := dispatcher.Dispatch("run-700", func() error {
				calls.Add(1)
				return nil
			})
			// 進行中の flight へ join したことを確定させてから release する。
			dispatched.Done()
			if err := flight.Wait(); err != nil {
				t.Errorf("joined dispatch: %v", err)
			}
		}()
	}

	dispatched.Wait()
	close(release)
	finished.Wait()
	if err := primary.Wait(); err != nil {
		t.Fatalf("primary: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("state-advancing Operation の実行回数 = %d, want 1", got)
	}
}
