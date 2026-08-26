package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const testWebhookSecret = "webhook-secret"

func signedHeader(t *testing.T, secret, event, deliveryID string, body []byte) http.Header {
	t.Helper()

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("X-GitHub-Event", event)
	header.Set("X-GitHub-Delivery", deliveryID)
	header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return header
}

func issuesPayload(action string, number int) []byte {
	return []byte(fmt.Sprintf(
		`{"action":%q,"issue":{"number":%d,"title":"t","body":"payload body must not become implementation input"},`+
			`"repository":{"name":"kudo","full_name":"mrbaron3/kudo","owner":{"login":"mrbaron3"}}}`,
		action, number))
}

func testVerifier(t *testing.T) *WebhookVerifier {
	t.Helper()

	verifier, err := NewWebhookVerifier(WebhookConfig{Secret: testWebhookSecret})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	return verifier
}

func acceptBody(t *testing.T, verifier *WebhookVerifier, event, deliveryID string, body []byte) (WebhookDelivery, error) {
	t.Helper()

	return verifier.Accept(signedHeader(t, testWebhookSecret, event, deliveryID, body), strings.NewReader(string(body)))
}

func TestNewWebhookVerifierRequiresSecret(t *testing.T) {
	t.Parallel()

	if _, err := NewWebhookVerifier(WebhookConfig{}); err == nil {
		t.Fatal("NewWebhookVerifier(empty secret) = nil error, want error")
	}
	if _, err := NewWebhookVerifier(WebhookConfig{Secret: "s", MaxPayloadBytes: -1}); err == nil {
		t.Fatal("NewWebhookVerifier(negative limit) = nil error, want error")
	}
}

func TestAcceptParsesSupportedIssuesActionsIntoReconcileRequest(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	for _, action := range SupportedIssueActions() {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			delivery, err := acceptBody(t, verifier, "issues", "delivery-"+action, issuesPayload(action, 18))
			if err != nil {
				t.Fatalf("Accept(%q) error = %v", action, err)
			}
			if !delivery.Supported {
				t.Errorf("Accept(%q).Supported = false, want true", action)
			}
			want := WebhookDelivery{
				ID:        "delivery-" + action,
				Event:     "issues",
				Action:    action,
				Issue:     contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18},
				Supported: true,
			}
			if delivery != want {
				t.Errorf("Accept(%q) = %+v, want %+v", action, delivery, want)
			}
			request, ok := delivery.ReconcileRequest()
			if !ok {
				t.Fatalf("ReconcileRequest(%q) ok = false, want true", action)
			}
			wantRequest := workflow.ReconcileRequest{
				Issue: contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18},
				Trigger: workflow.Trigger{
					Source: workflow.TriggerWebhookDelivery,
					ID:     "delivery-" + action,
					Action: action,
				},
			}
			if request != wantRequest {
				t.Errorf("ReconcileRequest(%q) = %+v, want %+v", action, request, wantRequest)
			}
			if err := request.Validate(); err != nil {
				t.Errorf("ReconcileRequest(%q).Validate() = %v, want nil", action, err)
			}
		})
	}
}

// 候補成立に関係しない action と issues 以外の event は、reconcile を起動しない受理に
// なる。error にすると GitHub 側の delivery 失敗として観測され、運用上の雑音になる。
func TestAcceptIgnoresEventsOutsideTheReconcileTriggerSet(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	cases := []struct {
		name  string
		event string
		body  []byte
	}{
		{"unsupportedAction", "issues", issuesPayload("milestoned", 18)},
		{"pullRequestEvent", "pull_request", []byte(`{"action":"opened"}`)},
		{"ping", "ping", []byte(`{"zen":"Non-blocking is better than blocking."}`)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			delivery, err := acceptBody(t, verifier, testCase.event, "delivery-1", testCase.body)
			if err != nil {
				t.Fatalf("Accept(%s) error = %v, want nil", testCase.name, err)
			}
			if delivery.Supported {
				t.Errorf("Accept(%s).Supported = true, want false", testCase.name)
			}
			if _, ok := delivery.ReconcileRequest(); ok {
				t.Errorf("ReconcileRequest(%s) ok = true, want false", testCase.name)
			}
		})
	}
}

