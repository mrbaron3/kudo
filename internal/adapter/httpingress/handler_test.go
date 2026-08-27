package httpingress

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/telemetry"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const testSecret = "webhook-secret"

const secretIssueBody = "confidential issue prose that must never reach telemetry"

type recordingTrigger struct {
	mu       sync.Mutex
	requests []workflow.ReconcileRequest
	err      error
}

func (t *recordingTrigger) TriggerReconcile(_ context.Context, request workflow.ReconcileRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	t.requests = append(t.requests, request)
	return nil
}

func (t *recordingTrigger) observed() []workflow.ReconcileRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]workflow.ReconcileRequest(nil), t.requests...)
}

type testIngress struct {
	handler http.Handler
	trigger *recordingTrigger
	logs    *syncBuffer
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func newVerifierForTest(t *testing.T) *github.WebhookVerifier {
	t.Helper()

	verifier, err := github.NewWebhookVerifier(github.WebhookConfig{
		Secret: testSecret, Repositories: []string{"mrbaron3/kudo"}, MaxPayloadBytes: 4096,
	})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	return verifier
}

func newTestIngress(t *testing.T, trigger *recordingTrigger, ready ReadinessCheck) testIngress {
	t.Helper()

	verifier := newVerifierForTest(t)
	logs := &syncBuffer{}
	if ready == nil {
		ready = func(context.Context) error { return nil }
	}
	handler, err := NewHandler(Config{
		Verifier:  verifier,
		Trigger:   trigger,
		Readiness: ready,
		Logger:    slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return testIngress{handler: handler, trigger: trigger, logs: logs}
}

func issuesBody(action string, number int) []byte {
	return []byte(fmt.Sprintf(
		`{"action":%q,"issue":{"number":%d,"body":%q},`+
			`"repository":{"name":"kudo","full_name":"mrbaron3/kudo","owner":{"login":"mrbaron3"}}}`,
		action, number, secretIssueBody))
}

func signedRequest(t *testing.T, secret, event, deliveryID string, body []byte) *http.Request {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}

func (i testIngress) send(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	i.handler.ServeHTTP(recorder, request)
	return recorder
}

func TestNewHandlerRequiresItsCollaborators(t *testing.T) {
	t.Parallel()

	verifier := newVerifierForTest(t)
	ready := ReadinessCheck(func(context.Context) error { return nil })
	cases := map[string]Config{
		"missingVerifier":      {Trigger: &recordingTrigger{}, Readiness: ready},
		"missingTrigger":       {Verifier: verifier, Readiness: ready},
		"missingReadiness":     {Verifier: verifier, Trigger: &recordingTrigger{}},
		"negativeReadyTimeout": {Verifier: verifier, Trigger: &recordingTrigger{}, Readiness: ready, ReadinessTimeout: -time.Second},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewHandler(config); err == nil {
				t.Errorf("NewHandler(%s) = nil error, want error", name)
			}
		})
	}
}

func TestWebhookTriggersReconcileForSupportedActions(t *testing.T) {
	t.Parallel()

	for _, action := range github.SupportedIssueActions() {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			ingress := newTestIngress(t, &recordingTrigger{}, nil)
			response := ingress.send(signedRequest(t, testSecret, "issues", "delivery-"+action, issuesBody(action, 18)))
			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
			}
			observed := ingress.trigger.observed()
			want := workflow.ReconcileRequest{
				Issue: contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18},
				Trigger: workflow.Trigger{
					Source: workflow.TriggerWebhookDelivery,
					ID:     "delivery-" + action,
					Action: action,
				},
			}
			if len(observed) != 1 || observed[0] != want {
				t.Errorf("triggered %+v, want exactly [%+v]", observed, want)
			}
		})
	}
}

// 重複配送と順不同配送は、同じ IssueRef に対する reconcile の再実行になるだけである。
// adapter は受信記録を持たず、action ごとの分岐も持たない。
func TestWebhookConvergesDuplicateAndReorderedDeliveriesOnTheSameIssue(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	deliveries := []struct{ id, action string }{
		{"delivery-1", "labeled"},
		{"delivery-1", "labeled"},
		{"delivery-2", "opened"},
		{"delivery-3", "closed"},
	}
	for _, delivery := range deliveries {
		response := ingress.send(signedRequest(t, testSecret, "issues", delivery.id, issuesBody(delivery.action, 18)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("status(%s/%s) = %d, want %d", delivery.id, delivery.action, response.Code, http.StatusAccepted)
		}
	}
	observed := ingress.trigger.observed()
	if len(observed) != len(deliveries) {
		t.Fatalf("triggered %d reconciles, want %d", len(observed), len(deliveries))
	}
	wantIssue := contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18}
	for index, request := range observed {
		if request.Issue != wantIssue {
			t.Errorf("triggered[%d].Issue = %+v, want %+v", index, request.Issue, wantIssue)
		}
	}
}

