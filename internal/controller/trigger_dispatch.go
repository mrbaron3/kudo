package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// ReconcileIssue は一つの IssueRef を live state の観測から reconcile する run-once operation である。
//
// webhook adapter と poller はこの関数だけを共有する。何度呼ばれても同じ live state からは
// 同じ安全な結果になり、重複した呼び出しは観測の再実行になる（mutation の重複は marker と
// branch ref create の CAS が防ぐ）。
type ReconcileIssue func(ctx context.Context, request workflow.ReconcileRequest) error

// log の event / outcome 語彙。message 文字列ではなくこの code で分岐・集計する。
const (
	EventReconcileTrigger = "reconcile.trigger"
	EventReconcileRun     = "reconcile.run"
)

// Outcome は trigger と reconcile 実行の機械可読な結果分類である。
type Outcome string

const (
	OutcomeTriggerAccepted Outcome = "trigger_accepted"
	// OutcomeTriggerDropped は同時実行上限による drop である。
	OutcomeTriggerDropped Outcome = "trigger_dropped"
	// OutcomeTriggerStopped は shutdown 後に届いた trigger である。
	// drop と分けるのは、503 が増えたときに「容量を上げるべき」と「停止中」を
	// record から区別するためである。
	OutcomeTriggerStopped Outcome = "trigger_stopped"
	// OutcomeTriggerRejected は identity 不足の trigger である。adapter が identity を
	// 検証済みで渡す限り現れず、現れた場合は producer 側の契約違反を意味する。
	OutcomeTriggerRejected Outcome = "trigger_rejected"
	// OutcomeTriggerCoalesced は実行中の Issue への trigger を再実行へ畳んだことを表す。
	// 件数が多い状態は、同じ Issue に対する trigger が実行より速く届いていることを示す。
	OutcomeTriggerCoalesced  Outcome = "trigger_coalesced"
	OutcomeReconcileFailed   Outcome = "reconcile_failed"
	OutcomeReconcilePanicked Outcome = "reconcile_panicked"
)

var (
	// ErrDispatcherStopped は shutdown 後の trigger を表す。
	ErrDispatcherStopped = errors.New("reconcile dispatcher は停止している")

	// ErrDispatcherAtCapacity は同時実行上限による trigger の drop を表す。
	// 落ちた delivery は polling が回収するため、escalation ではない。
	//
	// これは github-routing.md の`waiting_capacity`とは別概念である。`waiting_capacity`は
	// ReconcileIssue が観測を実行したうえで返す結果（`ai-ready`を残して再評価）であり、
	// こちらは観測へ到達させない受付側の背圧である。label にも Run にも影響しない。
	//
	// 「polling が回収する」という安全性が成立するのは webhook 経路に限る。polling 自身が
	// 落とされた場合、候補を毎 cycle 同じ順で投入する poller では先頭 N 件が slot を取り
	// 続け、末尾が回収されないまま飢餓する。poller はこの error を成功として捨てず、
	// slot を待つか同じ cycle 内で再投入しなければならない。
	ErrDispatcherAtCapacity = errors.New("reconcile dispatcher が同時実行上限に達している")
)

// TriggerDispatcherConfig は同時実行 capacity の調停値である。
type TriggerDispatcherConfig struct {
	// MaxInFlight は同時に走らせる ReconcileIssue の上限である。正数を要求する。
	MaxInFlight int
}

// TriggerDispatcher は ReconcileIssue を呼び出し元の lifetime から切り離して実行する。
//
// webhook は低遅延経路であり、応答は reconcile の完了を待てない。一方で無制限に
// goroutine を作ると、delivery 量がそのまま GitHub API と provider の負荷になる。
// 同時実行の上限は Controller の責務なので、その調停をここへ集約する。
//
// 同じ IssueRef の reconcile は同時に走らせない。ReconcileIssue は observe → derive →
// record の列であり、記録は「現在値を確認してから書く」冪等 mutation である。同じ Issue
// を並行に通すと、両方が「記録が無い」を観測してから両方が書く窓が開き、marker で
// 一意にしている記録が重複し得る。重複した trigger は観測の再実行になるだけでよいので、
// 実行中の Issue へ届いた trigger は破棄せず、完了後の 1 回の再実行へ畳む
// （docs/spec/05_design/04_github-routing.md の Unified reconciliation）。
type TriggerDispatcher struct {
	reconcile ReconcileIssue
	logger    *slog.Logger
	slots     chan struct{}

	// ctx は reconcile へ渡す lifetime であり、Shutdown まで生きる。呼び出し元の
	// request context を引き継がないのは、応答した時点で work が cancel されるためである。
	ctx    context.Context
	cancel context.CancelFunc

	// mu は stopped の観測と wg.Add、および in-flight の登録を同じ critical section に
	// 収め、Shutdown の wg.Wait と新規 Add が交差しないようにする。
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
	// inFlight は実行中の IssueRef と、その完了後に再実行する trigger を保持する。
	// 再実行は最後に届いた trigger 1 件に畳む。観測はやり直せば同じ結果になるため、
	// 中間の trigger を個別に実行する意味が無いからである。
	inFlight map[contract.IssueRef]*workflow.ReconcileRequest
}