func TestAcceptRejectsUnverifiedOrIncompleteDeliveries(t *testing.T) {
	t.Parallel()

	body := issuesPayload("opened", 18)
	cases := []struct {
		name   string
		header func(*testing.T) http.Header
		body   []byte
		want   WebhookRejectionCode
	}{
		{
			name: "missingSignature",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "issues", "d-1", body)
				h.Del("X-Hub-Signature-256")
				return h
			},
			body: body,
			want: WebhookSignatureInvalid,
		},
		{
			name:   "wrongSecret",
			header: func(t *testing.T) http.Header { return signedHeader(t, "other-secret", "issues", "d-1", body) },
			body:   body,
			want:   WebhookSignatureInvalid,
		},
		{
			name:   "signatureOverDifferentBytes",
			header: func(t *testing.T) http.Header { return signedHeader(t, testWebhookSecret, "issues", "d-1", body) },
			body:   append(append([]byte(nil), body...), ' '),
			want:   WebhookSignatureInvalid,
		},
		{
			name: "malformedSignatureEncoding",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "issues", "d-1", body)
				h.Set("X-Hub-Signature-256", "sha256=zz")
				return h
			},
			body: body,
			want: WebhookSignatureInvalid,
		},
		{
			name: "missingAlgorithmPrefix",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "issues", "d-1", body)
				h.Set("X-Hub-Signature-256", strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256="))
				return h
			},
			body: body,
			want: WebhookSignatureInvalid,
		},
		{
			name: "missingDeliveryID",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "issues", "", body)
				h.Del("X-GitHub-Delivery")
				return h
			},
			body: body,
			want: WebhookMissingIdentity,
		},
		{
			name: "missingEventName",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "", "d-1", body)
				h.Del("X-GitHub-Event")
				return h
			},
			body: body,
			want: WebhookMissingEvent,
		},
		{
			name: "unsupportedMediaType",
			header: func(t *testing.T) http.Header {
				h := signedHeader(t, testWebhookSecret, "issues", "d-1", body)
				h.Set("Content-Type", "application/x-www-form-urlencoded")
				return h
			},
			body: body,
			want: WebhookUnsupportedMediaType,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			verifier := testVerifier(t)
			_, err := verifier.Accept(testCase.header(t), strings.NewReader(string(testCase.body)))
			if err == nil {
				t.Fatalf("Accept(%s) = nil error, want %s", testCase.name, testCase.want)
			}
			if !errors.Is(err, testCase.want) {
				t.Errorf("Accept(%s) error = %v, want %s", testCase.name, err, testCase.want)
			}
		})
	}
}

func TestAcceptRejectsMalformedIssuesPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body []byte
		want WebhookRejectionCode
	}{
		{"notJSON", []byte("not json"), WebhookMalformedPayload},
		{"missingAction", []byte(`{"issue":{"number":18},"repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`), WebhookMalformedPayload},
		{"missingIssueNumber", []byte(`{"action":"opened","repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`), WebhookMissingIdentity},
		{"missingOwner", []byte(`{"action":"opened","issue":{"number":18},"repository":{"name":"kudo"}}`), WebhookMissingIdentity},
		{"missingRepositoryName", []byte(`{"action":"opened","issue":{"number":18},"repository":{"owner":{"login":"mrbaron3"}}}`), WebhookMissingIdentity},
		{"pathInjectedOwner", []byte(`{"action":"opened","issue":{"number":18},"repository":{"name":"kudo","owner":{"login":"mrbaron3/../other"}}}`), WebhookMissingIdentity},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			verifier := testVerifier(t)
			_, err := acceptBody(t, verifier, "issues", "d-1", testCase.body)
			if !errors.Is(err, testCase.want) {
				t.Errorf("Accept(%s) error = %v, want %s", testCase.name, err, testCase.want)
			}
		})
	}
}

func TestAcceptEnforcesPayloadLimitBeforeParsing(t *testing.T) {
	t.Parallel()

	verifier, err := NewWebhookVerifier(WebhookConfig{Secret: testWebhookSecret, MaxPayloadBytes: 64})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	oversized := []byte(`{"action":"opened","padding":"` + strings.Repeat("x", 64) + `"}`)
	if _, err := acceptBody(t, verifier, "issues", "d-1", oversized); !errors.Is(err, WebhookPayloadTooLarge) {
		t.Errorf("Accept(oversized) error = %v, want %s", err, WebhookPayloadTooLarge)
	}

	atLimit := []byte(`{"action":"opened","issue":{"number":18},"repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`)
	limited, err := NewWebhookVerifier(WebhookConfig{Secret: testWebhookSecret, MaxPayloadBytes: int64(len(atLimit))})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	if _, err := acceptBody(t, limited, "issues", "d-1", atLimit); err != nil {
		t.Errorf("Accept(at limit) error = %v, want nil", err)
	}
}

