package workflow

import (
	"math"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type constantJitter time.Duration

func (jitter constantJitter) Apply(delay time.Duration) time.Duration {
	return delay + time.Duration(jitter)
}

func sampleRetryPolicy() RetryPolicy {
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

// AC-6: class ごとの基準値、指数 backoff、上限、注入した clock と jitter だけで
// retry schedule が決まり、sleep なしに検証できる。
func TestAttemptTrackerDerivesDeterministicClassSpecificBackoff(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	tracker, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{now: now}, constantJitter(250*time.Millisecond))
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}

	tests := []struct {
		operation string
		class     contract.FailureClass
		attempt   int
		delay     time.Duration
	}{
		{"run-700/implement", contract.FailureTimeout, 1, 1250 * time.Millisecond},
		{"run-700/implement", contract.FailureTimeout, 2, 2250 * time.Millisecond},
		{"run-700/implement", contract.FailureTimeout, 3, 4250 * time.Millisecond},
		// exponential 部分が MaxDelay で止まってから jitter を適用する。
		{"run-700/implement", contract.FailureTimeout, 4, 5250 * time.Millisecond},
		// attempt counter は logical Operation ごとに独立し、class ごとに基準値が違う。
		// 同じ Operation では class をまたいで counter を共有する。
		{"run-700/review", contract.FailureRateLimit, 1, 4250 * time.Millisecond},
		{"run-700/review", contract.FailureTimeout, 2, 2250 * time.Millisecond},
	}
	for _, testCase := range tests {
		schedule, nextErr := tracker.Next(testCase.operation, testCase.class)
		if nextErr != nil {
			t.Fatalf("Next(%s, %s): %v", testCase.operation, testCase.class, nextErr)
		}
		if schedule.Attempt != testCase.attempt || schedule.Delay != testCase.delay {
			t.Fatalf("schedule = %+v, want attempt=%d delay=%s",
				schedule, testCase.attempt, testCase.delay)
		}
		if want := now.Add(testCase.delay); !schedule.RetryAt.Equal(want) {
			t.Fatalf("retryAt = %s, want %s", schedule.RetryAt, want)
		}
	}
}

// attempt counter は process-local である。tracker を作り直すと失われ、新しい
// process は fresh attempt 1 から始まる。この喪失は durable state ではない。
func TestAttemptTrackerIsProcessLocal(t *testing.T) {
	newTracker := func() *AttemptTracker {
		t.Helper()
		tracker, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{}, NoJitter{})
		if err != nil {
			t.Fatalf("NewAttemptTracker: %v", err)
		}
		return tracker
	}

	first, err := newTracker().Next("run-700/review", contract.FailureProviderCrash)
	if err != nil {
		t.Fatalf("first process: %v", err)
	}
	second, err := newTracker().Next("run-700/review", contract.FailureProviderCrash)
	if err != nil {
		t.Fatalf("second process: %v", err)
	}
	if first.Attempt != 1 || second.Attempt != 1 || first.Delay != second.Delay {
		t.Fatalf("fresh process の attempt = %+v / %+v", first, second)
	}
}

// Operation が成功した時点で counter を落とせないと、同じ Run の次の Operation が
// 使い切った budget を引き継ぐ。
func TestAttemptTrackerForgetsCompletedOperations(t *testing.T) {
	tracker, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{}, NoJitter{})
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}
	if _, err := tracker.Next("run-700/implement", contract.FailureTimeout); err != nil {
		t.Fatalf("Next: %v", err)
	}
	tracker.Forget("run-700/implement")

	schedule, err := tracker.Next("run-700/implement", contract.FailureTimeout)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if schedule.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", schedule.Attempt)
	}
}

// policy は tracker が固定する。呼び出し側が構築後に map を書き換えても backoff は
// 変わらない。共有していると、検証を通っていない値で待ち、Next と競合すれば
// concurrent map access になる。
func TestAttemptTrackerFixesThePolicyAtConstruction(t *testing.T) {
	policy := sampleRetryPolicy()
	tracker, err := NewAttemptTracker(policy, fixedClock{}, NoJitter{})
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}
	policy.BaseDelay[contract.FailureTimeout] = time.Hour
	delete(policy.BaseDelay, contract.FailureRateLimit)

	schedule, err := tracker.Next("run-700/implement", contract.FailureTimeout)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if schedule.Delay != time.Second {
		t.Fatalf("delay = %s, want %s", schedule.Delay, time.Second)
	}
	if _, err := tracker.Next("run-700/review", contract.FailureRateLimit); err != nil {
		t.Fatalf("削除された class の backoff を失った: %v", err)
	}
}