// NewTriggerDispatcher は reconcile を束縛した dispatcher を返す。
// logger が nil のときは slog.Default を使う。
func NewTriggerDispatcher(reconcile ReconcileIssue, config TriggerDispatcherConfig, logger *slog.Logger) (*TriggerDispatcher, error) {
	if reconcile == nil {
		return nil, errors.New("ReconcileIssue は必須")
	}
	if config.MaxInFlight <= 0 {
		return nil, fmt.Errorf("同時実行上限が不正: %d", config.MaxInFlight)
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &TriggerDispatcher{
		reconcile: reconcile,
		logger:    logger,
		slots:     make(chan struct{}, config.MaxInFlight),
		ctx:       ctx,
		cancel:    cancel,
		inFlight:  make(map[contract.IssueRef]*workflow.ReconcileRequest),
	}, nil
}

// TriggerReconcile は reconcile の起動可否だけを返し、完了を待たない。
//
// 戻り値の nil は「起動した」であって「reconcile が成功した」ではない。実行結果は
// log と GitHub 上の記録に現れる。identity 不足は ErrInvalidReconcileRequest、
// 上限超過は ErrDispatcherAtCapacity、shutdown 後は ErrDispatcherStopped を返す。
//
// ctx は log の相関にだけ使い、起動可否の判断にも reconcile の lifetime にも使わない。
// 呼び出し元（webhook request）が cancel されたことは「その Issue を reconcile しなくてよい」
// を意味しないためである。停止は Shutdown が一点で決める。
func (d *TriggerDispatcher) TriggerReconcile(ctx context.Context, request workflow.ReconcileRequest) error {
	if err := request.Validate(); err != nil {
		d.logRefusal(ctx, OutcomeTriggerRejected, "identity 不足の reconcile trigger を拒否した", request)
		return err
	}

	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		d.logRefusal(ctx, OutcomeTriggerStopped, "shutdown 後の reconcile trigger を拒否した", request)
		return ErrDispatcherStopped
	}
	key := issueKey(request.Issue)
	if _, running := d.inFlight[key]; running {
		// 実行中の Issue への trigger は、完了後の 1 回の再実行へ畳む。slot は取らない。
		// 成功として返すのは、この trigger の目的（live state をもう一度観測する）が
		// 果たされるためである。落としたことにすると poller が同じ cycle 内で
		// 待ち続け、自分が起動した reconcile の完了を待つことになる。
		pending := request
		d.inFlight[key] = &pending
		d.mu.Unlock()
		d.logger.DebugContext(ctx, "実行中の Issue への reconcile trigger を再実行へ畳んだ",
			slog.String(telemetry.FieldEvent, EventReconcileTrigger),
			slog.String(telemetry.FieldOutcome, string(OutcomeTriggerCoalesced)),
			telemetry.Issue(request.Issue),
			telemetry.Trigger(request.Trigger),
		)
		return nil
	}
	select {
	case d.slots <- struct{}{}:
	default:
		d.mu.Unlock()
		d.logRefusal(ctx, OutcomeTriggerDropped, "reconcile trigger を同時実行上限で落とした", request)
		return ErrDispatcherAtCapacity
	}
	d.inFlight[key] = nil
	d.wg.Add(1)
	d.mu.Unlock()

	d.logger.DebugContext(ctx, "reconcile trigger を受け付けた",
		slog.String(telemetry.FieldEvent, EventReconcileTrigger),
		slog.String(telemetry.FieldOutcome, string(OutcomeTriggerAccepted)),
		telemetry.Issue(request.Issue),
		telemetry.Trigger(request.Trigger),
	)
	go d.run(request)
	return nil
}