func TestWebhookAcceptsWithoutTriggeringOutsideTheTriggerVocabulary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		event string
		body  []byte
	}{
		{"unsupportedAction", "issues", issuesBody("milestoned", 18)},
		{"ping", "ping", []byte(`{"zen":"Design for failure."}`)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ingress := newTestIngress(t, &recordingTrigger{}, nil)
			response := ingress.send(signedRequest(t, testSecret, testCase.event, "delivery-1", testCase.body))
			if response.Code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
			if observed := ingress.trigger.observed(); len(observed) != 0 {
				t.Errorf("triggered %+v, want no reconcile", observed)
			}
		})
	}
}

func TestWebhookRejectsUnsafeDeliveriesWithoutTriggeringReconcile(t *testing.T) {
	t.Parallel()

	body := issuesBody("opened", 18)
	cases := []struct {
		name    string
		request func(*testing.T) *http.Request
		want    int
	}{
		{
			name: "invalidSignature",
			request: func(t *testing.T) *http.Request {
				return signedRequest(t, "other-secret", "issues", "delivery-1", body)
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "missingSignature",
			request: func(t *testing.T) *http.Request {
				request := signedRequest(t, testSecret, "issues", "delivery-1", body)
				request.Header.Del("X-Hub-Signature-256")
				return request
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "payloadTooLarge",
			request: func(t *testing.T) *http.Request {
				oversized := []byte(`{"action":"opened","padding":"` + strings.Repeat("x", 8192) + `"}`)
				return signedRequest(t, testSecret, "issues", "delivery-1", oversized)
			},
			want: http.StatusRequestEntityTooLarge,
		},
		{
			name: "malformedPayload",
			request: func(t *testing.T) *http.Request {
				return signedRequest(t, testSecret, "issues", "delivery-1", []byte("not json"))
			},
			want: http.StatusBadRequest,
		},
		{
			name: "missingIssueIdentity",
			request: func(t *testing.T) *http.Request {
				return signedRequest(t, testSecret, "issues", "delivery-1", []byte(`{"action":"opened","repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`))
			},
			want: http.StatusBadRequest,
		},
		{
			name: "missingDeliveryID",
			request: func(t *testing.T) *http.Request {
				request := signedRequest(t, testSecret, "issues", "", body)
				request.Header.Del("X-GitHub-Delivery")
				return request
			},
			want: http.StatusBadRequest,
		},
		{
			name: "missingEvent",
			request: func(t *testing.T) *http.Request {
				request := signedRequest(t, testSecret, "", "delivery-1", body)
				request.Header.Del("X-GitHub-Event")
				return request
			},
			want: http.StatusBadRequest,
		},
		{
			name: "unsupportedMediaType",
			request: func(t *testing.T) *http.Request {
				request := signedRequest(t, testSecret, "issues", "delivery-1", body)
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return request
			},
			want: http.StatusUnsupportedMediaType,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ingress := newTestIngress(t, &recordingTrigger{}, nil)
			response := ingress.send(testCase.request(t))
			if response.Code != testCase.want {
				t.Errorf("status = %d, want %d", response.Code, testCase.want)
			}
			if observed := ingress.trigger.observed(); len(observed) != 0 {
				t.Errorf("triggered %+v, want no reconcile", observed)
			}
			if body := response.Body.String(); strings.TrimSpace(body) != "" {
				t.Errorf("response body = %q, want empty", body)
			}
		})
	}
}

func TestWebhookRejectsNonPostMethods(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	request := httptest.NewRequest(http.MethodGet, WebhookPath, nil)
	if response := ingress.send(request); response.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

// trigger できないことは transport 側の一時状態であり、delivery の内容の問題ではない。
// GitHub へ 5xx を返し、欠落は polling が回収する。
func TestWebhookReportsUnavailableWhenTriggerIsRefused(t *testing.T) {
	t.Parallel()

	for _, triggerErr := range []error{errors.New("dispatcher stopped"), context.Canceled} {
		ingress := newTestIngress(t, &recordingTrigger{err: triggerErr}, nil)
		response := ingress.send(signedRequest(t, testSecret, "issues", "delivery-1", issuesBody("opened", 18)))
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
		}
	}
}

func TestWebhookLogCorrelatesDeliveryAndIssueWithoutLeakingPayload(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	request := signedRequest(t, testSecret, "issues", "delivery-1", issuesBody("opened", 18))
	signature := request.Header.Get("X-Hub-Signature-256")
	ingress.send(request)

	logs := ingress.logs.Bytes()
	record := findRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldEvent] == EventWebhookDelivery
	})
	trigger, ok := record[telemetry.FieldTrigger].(map[string]any)
	if !ok || trigger[telemetry.FieldTriggerID] != "delivery-1" {
		t.Errorf("record %v does not correlate the delivery ID", record)
	}
	issue, ok := record[telemetry.FieldIssue].(map[string]any)
	if !ok || issue[telemetry.FieldIssueRepository] != "mrbaron3/kudo" || issue[telemetry.FieldIssueNumber] != float64(18) {
		t.Errorf("record %v does not correlate the IssueRef", record)
	}
	if record[telemetry.FieldHTTPStatus] != float64(http.StatusAccepted) {
		t.Errorf("record %v does not carry the HTTP status", record)
	}
	for _, forbidden := range []string{secretIssueBody, testSecret, signature} {
		if bytes.Contains(logs, []byte(forbidden)) {
			t.Errorf("logs %s contain %q", logs, forbidden)
		}
	}
}

