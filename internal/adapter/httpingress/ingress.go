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
	// OutcomeIgnored は検証を通ったが reconcile trigger の語彙に無い delivery を表す。
	OutcomeIgnored Outcome = "ignored"
	// OutcomeTriggerRefused は reconcile を起動できなかったことを表す。delivery の
	// 内容の問題ではないため、GitHub へは一時的失敗として返す。
	OutcomeTriggerRefused Outcome = "trigger_refused"
	OutcomeReady          Outcome = "ready"
	OutcomeNotReady       Outcome = "not_ready"
)

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
type ReadinessCheck func(ctx context.Context) error

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
		i.logDelivery(ctx, slog.LevelWarn, "webhook delivery を受理しなかった", delivery, outcome, status,
			slog.String(telemetry.FieldError, message))
		writer.WriteHeader(status)
		return
	}

	reconcileRequest, ok := delivery.ReconcileRequest()
	if !ok {
		// 語彙外の action と issues 以外の event は、GitHub 側の delivery 失敗にせず
		// no-op として受理する。候補成立に関係する変更は次の reconcile が live state から読む。
		i.logDelivery(ctx, slog.LevelDebug, "reconcile trigger の語彙外 delivery を受理した",
			delivery, OutcomeIgnored, http.StatusNoContent)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err := i.trigger.TriggerReconcile(ctx, reconcileRequest); err != nil {
		i.logDelivery(ctx, slog.LevelError, "reconcile を起動できなかった", delivery,
			OutcomeTriggerRefused, http.StatusServiceUnavailable, slog.String(telemetry.FieldError, err.Error()))
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	i.logDelivery(ctx, slog.LevelInfo, "reconcile を起動した", delivery, OutcomeTriggered, http.StatusAccepted)
	writer.WriteHeader(http.StatusAccepted)
}

// classifyRejection は拒否 code を HTTP status へ写像する。response body には何も載せず、
// 送信者が検証の内部段階を推測できないようにする。診断は log にだけ残す。
func classifyRejection(err error) (int, Outcome, string) {
	rejection, ok := github.RejectedWebhook(err)
	if !ok {
		return http.StatusInternalServerError, Outcome("unclassified"), err.Error()
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

	if err := i.readiness(ctx); err != nil {
		i.logger.WarnContext(ctx, "readiness 検査が通らない",
			slog.String(telemetry.FieldEvent, EventReadiness),
			slog.String(telemetry.FieldOutcome, string(OutcomeNotReady)),
			slog.Int(telemetry.FieldHTTPStatus, http.StatusServiceUnavailable),
			slog.String(telemetry.FieldError, err.Error()),
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