// 上限超過は本文を読み切らずに判定する。webhook は低遅延経路であり、上限より
// 大きい body を全部 buffer してから捨てると、上限が memory 保護として働かない。
func TestAcceptStopsReadingBeyondPayloadLimit(t *testing.T) {
	t.Parallel()

	verifier, err := NewWebhookVerifier(WebhookConfig{Secret: testWebhookSecret, MaxPayloadBytes: 16})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	counting := &countingReader{data: strings.NewReader(strings.Repeat("x", 1<<20))}
	if _, err := verifier.Accept(signedHeader(t, testWebhookSecret, "issues", "d-1", nil), counting); !errors.Is(err, WebhookPayloadTooLarge) {
		t.Fatalf("Accept(streaming oversized) error = %v, want %s", err, WebhookPayloadTooLarge)
	}
	if counting.read > 1024 {
		t.Errorf("read %d bytes past the limit, want the read to stop near the limit", counting.read)
	}
}

type countingReader struct {
	data *strings.Reader
	read int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.data.Read(p)
	r.read += n
	return n, err
}

// signature は raw body に対して検証する。JSON を再 encode してから検証すると、
// 意味的に同じで byte 列が違う payload を通してしまう。
func TestAcceptVerifiesRawBodyBytes(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	spaced := []byte("{ \"action\" : \"opened\" ,\n \"issue\" : { \"number\" : 18 } ,\n" +
		` "repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`)
	delivery, err := acceptBody(t, verifier, "issues", "d-1", spaced)
	if err != nil {
		t.Fatalf("Accept(raw formatted body) error = %v", err)
	}
	if delivery.Issue.Number != 18 {
		t.Errorf("Issue.Number = %d, want 18", delivery.Issue.Number)
	}
}

// rejection は adapter の入口の分類であり、GitHub への outbound I/O 失敗を表す
// TransportFailure とは別の値空間に保つ。
func TestWebhookRejectionIsNotATransportFailure(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	_, err := verifier.Accept(signedHeader(t, "other", "issues", "d-1", nil), strings.NewReader("{}"))
	var failure *TransportFailure
	if errors.As(err, &failure) {
		t.Fatalf("Accept() error = %v, want no TransportFailure", err)
	}
	var rejection *WebhookRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("Accept() error = %v, want *WebhookRejection", err)
	}
	if rejection.Code != WebhookSignatureInvalid {
		t.Errorf("rejection.Code = %q, want %q", rejection.Code, WebhookSignatureInvalid)
	}
}

// error message は運用者向けの内部診断であり、body や signature を含めない。
func TestWebhookRejectionMessageExcludesPayloadAndSignature(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	body := issuesPayload("opened", 18)
	header := signedHeader(t, "other-secret", "issues", "d-1", body)
	_, err := verifier.Accept(header, strings.NewReader(string(body)))
	if err == nil {
		t.Fatal("Accept() = nil error, want error")
	}
	message := err.Error()
	for _, secret := range []string{
		testWebhookSecret,
		"other-secret",
		header.Get("X-Hub-Signature-256"),
		"payload body must not become implementation input",
	} {
		if strings.Contains(message, secret) {
			t.Errorf("rejection message %q contains %q", message, secret)
		}
	}
}

var _ io.Reader = (*countingReader)(nil)

// IssueRef の同値関係は case を無視する。webhook が GitHub 登録時の表記をそのまま
// 運ぶと、polling が configuration 文字列から作る同じ Issue の IssueRef と `==` で
// 一致しなくなる。
func TestAcceptCanonicalizesRepositoryIdentity(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	body := []byte(`{"action":"opened","issue":{"number":18},` +
		`"repository":{"name":"Kudo","owner":{"login":"MrBaron3"}}}`)
	delivery, err := acceptBody(t, verifier, "issues", "d-1", body)
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	want := contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18}
	if delivery.Issue != want {
		t.Errorf("Accept().Issue = %+v, want %+v", delivery.Issue, want)
	}
}

// payload 上限は未署名 request に対しても有限の memory 保護でなければならない。
// 上限ちょうどを許すための sentinel が int64 を溢れると、保護が無効化される。
func TestNewWebhookVerifierBoundsThePayloadLimit(t *testing.T) {
	t.Parallel()

	if _, err := NewWebhookVerifier(WebhookConfig{Secret: "s", MaxPayloadBytes: MaxWebhookPayloadBytes + 1}); err == nil {
		t.Error("NewWebhookVerifier(above ceiling) = nil error, want error")
	}
	if _, err := NewWebhookVerifier(WebhookConfig{Secret: "s", MaxPayloadBytes: math.MaxInt64}); err == nil {
		t.Error("NewWebhookVerifier(MaxInt64) = nil error, want error")
	}
	if _, err := NewWebhookVerifier(WebhookConfig{Secret: "s", MaxPayloadBytes: MaxWebhookPayloadBytes}); err != nil {
		t.Errorf("NewWebhookVerifier(ceiling) error = %v, want nil", err)
	}
}

