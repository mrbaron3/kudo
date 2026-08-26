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

	first, startedFirst := dispatcher.Dispatch("run-700", func() error {
		primaryCalls.Add(1)
		close(started)
		<-release
		return wantErr
	})
	if !startedFirst {
		t.Fatal("最初の dispatch が Operation を開始していない")
	}
	waitForSignal(t, started, "run-700 の開始")

	second, startedSecond := dispatcher.Dispatch("run-700", func() error {
		duplicateCalls.Add(1)
		return nil
	})
	if first != second {
		t.Fatal("同じ Run の同時 dispatch が同じ flight を共有していない")
	}
	// join した側は自分の Operation を実行していない。結果を自分のものとして扱えない
	// ことが、この真偽値で呼び出し側に見える必要がある。
	if startedSecond {
		t.Fatal("join した dispatch が Operation を開始したと報告した")
	}

	other, startedOther := dispatcher.Dispatch("run-701", func() error {
		close(otherStarted)
		return nil
	})
	if !startedOther {
		t.Fatal("別 Run の dispatch が開始されていない")
	}
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
	third, startedThird := dispatcher.Dispatch("run-700", func() error { return nil })
	if !startedThird {
		t.Fatal("完了後の dispatch が Operation を開始していない")
	}
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

	emptyKey, started := dispatcher.Dispatch("", func() error { return nil })
	if err := emptyKey.Wait(); !errors.Is(err, ErrRunKeyRequired) || started {
		t.Fatalf("空 key の結果 = %v (started=%v), want %v", err, started, ErrRunKeyRequired)
	}
	missingOperation, started := dispatcher.Dispatch("run-700", nil)
	if err := missingOperation.Wait(); !errors.Is(err, ErrOperationRequired) || started {
		t.Fatalf("nil Operation の結果 = %v (started=%v), want %v", err, started, ErrOperationRequired)
	}
	// 失敗した dispatch が registry を汚さない。
	accepted, started := dispatcher.Dispatch("run-700", func() error { return nil })
	if err := accepted.Wait(); err != nil || !started {
		t.Fatalf("後続 dispatch = %v (started=%v)", err, started)
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

	primary, _ := dispatcher.Dispatch("run-700", func() error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})
	waitForSignal(t, started, "run-700 の開始")
	var joined atomic.Int32

	const joiners = 32
	dispatched.Add(joiners)
	finished.Add(joiners)
	for range joiners {
		go func() {
			defer finished.Done()
			flight, started := dispatcher.Dispatch("run-700", func() error {
				calls.Add(1)
				return nil
			})
			if !started {
				joined.Add(1)
			}
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
	if got := joined.Load(); got != joiners {
		t.Fatalf("join した dispatch = %d, want %d", got, joiners)
	}
}
