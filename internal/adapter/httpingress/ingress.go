// Package httpingress は Kudo process の HTTP 入口である。
//
// webhook route は署名検証済みの delivery を typed な ReconcileIssue trigger へ変換するだけの
// producer であり、candidate 判定、phase 導出、GitHub mutation を持たない。health route は
// process liveness と設定・認証の readiness を分けて公開する
// （docs/spec/05_design/03_runtime-platform.md の Network）。
package httpingress

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// 公開する route。deployment の reverse proxy と GitHub App 設定が参照する。
const (
	WebhookPath = "/webhooks/github"
	HealthPath  = "/healthz"
	ReadyPath   = "/readyz"
)

// log の event 語彙。
const (
	EventWebhookDelivery = "webhook.delivery"
	EventReadiness       = "readiness.check"
)

// Outcome は ingress が返した結果の機械可読な分類である。
// delivery を受理しなかった場合は github.WebhookRejectionCode をそのまま outcome に使う。
type Outcome string

const (
	// OutcomeTriggered は reconcile を起動して応答したことを表す。
	OutcomeTriggered Outcome = "reconcile_triggered"
	// OutcomeIgnoredEvent は issues 以外の event を受理したことを表す。件数が多い状態は
	// App の webhook 購読が広すぎる signal である。
	OutcomeIgnoredEvent Outcome = "ignored_event"
	// OutcomeIgnoredAction は trigger 語彙に無い issues action を受理したことを表す。
	// GitHub が候補成立に関わる新 action を送り始めた場合、この件数だけが手掛かりになる。
	OutcomeIgnoredAction Outcome = "ignored_action"
	// OutcomeUnclassified は adapter が分類しなかった失敗である。verifier は分類済みの
	// rejection しか返さないため、現れた場合は adapter 側の契約違反を意味する。
	OutcomeUnclassified Outcome = "unclassified"
	// OutcomeTriggerRefused は reconcile を起動できなかったことを表す。delivery の
	// 内容の問題ではないため、GitHub へは一時的失敗として返す。
	OutcomeTriggerRefused Outcome = "trigger_refused"
	OutcomeReady          Outcome = "ready"
	OutcomeNotReady       Outcome = "not_ready"
)

// Reason は ingress 自身が判定した not_ready の理由 code である。
// ReadinessCheck が返す *NotReadyError の Reason とは別 namespace ではなく、同じ
// telemetry field に載る値なので、衝突しない prefix を持たせる。
type Reason string

const (
	// ReasonReadinessTimeout は検査が応答期限内に戻らなかったことを表す。
	ReasonReadinessTimeout Reason = "readiness_check_timeout"
	// ReasonReadinessPanicked は検査が panic したことを表す。
	ReasonReadinessPanicked Reason = "readiness_check_panicked"
)

// errReadinessPanicked は panic した検査を「準備できない」へ写す sentinel である。
// panic 値そのものは telemetry へ載せないため、error 値としても運ばない。
var errReadinessPanicked = errors.New("readiness 検査が panic した")

// DefaultReadinessTimeout は readiness 検査へ与える既定の猶予である。
const DefaultReadinessTimeout = 5 * time.Second

// ReconcileTrigger は検証済み delivery から ReconcileIssue を起動する境界である。
//
// 実装は reconcile の完了を待たずに戻らなければならない。webhook は低遅延経路であり、
// 応答を worker の完了に結び付けると、GitHub 側の delivery timeout が workflow の
// 成否に混ざる。戻り値の error は「起動できなかった」であって reconcile の結果ではない。
type ReconcileTrigger interface {
	TriggerReconcile(ctx context.Context, request workflow.ReconcileRequest) error
}

// ReadinessCheck は required configuration と GitHub App 認証の readiness を返す。
// error は運用者向けの内部診断であり、response body へは載せない。
//
// 理由を log へ残したい実装は *NotReadyError を返す。素の error は message を記録せず
// 型名だけを残すため、credential path や file 内容が telemetry へ漏れない。
type ReadinessCheck func(ctx context.Context) error