// event 名は log field として運ぶため、値域を header の申告のままにしない。
func TestAcceptRejectsMalformedEventNames(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	body := issuesPayload("opened", 18)
	for _, event := range []string{"issues\nfake", "issues ", strings.Repeat("e", 65)} {
		if _, err := acceptBody(t, verifier, event, "d-1", body); !errors.Is(err, WebhookMissingEvent) {
			t.Errorf("Accept(event=%q) error = %v, want %s", event, err, WebhookMissingEvent)
		}
	}
}

// trigger 語彙は protocol baseline（docs/spec/05_design/04_github-routing.md）の値集合である。
// 実装から期待値を取ると、allowlist から action が消えても test が緑のまま通る。
func TestSupportedIssueActionsMatchesTheRoutingVocabulary(t *testing.T) {
	t.Parallel()

	want := []string{"opened", "reopened", "edited", "assigned", "unassigned", "labeled", "unlabeled", "closed"}
	got := SupportedIssueActions()
	if len(got) != len(want) {
		t.Fatalf("SupportedIssueActions() = %v, want %v", got, want)
	}
	for index, action := range want {
		if got[index] != action {
			t.Errorf("SupportedIssueActions()[%d] = %q, want %q", index, got[index], action)
		}
	}
}

// action も log field として運ぶ。header 値へ課した値域の制約が payload 由来の値で
// 破れていると、同じ record の別 field から任意長・任意内容が入り込む。
func TestAcceptRejectsMalformedIssueActions(t *testing.T) {
	t.Parallel()

	verifier := testVerifier(t)
	for _, action := range []string{strings.Repeat("a", 65), "opened\nlabeled", "opened labeled"} {
		body := []byte(fmt.Sprintf(
			`{"action":%q,"issue":{"number":18},"repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`, action))
		delivery, err := acceptBody(t, verifier, "issues", "d-1", body)
		if !errors.Is(err, WebhookMalformedPayload) {
			t.Errorf("Accept(action=%q) error = %v, want %s", action, err, WebhookMalformedPayload)
		}
		if delivery.Action != "" {
			t.Errorf("Accept(action=%q).Action = %q, want empty", action, delivery.Action)
		}
	}
}

// rejection message は ingress が log へ載せる唯一の自由記述である。全 code について
// payload・signature・secret の断片を含まないことを固定する。
func TestRejectionMessagesNeverCarryPayloadOrSignature(t *testing.T) {
	t.Parallel()

	const prose = "payload body must not become implementation input"
	body := issuesPayload("opened", 18)
	cases := []struct {
		name   string
		secret string
		event  string
		id     string
		body   []byte
		mutate func(http.Header)
	}{
		{name: "signatureInvalid", secret: "other-secret", event: "issues", id: "d-1", body: body},
		{name: "payloadTooLarge", secret: testWebhookSecret, event: "issues", id: "d-1",
			body: []byte(`{"action":"opened","padding":"` + strings.Repeat("x", 64) + `","prose":"` + prose + `"}`)},
		{name: "missingEvent", secret: testWebhookSecret, event: "issues", id: "d-1", body: body,
			mutate: func(h http.Header) { h.Del("X-GitHub-Event") }},
		{name: "unsupportedMediaType", secret: testWebhookSecret, event: "issues", id: "d-1", body: body,
			mutate: func(h http.Header) { h.Set("Content-Type", "text/plain") }},
		{name: "missingIdentity", secret: testWebhookSecret, event: "issues", id: "d-1", body: body,
			mutate: func(h http.Header) { h.Del("X-GitHub-Delivery") }},
		{name: "malformedPayload", secret: testWebhookSecret, event: "issues", id: "d-1",
			body: []byte(`{"prose":"` + prose + `"`)},
		{name: "malformedAction", secret: testWebhookSecret, event: "issues", id: "d-1",
			body: []byte(`{"action":"` + strings.Repeat("a", 65) + `","prose":"` + prose + `"}`)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			verifier, err := NewWebhookVerifier(WebhookConfig{Secret: testWebhookSecret, MaxPayloadBytes: 128})
			if err != nil {
				t.Fatalf("NewWebhookVerifier() error = %v", err)
			}
			header := signedHeader(t, testCase.secret, testCase.event, testCase.id, testCase.body)
			signature := header.Get("X-Hub-Signature-256")
			if testCase.mutate != nil {
				testCase.mutate(header)
			}
			_, err = verifier.Accept(header, strings.NewReader(string(testCase.body)))
			if err == nil {
				t.Fatalf("Accept(%s) = nil error, want rejection", testCase.name)
			}
			message := err.Error()
			for _, forbidden := range []string{prose, signature, testWebhookSecret, "other-secret", string(testCase.body)} {
				if forbidden != "" && strings.Contains(message, forbidden) {
					t.Errorf("rejection message %q contains %q", message, forbidden)
				}
			}
		})
	}
}
