package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// polling の既定値と最低値。
//
// 間隔が15分なのは、polling が低遅延経路ではなく webhook 欠落の回復経路だからである
// （docs/spec/05_design/04_github-routing.md の Polling fallback）。rate limit を理由に
// この既定を変えない。最低値を置くのは、短い間隔が回復を速めずに rate limit 予算を
// 消費し、webhook 経路まで巻き込んで遅くするためである。
const (
	DefaultPollInterval = 15 * time.Minute
	MinPollInterval     = time.Minute

	DefaultPollBackoffInitial = 30 * time.Second
	DefaultPollBackoffMax     = 15 * time.Minute
	MinPollBackoff            = time.Second

	// DefaultCapacityRetryInterval は同時実行上限で落ちた IssueRef を再投入するまでの待機である。
	DefaultCapacityRetryInterval = 5 * time.Second
	MinCapacityRetryInterval     = time.Second
)

// log の event 語彙。
const (
	EventPollCycle  = "poll.cycle"
	EventPollSource = "poll.source"
)

// ErrPollCycleDeadline は同時実行上限の待機が cycle の持ち時間を超えたことを表す。
// 失敗ではなく中断であり、残りは backlog として次の cycle が再発見する。
var ErrPollCycleDeadline = errors.New("poll cycle が同時実行上限の待機で持ち時間を使い切った")

// poll の結果分類。
const (
	OutcomePollCompleted   Outcome = "poll_completed"
	OutcomePollFailed      Outcome = "poll_failed"
	OutcomeSourceListed    Outcome = "poll_source_listed"
	OutcomeSourceFailed    Outcome = "poll_source_failed"
	OutcomePollInterrupted Outcome = "poll_interrupted"
)

// Clock は scheduler と backoff が使う時刻境界である。
//
// time package を直接呼ばないのは、15 分 interval と backoff の検証を実時間の sleep
// なしに行うためである。After は d 経過後に一度だけ値を送る channel を返す。
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Discovery は一つの configured repository に対する候補列挙の境界である。
//
// 返るのは identity だけである。polling の query result は authority ではなく、
// Issue Contract は claim 直前の live read から compile する
// （docs/spec/05_design/04_github-routing.md の Polling fallback）。
type Discovery interface {
	// ListCandidateIssueRefs は open / non-PR / target assignee / ready label を満たす
	// Issue を pagination して列挙する。
	ListCandidateIssueRefs(ctx context.Context, filter workflow.CandidateFilter) ([]contract.IssueRef, error)
	// ListOpenRunIssueRefs は open な kudo Pull Request を持つ Issue を列挙する。
	// 途中 phase の Run は候補条件を満たさないため、この列挙だけが再開の入口になる。
	ListOpenRunIssueRefs(ctx context.Context) ([]contract.IssueRef, error)
}

// ReconcileTrigger は poll 結果を ReconcileIssue へ投入する境界である。
// webhook adapter と同じ実装（TriggerDispatcher）を共有する。
type ReconcileTrigger interface {
	TriggerReconcile(ctx context.Context, request workflow.ReconcileRequest) error
}

// PollSource は一つの configured repository の列挙対象である。
type PollSource struct {
	// Repository は log 相関のための`owner/name`表記である。列挙が失敗して IssueRef が
	// 一つも得られない場合、この値だけが「どの repository が落ちたか」を示す。
	Repository string
	Discovery  Discovery
	// RateLimit は直近に観測した rate limit 残量を返す任意の hook である。
	// nil のとき log から field を省く。gateway の観測を Controller の型へ写す責務は
	// composition root にあり、Discovery の必須 method にはしない。
	RateLimit func() (remaining int, observed bool)
}

// PollConfig は polling の scheduler 設定である。起動時に strict validation する。
type PollConfig struct {
	Interval              time.Duration
	BackoffInitial        time.Duration
	BackoffMax            time.Duration
	CapacityRetryInterval time.Duration
	Filter                workflow.CandidateFilter
}

// DefaultPollConfig は spec が定める既定値を返す。
func DefaultPollConfig() PollConfig {
	return PollConfig{
		Interval:              DefaultPollInterval,
		BackoffInitial:        DefaultPollBackoffInitial,
		BackoffMax:            DefaultPollBackoffMax,
		CapacityRetryInterval: DefaultCapacityRetryInterval,
		Filter:                workflow.DefaultCandidateFilter(),
	}
}

