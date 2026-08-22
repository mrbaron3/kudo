package workflow

import (
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

type staticClock struct {
	now time.Time
}

func (clock staticClock) Now() time.Time { return clock.now }

type noJitter struct{}

func (noJitter) Apply(delay time.Duration) time.Duration { return delay }

type fixedJitter time.Duration

func (jitter fixedJitter) Apply(delay time.Duration) time.Duration {
	return delay + time.Duration(jitter)
}

func testRetryPolicy() RetryPolicy {
	return RetryPolicy{
		BaseDelay: map[contract.FailureClass]time.Duration{
			contract.FailureTimeout:                 time.Second,
			contract.FailureRateLimit:               4 * time.Second,
			contract.FailureNetwork:                 time.Second,
			contract.FailureProviderCrash:           2 * time.Second,
			contract.FailureProviderInvalidResponse: 2 * time.Second,
			contract.FailureGitHubTransport:         time.Second,
		},
		MaxDelay: 5 * time.Second,
	}
}

// AC-6: class ごとの基準値、指数 backoff、上限、注入 jitter/clock を使って
// sleep なしに retry schedule を決定できる。
func TestAttemptTrackerDerivesDeterministicClassSpecificBackoff(t *testing.T) {
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	tracker, err := NewAttemptTracker(testRetryPolicy(), staticClock{now: now}, fixedJitter(250*time.Millisecond))
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}

	tests := []struct {
		operation string
		class     contract.FailureClass
		attempt   int
		delay     time.Duration
	}{
		{"op-timeout", contract.FailureTimeout, 1, 1250 * time.Millisecond},
		{"op-timeout", contract.FailureTimeout, 2, 2250 * time.Millisecond},
		{"op-timeout", contract.FailureTimeout, 3, 4250 * time.Millisecond},
		// exponential 部分は MaxDelay で止まり、その後に jitter boundary を適用する。
		{"op-timeout", contract.FailureTimeout, 4, 5250 * time.Millisecond},
		// attempt counter は logical Operation ごとで、FailureClass の基準値も独立する。
		{"op-rate-limit", contract.FailureRateLimit, 1, 4250 * time.Millisecond},
	}
	for _, tc := range tests {
		schedule, nextErr := tracker.Next(tc.operation, tc.class)
		if nextErr != nil {
			t.Fatalf("Next(%s, %s): %v", tc.operation, tc.class, nextErr)
		}
		if schedule.Attempt != tc.attempt || schedule.Delay != tc.delay {
			t.Fatalf("schedule = %+v, want attempt=%d delay=%s", schedule, tc.attempt, tc.delay)
		}
		if want := now.Add(tc.delay); !schedule.RetryAt.Equal(want) {
			t.Fatalf("retryAt = %s, want %s", schedule.RetryAt, want)
		}
	}
}

// attempt は process-local であり、tracker を作り直すと失われる。これは durable phase や
// review round と混同せず、新 process の fresh attempt 1 から再開する契約である。
func TestAttemptTrackerIsProcessLocal(t *testing.T) {
	newTracker := func() *AttemptTracker {
		tracker, err := NewAttemptTracker(testRetryPolicy(), staticClock{}, noJitter{})
		if err != nil {
			t.Fatalf("NewAttemptTracker: %v", err)
		}
		return tracker
	}

	first, err := newTracker().Next("run-70/review", contract.FailureProviderCrash)
	if err != nil {
		t.Fatalf("first process: %v", err)
	}
	second, err := newTracker().Next("run-70/review", contract.FailureProviderCrash)
	if err != nil {
		t.Fatalf("second process: %v", err)
	}
	if first.Attempt != 1 || second.Attempt != 1 || first.Delay != second.Delay {
		t.Fatalf("fresh process の attempt = %+v / %+v", first, second)
	}
}

func TestAttemptTrackerRejectsUnknownFailureClass(t *testing.T) {
	tracker, err := NewAttemptTracker(testRetryPolicy(), staticClock{}, noJitter{})
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}
	if _, err := tracker.Next("run-70/review", contract.FailureClass("quality_verdict")); err == nil {
		t.Fatal("quality verdict を retry class として受理した")
	}
}