// logRefusal は起動しなかった trigger を outcome 別に記録する。ingress は message を
// 載せない方針で 503 へ潰すため、理由の分類はこの record だけが持つ。
func (d *TriggerDispatcher) logRefusal(ctx context.Context, outcome Outcome, message string, request workflow.ReconcileRequest) {
	d.logger.LogAttrs(ctx, slog.LevelWarn, message,
		slog.String(telemetry.FieldEvent, EventReconcileTrigger),
		slog.String(telemetry.FieldOutcome, string(outcome)),
		telemetry.Issue(request.Issue),
		telemetry.Trigger(request.Trigger),
	)
}

// run は 1 件の reconcile を実行し、実行中に畳まれた trigger があればそのまま続けて
// 再実行する。slot は畳まれた分を通しても 1 つのままなので、同時実行上限は保たれる。
//
// shutdown 中でも畳まれた分は実行する。畳んだ時点で呼び出し元へ「受け付けた」と
// 返しているためであり、新規 trigger は既に拒否されているので loop は必ず終わる。
func (d *TriggerDispatcher) run(request workflow.ReconcileRequest) {
	defer d.wg.Done()
	defer func() { <-d.slots }()
	key := issueKey(request.Issue)
	for {
		d.runOnce(request)
		d.mu.Lock()
		pending := d.inFlight[key]
		if pending == nil {
			delete(d.inFlight, key)
			d.mu.Unlock()
			return
		}
		d.inFlight[key] = nil
		d.mu.Unlock()
		request = *pending
	}
}

func (d *TriggerDispatcher) runOnce(request workflow.ReconcileRequest) {
	defer func() {
		// 一つの Issue の導出 bug で低遅延経路 process 全体を落とさない。
		// 落とした delivery と同じく、この Run は polling の再観測が回収する。
		if recovered := recover(); recovered != nil {
			// panic 値そのものは記録しない。任意の値であり、Issue 本文や credential を
			// 含み得る。process を落とさない選択は stack trace を運用者から奪う選択でも
			// あるため、診断は型と stack で残す。
			d.logger.LogAttrs(d.ctx, slog.LevelError, "reconcile が panic した",
				slog.String(telemetry.FieldEvent, EventReconcileRun),
				slog.String(telemetry.FieldOutcome, string(OutcomeReconcilePanicked)),
				telemetry.Issue(request.Issue),
				telemetry.Trigger(request.Trigger),
				telemetry.ErrorType(recovered),
				slog.String(telemetry.FieldStack, string(debug.Stack())),
			)
		}
	}()

	if err := d.reconcile(d.ctx, request); err != nil {
		// error message を記録しないのは、ReconcileIssue が注入された collaborator であり、
		// GitHub の response 本文や Issue の非公開本文を含み得るためである。失敗の分類は
		// reconcile 側が機械可読な code として自分の record に残す。
		d.logger.LogAttrs(d.ctx, slog.LevelError, "reconcile が失敗した",
			slog.String(telemetry.FieldEvent, EventReconcileRun),
			slog.String(telemetry.FieldOutcome, string(OutcomeReconcileFailed)),
			telemetry.Issue(request.Issue),
			telemetry.Trigger(request.Trigger),
			telemetry.ErrorType(err),
		)
	}
}

// issueKey は in-flight 表の key を GitHub の case-insensitive な identity へ揃える。
// 表記だけが違う IssueRef が別 key になると、同じ Issue が並行に走る。
func issueKey(ref contract.IssueRef) contract.IssueRef {
	return contract.IssueRef{
		Owner:      strings.ToLower(ref.Owner),
		Repository: strings.ToLower(ref.Repository),
		Number:     ref.Number,
	}
}

// Shutdown は新規 trigger の受付を止め、進行中の reconcile を待つ。
//
// ctx の猶予内に終わらない reconcile へは cancel を伝えて戻る。中断された Operation は
// 再起動後の再観測で新しい attempt として再実行されるため、ここで待ち切る必要はない。
func (d *TriggerDispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		d.cancel()
		return nil
	case <-ctx.Done():
		d.cancel()
		return ctx.Err()
	}
}