// Validate は不正な duration を warning で継続せずに拒否する
// （docs/spec/05_design/03_runtime-platform.md の Configuration contract）。
func (c PollConfig) Validate() error {
	if c.Interval < MinPollInterval {
		return fmt.Errorf("poll interval は %s 以上でなければならない: %s", MinPollInterval, c.Interval)
	}
	if c.BackoffInitial < MinPollBackoff {
		return fmt.Errorf("poll backoff の初期値は %s 以上でなければならない: %s", MinPollBackoff, c.BackoffInitial)
	}
	if c.BackoffMax < c.BackoffInitial {
		return fmt.Errorf("poll backoff の上限が初期値を下回っている: %s < %s", c.BackoffMax, c.BackoffInitial)
	}
	if c.CapacityRetryInterval < MinCapacityRetryInterval {
		return fmt.Errorf("capacity 再投入間隔は %s 以上でなければならない: %s",
			MinCapacityRetryInterval, c.CapacityRetryInterval)
	}
	if c.CapacityRetryInterval > c.Interval {
		return fmt.Errorf("capacity 再投入間隔が poll interval を超えている: %s > %s",
			c.CapacityRetryInterval, c.Interval)
	}
	return c.Filter.Validate()
}

// PollerConfig は poller が束縛する collaborator と設定である。
type PollerConfig struct {
	Sources []PollSource
	Trigger ReconcileTrigger
	Clock   Clock
	Poll    PollConfig
	// Jitter は backoff 幅を散らす関数である。nil のとき full jitter を使う。
	Jitter func(time.Duration) time.Duration
	Logger *slog.Logger
}

// Poller は startup reconciliation と定期 poll を回し、発見した IssueRef を
// ReconcileIssue へ投入する薄い producer である。
//
// claim、dependency 判定、label 遷移の business logic をここへ置かない
// （docs/spec/05_design/04_github-routing.md の Polling fallback）。cycle は逐次であり、
// 前の cycle が終わるまで次を始めない。重複した発見は観測の再実行になるだけで、
// 二重 Run は branch ref create の atomicity と marker が防ぐ。
type Poller struct {
	sources []PollSource
	trigger ReconcileTrigger
	clock   Clock
	poll    PollConfig
	jitter  func(time.Duration) time.Duration
	logger  *slog.Logger

	// cycles と lastSuccess は Run の単一 goroutine だけが触る。Run を並行に呼ぶと
	// cycle が重なり、poller の逐次性という前提自体が崩れる。
	cycles      int
	lastSuccess time.Time
}

// rotation は cycle ごとの投入開始位置を返す。
func (p *Poller) rotation(size int) int {
	if size <= 0 {
		return 0
	}
	return (p.cycles - 1) % size
}

// NewPoller は collaborator と設定を検証した poller を返す。
func NewPoller(config PollerConfig) (*Poller, error) {
	if len(config.Sources) == 0 {
		return nil, errors.New("poll 対象の repository が無い")
	}
	for _, source := range config.Sources {
		if strings.TrimSpace(source.Repository) == "" {
			return nil, errors.New("poll source の repository identity は必須")
		}
		if source.Discovery == nil {
			return nil, fmt.Errorf("poll source %q の Discovery は必須", source.Repository)
		}
	}
	if config.Trigger == nil {
		return nil, errors.New("ReconcileTrigger は必須")
	}
	if config.Clock == nil {
		return nil, errors.New("Clock は必須")
	}
	if err := config.Poll.Validate(); err != nil {
		return nil, err
	}
	jitter := config.Jitter
	if jitter == nil {
		jitter = fullJitter
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		sources: append([]PollSource(nil), config.Sources...),
		trigger: config.Trigger,
		clock:   config.Clock,
		poll:    config.Poll,
		jitter:  jitter,
		logger:  logger,
	}, nil
}

// cycleReport は 1 回の poll cycle の観測である。log と test の観測点にする。
type cycleReport struct {
	Trigger workflow.Trigger
	// Discovered は重複排除後の IssueRef 件数である。
	Discovered int
	Submitted  int
	// Backlog は cycle を中断した時点で投入できていない IssueRef 件数である。
	// 監視対象であり、増え続ける状態は capacity か GitHub 側の異常を示す。
	Backlog int
	// CapacityWaits は同時実行上限で待った回数である。
	CapacityWaits int
	// Failures は列挙に失敗した repository の数である。
	Failures  int
	Duration  time.Duration
	FirstErr  error
	StartedAt time.Time
}

func (r cycleReport) failed() bool { return r.FirstErr != nil || r.Failures > 0 }

// Run は startup reconciliation を一度行い、以後は interval ごとに poll する。
//
// ctx の cancel で戻る。並行に呼んではならない。cycle は逐次であり、cycle が interval
// より長引いた場合は次の cycle が遅れる（重ねない）。失敗した cycle の後は jitter 付き
// backoff で待ち、成功で既定 interval へ戻る。
func (p *Poller) Run(ctx context.Context) error {
	report := p.runCycle(ctx, workflow.TriggerStartup)
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := p.poll.Interval
		if report.failed() {
			failures++
			delay = applyRetryHint(p.poll, backoffDelay(p.poll, failures, p.jitter),
				retryHint(report.FirstErr, p.clock.Now()))
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.clock.After(delay):
		}
		report = p.runCycle(ctx, workflow.TriggerScheduledPoll)
	}
}