// NotReadyError は readiness 失敗の telemetry 安全な理由 code である。
//
// Reason には credential path、secret、file 内容、外部 response 本文を入れてはならない。
// この型が存在するのは、readiness 検査が credential file と GitHub 認証に触れる以上、
// 素の error message（例: os.ReadFile の PathError）が credential path を含むためである。
type NotReadyError struct {
	Reason string
}

func (e *NotReadyError) Error() string {
	return "readiness を満たさない: " + e.Reason
}

// Config は ingress が束縛する collaborator である。既定値で黙って継続しない。
type Config struct {
	Verifier  *github.WebhookVerifier
	Trigger   ReconcileTrigger
	Readiness ReadinessCheck
	// ReadinessTimeout は 0 のとき DefaultReadinessTimeout になる。
	ReadinessTimeout time.Duration
	// Logger は nil のとき slog.Default を使う。
	Logger *slog.Logger
}

type ingress struct {
	verifier         *github.WebhookVerifier
	trigger          ReconcileTrigger
	readiness        ReadinessCheck
	readinessTimeout time.Duration
	logger           *slog.Logger

	// mu と inFlight は readiness 検査の単一 flight を保つ。probe は繰り返し届くため、
	// request ごとに goroutine を起こすと、ctx を無視する検査が一つ居るだけで
	// outstanding な goroutine と検査内部の resource が probe 間隔ぶんだけ増え続ける。
	mu       sync.Mutex
	inFlight *readinessFlight
}

// readinessFlight は進行中の readiness 検査 1 回分である。
//
// 結果を共有するのは、readiness が request ではなく process の性質だからである。
// 待機中の probe を「別の検査」にすると、健全だが遅い検査を持つ deployment で
// probe 間隔ぶんの並行検査が走る。
type readinessFlight struct {
	done chan struct{}
	err  error
}