func TestRejectedDeliveryIsLoggedWithItsRejectionCode(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	ingress.send(signedRequest(t, "other-secret", "issues", "delivery-1", issuesBody("opened", 18)))

	record := findRecord(t, ingress.logs.Bytes(), func(record map[string]any) bool {
		return record[telemetry.FieldEvent] == EventWebhookDelivery
	})
	if record[telemetry.FieldOutcome] != string(github.WebhookSignatureInvalid) {
		t.Errorf("record %v outcome = %v, want %q", record, record[telemetry.FieldOutcome], github.WebhookSignatureInvalid)
	}
}

func findRecord(t *testing.T, logs []byte, match func(map[string]any) bool) map[string]any {
	t.Helper()

	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, line)
		}
		if match(record) {
			return record
		}
	}
	t.Fatalf("no matching log record in %s", logs)
	return nil
}

// 注入された collaborator の error message は telemetry 安全ではない。
// 起動できなかった理由は、その error を作った側（dispatcher）が自分の record で分類する。
func TestTriggerRefusalLogsTheErrorTypeWithoutItsMessage(t *testing.T) {
	t.Parallel()

	const leak = "issue body: " + secretIssueBody
	ingress := newTestIngress(t, &recordingTrigger{err: errors.New(leak)}, nil)
	ingress.send(signedRequest(t, testSecret, "issues", "delivery-1", issuesBody("opened", 18)))

	logs := ingress.logs.Bytes()
	if bytes.Contains(logs, []byte(secretIssueBody)) {
		t.Errorf("logs %s contain the collaborator error message", logs)
	}
	record := findRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeTriggerRefused)
	})
	if record[telemetry.FieldErrorType] != "*errors.errorString" {
		t.Errorf("record %v does not carry the error type", record)
	}
}

// 語彙外 delivery は既定 log level で観測できなければならない。event 由来か action 由来かを
// 区別できないと、App の購読設定が広すぎる状態と新 action の到来が同じ record になる。
func TestIgnoredDeliveriesAreDistinguishableAtDefaultLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		event string
		body  []byte
		want  Outcome
	}{
		{"unsupportedAction", "issues", issuesBody("milestoned", 18), OutcomeIgnoredAction},
		{"unsupportedEvent", "ping", []byte(`{"zen":"Design for failure."}`), OutcomeIgnoredEvent},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ingress := newTestIngress(t, &recordingTrigger{}, nil)
			ingress.send(signedRequest(t, testSecret, testCase.event, "delivery-1", testCase.body))

			record := findRecord(t, ingress.logs.Bytes(), func(record map[string]any) bool {
				return record[telemetry.FieldOutcome] == string(testCase.want)
			})
			if record["level"] == "DEBUG" {
				t.Errorf("record %v is below the default log level", record)
			}
			if record[telemetry.FieldWebhookEvent] != testCase.event {
				t.Errorf("record %v does not carry the webhook event name", record)
			}
		})
	}
}

// 語彙外の分類根拠は event 名である。Action の有無から推測すると、issues 以外の event が
// action を持つようになった時点で「新しい issues action が来ている」という誤った signal になる。
func TestIgnoredOutcomeIsDerivedFromTheEventName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		delivery github.WebhookDelivery
		want     Outcome
	}{
		{"issuesAction", github.WebhookDelivery{Event: github.IssuesEvent, Action: "milestoned"}, OutcomeIgnoredAction},
		{"otherEventWithAction", github.WebhookDelivery{Event: "pull_request", Action: "opened"}, OutcomeIgnoredEvent},
		{"otherEventWithoutAction", github.WebhookDelivery{Event: "ping"}, OutcomeIgnoredEvent},
	}
	for _, testCase := range cases {
		if got := ignoredOutcome(testCase.delivery); got != testCase.want {
			t.Errorf("ignoredOutcome(%s) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