// runCycle は 1 回の列挙と投入を行う。
//
// 列挙が失敗した repository があっても他の repository の poll は続ける。一つの
// repository の rate limit が全体の回復経路を止めないためである。
func (p *Poller) runCycle(ctx context.Context, source workflow.TriggerSource) cycleReport {
	p.cycles++
	trigger := workflow.Trigger{Source: source, ID: fmt.Sprintf("%s-%d", source, p.cycles)}
	report := cycleReport{Trigger: trigger, StartedAt: p.clock.Now()}

	var pending []contract.IssueRef
	seen := make(map[contract.IssueRef]struct{})
	for _, pollSource := range p.sources {
		refs, err := p.discover(ctx, pollSource, trigger)
		if err != nil {
			report.Failures++
			if report.FirstErr == nil {
				report.FirstErr = err
			}
			continue
		}
		for _, ref := range refs {
			if _, exists := seen[ref]; exists {
				continue
			}
			seen[ref] = struct{}{}
			pending = append(pending, ref)
		}
	}
	report.Discovered = len(pending)

	// 投入順を cycle ごとに回転させる。同時実行上限に張り付いた状態が続くと、毎 cycle
	// 同じ順では先頭が slot を取り続け、末尾が観測されないまま残る。回転させれば、
	// どの IssueRef もいずれ先頭に来る。
	deadline := report.StartedAt.Add(p.poll.Interval)
	for offset := range pending {
		ref := pending[(p.rotation(len(pending))+offset)%len(pending)]
		submitted, err := p.submit(ctx, ref, trigger, deadline, &report)
		if err != nil {
			report.Backlog = len(pending) - offset
			if report.FirstErr == nil {
				report.FirstErr = err
			}
			break
		}
		if submitted {
			report.Submitted++
		}
	}

	finished := p.clock.Now()
	report.Duration = finished.Sub(report.StartedAt)
	if !report.failed() {
		p.lastSuccess = finished
	}
	p.logCycle(ctx, report)
	return report
}

// discover は一つの repository の候補 Issue と open Run を列挙する。
func (p *Poller) discover(ctx context.Context, source PollSource,
	trigger workflow.Trigger) ([]contract.IssueRef, error) {
	candidates, err := source.Discovery.ListCandidateIssueRefs(ctx, p.poll.Filter)
	if err != nil {
		p.logSource(ctx, source, trigger, 0, 0, err)
		return nil, err
	}
	runs, err := source.Discovery.ListOpenRunIssueRefs(ctx)
	if err != nil {
		p.logSource(ctx, source, trigger, len(candidates), 0, err)
		return nil, err
	}
	p.logSource(ctx, source, trigger, len(candidates), len(runs), nil)
	return append(candidates, runs...), nil
}

// submit は 1 件の IssueRef を投入する。
//
// 同時実行上限で落ちた IssueRef を成功として捨てない。捨てると、毎 cycle 同じ順で
// 投入する poller では先頭が slot を取り続け、末尾が回収されないまま飢餓する
// （ErrDispatcherAtCapacity の doc comment）。slot が空くまで cycle 内で待ち、
// deadline を過ぎた分だけを backlog として次の cycle へ残す。
//
// 戻り値の error は cycle を中断すべき失敗だけである。identity 不足は producer 側の
// 契約違反であり、その IssueRef を飛ばして残りの投入を続ける。
func (p *Poller) submit(ctx context.Context, ref contract.IssueRef, trigger workflow.Trigger,
	deadline time.Time, report *cycleReport) (bool, error) {
	request := workflow.ReconcileRequest{Issue: ref, Trigger: trigger}
	for {
		err := p.trigger.TriggerReconcile(ctx, request)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, ErrDispatcherAtCapacity):
			if !p.clock.Now().Before(deadline) {
				// 待ち切れなかった分は backlog として記録し、cycle を終える。無期限に
				// 待つと、slot を占有したまま戻らない reconcile が polling そのものを
				// 止める。次の cycle が同じ live state から再発見する。
				return false, ErrPollCycleDeadline
			}
			report.CapacityWaits++
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-p.clock.After(p.poll.CapacityRetryInterval):
			}
		case errors.Is(err, ErrDispatcherStopped):
			return false, err
		case errors.Is(err, workflow.ErrInvalidReconcileRequest):
			p.logger.LogAttrs(ctx, slog.LevelError, "identity 不足の IssueRef を投入できなかった",
				slog.String(telemetry.FieldEvent, EventPollCycle),
				slog.String(telemetry.FieldOutcome, string(OutcomeTriggerRejected)),
				telemetry.Issue(ref),
				telemetry.Trigger(trigger),
			)
			return false, nil
		default:
			return false, err
		}
	}
}