// quality verdict や語彙外の値を retry class として受理しない。受理すると
// review 判断が backoff 対象の transport failure に化ける。
func TestAttemptTrackerRejectsUnknownFailureClass(t *testing.T) {
	tracker, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{}, NoJitter{})
	if err != nil {
		t.Fatalf("NewAttemptTracker: %v", err)
	}
	for _, class := range []contract.FailureClass{"", "request_changes", "quality_verdict"} {
		if _, err := tracker.Next("run-700/review", class); err == nil {
			t.Fatalf("class %q を受理した", class)
		}
	}
	if _, err := tracker.Next("", contract.FailureTimeout); err == nil {
		t.Fatal("空の Operation identity を受理した")
	}
}

// 語彙の一部だけを定義した policy は受理しない。未定義 class の暗黙の既定値は
// 「即座に retry する」へ倒れやすく、fail-open な待機になる。
func TestNewAttemptTrackerRejectsIncompletePolicy(t *testing.T) {
	partial := sampleRetryPolicy()
	delete(partial.BaseDelay, contract.FailureRateLimit)

	nonPositive := sampleRetryPolicy()
	nonPositive.BaseDelay[contract.FailureNetwork] = 0

	tooSmallMax := sampleRetryPolicy()
	tooSmallMax.MaxDelay = time.Millisecond

	for name, policy := range map[string]RetryPolicy{
		"class が欠けている":        partial,
		"基準値が正でない":            nonPositive,
		"MaxDelay が基準値より小さい":  tooSmallMax,
		"BaseDelay が設定されていない": {MaxDelay: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAttemptTracker(policy, fixedClock{}, NoJitter{}); err == nil {
				t.Fatal("不完全な policy を受理した")
			}
		})
	}

	if _, err := NewAttemptTracker(sampleRetryPolicy(), nil, NoJitter{}); err == nil {
		t.Fatal("clock の注入なしで tracker を作れてしまう")
	}
	if _, err := NewAttemptTracker(sampleRetryPolicy(), fixedClock{}, nil); err == nil {
		t.Fatal("jitter の注入なしで tracker を作れてしまう")
	}
}

// ProportionalJitter は待ち時間を縮める方向にだけ分散させる。乱数源が無い場合は
// 縮めない（待ちが短くなる方向へは倒さない）。
func TestProportionalJitterOnlyShortensWithinFraction(t *testing.T) {
	tests := []struct {
		name   string
		jitter ProportionalJitter
		delay  time.Duration
		want   time.Duration
	}{
		{"下限", ProportionalJitter{Fraction: 0.2, Float64: func() float64 { return 0 }},
			10 * time.Second, 8 * time.Second},
		// Float64 の契約は [0,1) なので上端は開区間である。契約内の最大値で境界を示す。
		{"上端", ProportionalJitter{
			Fraction: 0.2,
			Float64:  func() float64 { return math.Nextafter(1, 0) },
		}, 10 * time.Second, 10 * time.Second},
		{"中間", ProportionalJitter{Fraction: 0.5, Float64: func() float64 { return 0.5 }},
			10 * time.Second, 7500 * time.Millisecond},
		{"乱数源なし", ProportionalJitter{Fraction: 0.5}, 10 * time.Second, 10 * time.Second},
		{"fraction なし", ProportionalJitter{Float64: func() float64 { return 0 }},
			10 * time.Second, 10 * time.Second},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.jitter.Apply(testCase.delay); got != testCase.want {
				t.Fatalf("Apply(%s) = %s, want %s", testCase.delay, got, testCase.want)
			}
		})
	}
}

// 語彙の全 FailureClass に基準値を要求する検証が、contract 側の語彙追加へ
// 追従できることを固定する。
func TestFailureClassesAreExhaustive(t *testing.T) {
	classes := contract.FailureClasses()
	if len(classes) == 0 {
		t.Fatal("FailureClass の語彙が空である")
	}
	policy := sampleRetryPolicy()
	for _, class := range classes {
		if _, ok := policy.BaseDelay[class]; !ok {
			t.Fatalf("test policy が class %q を定義していない", class)
		}
	}
	if len(policy.BaseDelay) != len(classes) {
		t.Fatalf("policy の class 数 = %d, want %d", len(policy.BaseDelay), len(classes))
	}
}