// NewHandler は webhook と health route を持つ handler を返す。
func NewHandler(config Config) (http.Handler, error) {
	if config.Verifier == nil {
		return nil, errors.New("webhook verifier は必須")
	}
	if config.Trigger == nil {
		return nil, errors.New("ReconcileTrigger は必須")
	}
	if config.Readiness == nil {
		return nil, errors.New("ReadinessCheck は必須")
	}
	if config.ReadinessTimeout < 0 {
		return nil, errors.New("readiness timeout が負")
	}
	timeout := config.ReadinessTimeout
	if timeout == 0 {
		timeout = DefaultReadinessTimeout
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	handler := &ingress{
		verifier:         config.Verifier,
		trigger:          config.Trigger,
		readiness:        config.Readiness,
		readinessTimeout: timeout,
		logger:           logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+WebhookPath, handler.handleWebhook)
	mux.HandleFunc("GET "+HealthPath, handler.handleHealthz)
	mux.HandleFunc("GET "+ReadyPath, handler.handleReadyz)
	return mux, nil
}

// NewServer は ingress handler を bounded な request timeout で公開する server を返す。
//
// timeout を既定の無制限のままにしないのは、webhook endpoint が公開 port であり、
// 応答しない client が connection を占有できてしまうためである。TLS termination と
// rate limit は reverse proxy 側に置く。
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func (i *ingress) handleWebhook(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	delivery, err := i.verifier.Accept(request.Header, request.Body)
	if err != nil {
		status, outcome, message := classifyRejection(err)
		extra := []slog.Attr{telemetry.ErrorType(err)}
		if message != "" {
			// message は verifier が構築した定型の診断文である。payload、signature、secret の
			// 断片を含まないことは、全 rejection code に対する
			// github.TestRejectionMessagesNeverCarryPayloadOrSignature が固定している。
			// 診断を厚くする変更でここへ payload 由来の値を混ぜてはならない。
			extra = append(extra, slog.String(telemetry.FieldError, message))
		}
		i.logDelivery(ctx, slog.LevelWarn, "webhook delivery を受理しなかった", delivery, outcome, status, extra...)
		writer.WriteHeader(status)
		return
	}

	reconcileRequest, ok := delivery.ReconcileRequest()
	if !ok {
		// 語彙外の action と issues 以外の event は、GitHub 側の delivery 失敗にせず
		// no-op として受理する。候補成立に関係する変更は次の reconcile が live state から読む。
		// 既定 log level に残すのは、この件数が「購読が広すぎる」と「GitHub が新しい action を
		// 送り始めた」を区別する唯一の材料だからである。後者は webhook が効かず polling でだけ
		// 進む遅延としてしか現れない。
		i.logDelivery(ctx, slog.LevelInfo, "reconcile trigger の語彙外 delivery を受理した",
			delivery, ignoredOutcome(delivery), http.StatusNoContent)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err := i.trigger.TriggerReconcile(ctx, reconcileRequest); err != nil {
		// error message を記録しないのは、trigger が注入された collaborator であり、
		// message の中身が telemetry の制約を満たす保証が無いためである。起動できなかった
		// 理由の分類は、その error を作った側が自分の record で残す。
		i.logDelivery(ctx, slog.LevelError, "reconcile を起動できなかった", delivery,
			OutcomeTriggerRefused, http.StatusServiceUnavailable, telemetry.ErrorType(err))
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	i.logDelivery(ctx, slog.LevelInfo, "reconcile を起動した", delivery, OutcomeTriggered, http.StatusAccepted)
	writer.WriteHeader(http.StatusAccepted)
}

// ignoredOutcome は「App の webhook 購読が広すぎる」（event 語彙外）と「GitHub が候補成立に
// 関わる新しい action を送り始めた」（action 語彙外）を分ける。両者は同じ 204 になるため、
// この outcome だけが運用上の signal になる。
func ignoredOutcome(delivery github.WebhookDelivery) Outcome {
	if delivery.Event == github.IssuesEvent {
		return OutcomeIgnoredAction
	}
	return OutcomeIgnoredEvent
}

// classifyRejection は拒否 code を HTTP status へ写像する。response body には何も載せず、
// 送信者が検証の内部段階を推測できないようにする。診断は log にだけ残す。
func classifyRejection(err error) (int, Outcome, string) {
	rejection, ok := github.RejectedWebhook(err)
	if !ok {
		return http.StatusInternalServerError, OutcomeUnclassified, ""
	}
	status := http.StatusBadRequest
	switch rejection.Code {
	case github.WebhookSignatureInvalid:
		status = http.StatusUnauthorized
	case github.WebhookPayloadTooLarge:
		status = http.StatusRequestEntityTooLarge
	case github.WebhookUnsupportedMediaType:
		status = http.StatusUnsupportedMediaType
	}
	return status, Outcome(rejection.Code), rejection.Message
}

func (i *ingress) handleHealthz(writer http.ResponseWriter, _ *http.Request) {
	writeStatus(writer, http.StatusOK)
}

func (i *ingress) handleReadyz(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), i.readinessTimeout)
	defer cancel()

	// 検査を別 goroutine で回して ctx.Done() と select するのは、context の cancel が
	// 合図であって強制ではないためである。ctx を無視する I/O を持つ検査が居ると、
	// 同期呼び出しでは handler が期限を超えて残る。goroutine を request ごとに起こさず
	// 単一 flight にするのは、その検査が戻らない間も probe が届き続けるためである。
	flight := i.readinessFlight(ctx)

	select {
	case <-flight.done:
		err := flight.err
		if err == nil {
			break
		}
		attrs := []slog.Attr{
			slog.String(telemetry.FieldEvent, EventReadiness),
			slog.String(telemetry.FieldOutcome, string(OutcomeNotReady)),
			slog.Int(telemetry.FieldHTTPStatus, http.StatusServiceUnavailable),
			telemetry.ErrorType(err),
		}
		var notReady *NotReadyError
		if errors.As(err, &notReady) {
			attrs = append(attrs, slog.String(telemetry.FieldReason, notReady.Reason))
		}
		i.logger.LogAttrs(ctx, slog.LevelWarn, "readiness 検査が通らない", attrs...)
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	case <-ctx.Done():
		i.logger.LogAttrs(ctx, slog.LevelWarn, "readiness 検査が応答期限内に戻らない",
			slog.String(telemetry.FieldEvent, EventReadiness),
			slog.String(telemetry.FieldOutcome, string(OutcomeNotReady)),
			slog.Int(telemetry.FieldHTTPStatus, http.StatusServiceUnavailable),
			slog.String(telemetry.FieldReason, string(ReasonReadinessTimeout)),
			telemetry.ErrorType(ctx.Err()),
		)
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	i.logger.DebugContext(ctx, "readiness 検査が通った",
		slog.String(telemetry.FieldEvent, EventReadiness),
		slog.String(telemetry.FieldOutcome, string(OutcomeReady)),
		slog.Int(telemetry.FieldHTTPStatus, http.StatusOK),
	)
	writeStatus(writer, http.StatusOK)
}

// readinessFlight は進行中の検査へ相乗りするか、無ければ 1 本だけ起こす。
//
// 起こした goroutine は ctx を無視する検査が戻るまで残るが、その間に届いた probe は
// 新しい goroutine を作らず同じ flight を待つ。outstanding な検査は常に高々 1 本である。
//
// 検査へ渡す context は request の cancel から切り離し、同じ猶予の deadline を自分で持つ。
// request を引き継ぐと、相乗りした probe のうち最初の一つが切断しただけで検査が cancel
// され、残りの probe が結果を得られない。deadline を落とさないのは、ctx を尊重する検査が
// 自分で諦められる契約を保つためである。
func (i *ingress) readinessFlight(ctx context.Context) *readinessFlight {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.inFlight != nil {
		return i.inFlight
	}
	flight := &readinessFlight{done: make(chan struct{})}
	i.inFlight = flight
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), i.readinessTimeout)
	go func() {
		defer cancel()
		i.checkReadiness(checkCtx, flight)
	}()
	return flight
}