func (p *Poller) logSource(ctx context.Context, source PollSource, trigger workflow.Trigger,
	candidates, runs int, err error) {
	outcome, level := OutcomeSourceListed, slog.LevelDebug
	if err != nil {
		outcome, level = OutcomeSourceFailed, slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String(telemetry.FieldEvent, EventPollSource),
		slog.String(telemetry.FieldOutcome, string(outcome)),
		slog.String(telemetry.FieldRepository, source.Repository),
		telemetry.Trigger(trigger),
		slog.Int(telemetry.FieldCandidates, candidates),
		slog.Int(telemetry.FieldOpenRuns, runs),
	}
	if source.RateLimit != nil {
		if remaining, observed := source.RateLimit(); observed {
			attrs = append(attrs, slog.Int(telemetry.FieldRateLimitRemaining, remaining))
		}
	}
	if err != nil {
		// error message を記録しないのは、Discovery が注入された collaborator であり、
		// message が GitHub の response 本文を含み得るためである。分類は型で残す。
		attrs = append(attrs, telemetry.ErrorType(err))
	}
	p.logger.LogAttrs(ctx, level, "poll cycle の列挙を記録した", attrs...)
}

func (p *Poller) logCycle(ctx context.Context, report cycleReport) {
	outcome := OutcomePollCompleted
	level := slog.LevelInfo
	switch {
	case report.Backlog > 0:
		outcome = OutcomePollInterrupted
		level = slog.LevelWarn
	case report.failed():
		outcome = OutcomePollFailed
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String(telemetry.FieldEvent, EventPollCycle),
		slog.String(telemetry.FieldOutcome, string(outcome)),
		telemetry.Trigger(report.Trigger),
		slog.Int64(telemetry.FieldDurationMillis, report.Duration.Milliseconds()),
		slog.Int(telemetry.FieldDiscovered, report.Discovered),
		slog.Int(telemetry.FieldSubmitted, report.Submitted),
		slog.Int(telemetry.FieldBacklog, report.Backlog),
		slog.Int(telemetry.FieldCapacityWaits, report.CapacityWaits),
		slog.Int(telemetry.FieldFailures, report.Failures),
	}
	if !p.lastSuccess.IsZero() {
		attrs = append(attrs, slog.String(telemetry.FieldLastSuccessAt,
			p.lastSuccess.UTC().Format(time.RFC3339)))
	}
	if report.FirstErr != nil {
		attrs = append(attrs, telemetry.ErrorType(report.FirstErr))
	}
	p.logger.LogAttrs(ctx, level, "poll cycle を記録した", attrs...)
}

// backoffDelay は失敗回数に応じた待機を返す。上限を超えず、jitter 後も初期値を下回らない。
//
// 上限を置くのは、rate limit が長引いた repository の polling が事実上停止すると、
// webhook 欠落の回復経路そのものが失われるためである。
func backoffDelay(config PollConfig, failures int, jitter func(time.Duration) time.Duration) time.Duration {
	delay := config.BackoffInitial
	for range max(failures-1, 0) {
		if delay >= config.BackoffMax/2 {
			delay = config.BackoffMax
			break
		}
		delay *= 2
	}
	return clampDelay(config, jitter(delay))
}

func clampDelay(config PollConfig, delay time.Duration) time.Duration {
	if delay < config.BackoffInitial {
		return config.BackoffInitial
	}
	if delay > config.BackoffMax {
		return config.BackoffMax
	}
	return delay
}

// applyRetryHint は GitHub が示した再試行時刻を backoff の下限として適用する。
//
// hint が backoff の上限を超えていても縮めない。示された時刻より早く再試行すると
// 同じ失敗を確実に繰り返し、secondary rate limit では追加のペナルティを受ける。
func applyRetryHint(config PollConfig, delay, hint time.Duration) time.Duration {
	if hint > delay {
		return hint
	}
	return clampDelay(config, delay)
}

// retryHinter は transport failure が持つ再試行 hint の境界である。
// adapter package を import せずに Retry-After と rate limit reset を読むために置く。
type retryHinter interface {
	RetryAfterHint(now time.Time) (time.Duration, bool)
}

func retryHint(err error, now time.Time) time.Duration {
	var hinter retryHinter
	if errors.As(err, &hinter) {
		if hint, ok := hinter.RetryAfterHint(now); ok {
			return hint
		}
	}
	return 0
}

// fullJitter は [初期値, delay] の範囲へ散らす。全 instance が同じ時刻に GitHub へ
// 再試行しないようにするためであり、値そのものに意味は無い。
func fullJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	return time.Duration(rand.Int64N(int64(delay)) + 1)
}
