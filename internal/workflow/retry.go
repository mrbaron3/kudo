package workflow

import (
	"fmt"
	"sync"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

// Clock は attempt 管理が時刻を読む唯一の境界である。
// 導出も backoff 判定も time.Now を直接呼ばない。呼ぶと、待ち時間の検証に実時間の
// 経過が必要になり、test が sleep 込みでしか書けなくなる。
type Clock interface {
	Now() time.Time
}

// Jitter は確定した backoff へ分散を与える。実装は待ち時間を伸ばす向きにも
// 縮める向きにも使えるが、負の delay を返してはならない。
type Jitter interface {
	Apply(time.Duration) time.Duration
}

// NoJitter は分散を与えない Jitter である。test と、単一 instance で thundering herd が
// 起きない deployment のための既定である。
type NoJitter struct{}

func (NoJitter) Apply(delay time.Duration) time.Duration { return delay }

// ProportionalJitter は delay を [(1-Fraction)*delay, delay] の範囲へ縮める。
//
// Float64 は [0,1) の乱数源であり、注入する。乱数源が無い、または Fraction が
// 範囲外の場合は delay をそのまま返す。分散が無い側へ倒すのは、設定漏れが
// 「待ち時間が短くなる」方向へ倒れると rate limit を悪化させるためである。
type ProportionalJitter struct {
	Fraction float64
	Float64  func() float64
}

func (j ProportionalJitter) Apply(delay time.Duration) time.Duration {
	if j.Float64 == nil || j.Fraction <= 0 || j.Fraction > 1 {
		return delay
	}
	shrink := j.Fraction * (1 - j.Float64())
	if shrink < 0 {
		shrink = 0
	}
	return delay - time.Duration(float64(delay)*shrink)
}

// RetryPolicy は retry 可能な failure class ごとの backoff を固定する。
//
// class ごとに基準値を分けるのは、rate limit と network timeout が同じ間隔で
// 再試行してよい失敗ではないためである。
type RetryPolicy struct {
	// BaseDelay は class ごとの初回 backoff である。語彙の全 class を明示的に
	// 持たない policy は受理しない。未定義 class の暗黙の既定は「即座に retry」へ
	// 倒れやすく、fail-open な待機になる。
	BaseDelay map[contract.FailureClass]time.Duration
	// MaxDelay は指数部分の上限である。jitter は上限適用の後に載せる。
	MaxDelay time.Duration
}

// Validate は policy が語彙を網羅し、待ち時間が正であることを検証する。
func (p RetryPolicy) Validate() error {
	if p.MaxDelay <= 0 {
		return fmt.Errorf("retry policy の maxDelay は正でなければならない: %s", p.MaxDelay)
	}
	classes := contract.FailureClasses()
	for _, class := range classes {
		delay, ok := p.BaseDelay[class]
		if !ok {
			return fmt.Errorf("retry policy が failure class %q の基準値を持たない", class)
		}
		if delay <= 0 {
			return fmt.Errorf("failure class %q の基準値は正でなければならない: %s", class, delay)
		}
		if delay > p.MaxDelay {
			return fmt.Errorf("failure class %q の基準値が maxDelay を超えている: %s > %s",
				class, delay, p.MaxDelay)
		}
	}
	if len(p.BaseDelay) != len(classes) {
		return fmt.Errorf("retry policy が語彙外の failure class を含む: %d 件定義、語彙は %d 件",
			len(p.BaseDelay), len(classes))
	}
	return nil
}

// RetrySchedule は次の attempt をいつ始めるかである。
type RetrySchedule struct {
	// Attempt は同じ logical Operation で数えた今回の再試行番号（初回 failure 後が 1）である。
	Attempt int
	Delay   time.Duration
	RetryAt time.Time
}

// AttemptTracker は logical Operation ごとの attempt 数を process-local に持つ。
//
// durable な保存を持たない。attempt counter は「今この process が何回試したか」で
// あって workflow 状態ではなく、保存すると process 再起動が phase 導出へ影響する
// （ADR-0001）。喪失時は fresh attempt から数え直し、無人 loop の外側の防波堤は
// review round 予算と escalation が担う。
//
// 並行 Operation から安全に呼べる。
type AttemptTracker struct {
	policy RetryPolicy
	clock  Clock
	jitter Jitter

	mu       sync.Mutex
	attempts map[string]int
}

// NewAttemptTracker は policy と注入した境界を検証して tracker を返す。
func NewAttemptTracker(policy RetryPolicy, clock Clock, jitter Jitter) (*AttemptTracker, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		return nil, fmt.Errorf("attempt tracker には clock の注入が必要である")
	}
	if jitter == nil {
		return nil, fmt.Errorf("attempt tracker には jitter の注入が必要である")
	}
	return &AttemptTracker{
		policy:   policy,
		clock:    clock,
		jitter:   jitter,
		attempts: map[string]int{},
	}, nil
}

// Next は logical Operation の attempt を 1 つ進め、次の retry schedule を返す。
//
// attempt counter は operationID ごとに数え、class をまたいで共有する。同じ
// Operation 内の timeout と rate limit が別 counter になると、交互に失敗する
// Operation が retry budget を消費しないまま回り続ける。backoff の基準値だけが
// class ごとに違う。
//
// 語彙外の failure class は受理しない。受理すると review の品質 verdict が
// backoff 対象の transport failure として扱われる余地が生まれる。
func (t *AttemptTracker) Next(operationID string, class contract.FailureClass) (RetrySchedule, error) {
	if operationID == "" {
		return RetrySchedule{}, fmt.Errorf("attempt を数える logical Operation identity が空である")
	}
	base, ok := t.policy.BaseDelay[class]
	if !ok {
		return RetrySchedule{}, fmt.Errorf("retry 対象の failure class ではない: %q", class)
	}

	t.mu.Lock()
	t.attempts[operationID]++
	attempt := t.attempts[operationID]
	t.mu.Unlock()

	delay := t.jitter.Apply(exponentialDelay(base, attempt, t.policy.MaxDelay))
	if delay < 0 {
		delay = 0
	}
	return RetrySchedule{Attempt: attempt, Delay: delay, RetryAt: t.clock.Now().Add(delay)}, nil
}

// Forget は完了した logical Operation の counter を落とす。
// 落とさないと、同じ Run の次の Operation が使い切った budget を引き継ぐ。
func (t *AttemptTracker) Forget(operationID string) {
	t.mu.Lock()
	delete(t.attempts, operationID)
	t.mu.Unlock()
}

// exponentialDelay は base を attempt-1 回倍にし、max で頭打ちにする。
// shift 演算ではなく加算 loop なのは、attempt が増えても overflow で待ち時間が
// 負や 0 へ折り返さないようにするためである。
func exponentialDelay(base time.Duration, attempt int, max time.Duration) time.Duration {
	delay := base
	for range attempt - 1 {
		if delay >= max-delay {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}
