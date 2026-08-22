package workflow

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// AC-5: 同じ Run の同時 dispatch は一つの flight を共有し、別 Run はその flight が
// 完了する前に開始できる。repository 全体を直列化する lock は許さない。
func TestRunDispatcherCoalescesOneRunWithoutSerializingOtherRuns(t *testing.T) {
	dispatcher := NewRunDispatcher()
	releaseRunA := make(chan struct{})
	runAStarted := make(chan struct{})
	runBStarted := make(chan struct{})
	wantRunAErr := errors.New("run-a result")
	var runACalls atomic.Int32
	var duplicateCalls atomic.Int32

	first := dispatcher.Dispatch("run-a", func() error {
		runACalls.Add(1)
		close(runAStarted)
		<-releaseRunA
		return wantRunAErr
	})
	select {
	case <-runAStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("run-a が開始しない")
	}

	second := dispatcher.Dispatch("run-a", func() error {
		duplicateCalls.Add(1)
		return nil
	})
	if first != second {
		t.Fatal("同じ Run の同時 dispatch が同じ flight を共有していない")
	}

	runB := dispatcher.Dispatch("run-b", func() error {
		close(runBStarted)
		return nil
	})
	select {
	case <-runBStarted:
		// run-a を release する前に run-b が開始すれば global lock ではない。
	case <-time.After(2 * time.Second):
		t.Fatal("別 Run が run-a の完了待ちになった")
	}
	if err := runB.Wait(); err != nil {
		t.Fatalf("run-b: %v", err)
	}

	close(releaseRunA)
	if err := first.Wait(); !errors.Is(err, wantRunAErr) {
		t.Fatalf("first wait = %v, want %v", err, wantRunAErr)
	}
	if err := second.Wait(); !errors.Is(err, wantRunAErr) {
		t.Fatalf("joined wait = %v, want %v", err, wantRunAErr)
	}
	if runACalls.Load() != 1 || duplicateCalls.Load() != 0 {
		t.Fatalf("same-run calls = primary:%d duplicate:%d, want 1/0", runACalls.Load(), duplicateCalls.Load())
	}

	// 完了済み flight は process-local registry から外れ、後続 Operation を開始できる。
	third := dispatcher.Dispatch("run-a", func() error {
		runACalls.Add(1)
		return nil
	})
	if third == first {
		t.Fatal("完了済み flight が後続 Operation に再利用された")
	}
	if err := third.Wait(); err != nil {
		t.Fatalf("third wait: %v", err)
	}
	if runACalls.Load() != 2 {
		t.Fatalf("後続 Operation 後の call 数 = %d, want 2", runACalls.Load())
	}
}