// checkReadiness は検査を 1 回実行し、結果を flight へ確定させる。
//
// panic を recover するのは、goroutine へ移したことで net/http の per-connection
// recover が届かなくなるためである。readiness 実装の bug で process ごと落とすと、
// liveness endpoint も webhook も同時に失われる。
func (i *ingress) checkReadiness(ctx context.Context, flight *readinessFlight) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			i.logger.LogAttrs(ctx, slog.LevelError, "readiness 検査が panic した",
				slog.String(telemetry.FieldEvent, EventReadiness),
				slog.String(telemetry.FieldOutcome, string(OutcomeNotReady)),
				slog.String(telemetry.FieldReason, string(ReasonReadinessPanicked)),
				telemetry.ErrorType(recovered),
				slog.String(telemetry.FieldStack, string(debug.Stack())),
			)
			flight.err = errReadinessPanicked
		}
		i.mu.Lock()
		if i.inFlight == flight {
			i.inFlight = nil
		}
		i.mu.Unlock()
		close(flight.done)
	}()
	flight.err = i.readiness(ctx)
}

func writeStatus(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte("ok\n"))
}

// logDelivery は delivery ID と IssueRef で reconcile と相関できる record を出す。
// payload、signature、credential は載せない。identity が確定していない段階の拒否では
// 該当 field を省き、未検証の header 値を record へ持ち込まない。
func (i *ingress) logDelivery(
	ctx context.Context,
	level slog.Level,
	message string,
	delivery github.WebhookDelivery,
	outcome Outcome,
	status int,
	extra ...slog.Attr,
) {
	attrs := []slog.Attr{
		slog.String(telemetry.FieldEvent, EventWebhookDelivery),
		slog.String(telemetry.FieldOutcome, string(outcome)),
		slog.Int(telemetry.FieldHTTPStatus, status),
	}
	if delivery.Event != "" {
		attrs = append(attrs, slog.String(telemetry.FieldWebhookEvent, delivery.Event))
	}
	if delivery.ID != "" {
		attrs = append(attrs, telemetry.Trigger(workflow.Trigger{
			Source: workflow.TriggerWebhookDelivery,
			ID:     delivery.ID,
			Action: delivery.Action,
		}))
	}
	if delivery.Issue.Number > 0 {
		attrs = append(attrs, telemetry.Issue(delivery.Issue))
	}
	i.logger.LogAttrs(ctx, level, message, append(attrs, extra...)...)
}
