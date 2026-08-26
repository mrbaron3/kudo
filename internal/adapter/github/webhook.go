package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// DefaultWebhookMaxPayloadBytes は署名検証のために buffer する raw body の既定上限である。
// issues event は body と`changes`を含んでも 1MiB 未満に収まる一方、GitHub 自体の上限は
// 25MiB あるため、低遅延経路が受け取る buffer を deployment 側で絞れるようにしている。
const DefaultWebhookMaxPayloadBytes = 2 * 1024 * 1024

// MaxWebhookPayloadBytes は設定できる上限の天井であり、GitHub 自身の delivery 上限に合わせる。
// 天井を置くのは、上限が「未署名 request に対する memory 保護」だからである。任意の正数を
// 許すと保護が設定次第で消え、int64 の端では上限判定用の +1 sentinel が overflow して
// 正当な delivery まで空 body として拒否される。
const MaxWebhookPayloadBytes = 25 * 1024 * 1024

const (
	webhookEventHeader     = "X-GitHub-Event"
	webhookDeliveryHeader  = "X-GitHub-Delivery"
	webhookSignatureHeader = "X-Hub-Signature-256"
	webhookSignaturePrefix = "sha256="
	webhookIssuesEvent     = "issues"
	maxWebhookDeliveryID   = 128
	maxWebhookEventName    = 64
)

// supportedIssueActions は docs/spec/05_design/04_github-routing.md が reconcile trigger として
// 受け付けると宣言した issues action である。ここに無い action は「候補成立に関係しない」の
// ではなく「trigger 語彙に無い」であり、受理して no-op にする（欠落は polling が回収する）。
//
// allowlist を維持できるのは、candidate 条件の入力（live state の open / assignee / label と
// Issue 本文）がこの 8 つで過不足なく覆われるためである。全 action を素通しにすると、
// routing に使わない milestoned / pinned / typed のたびに no-op reconcile が GitHub read を
// 消費する。**candidate 条件を変えるときは本 list も見直す**。覆えなくなった変更は、
// webhook では進まず polling でだけ進む遅延としてしか現れない。
var supportedIssueActions = []string{
	"opened",
	"reopened",
	"edited",
	"assigned",
	"unassigned",
	"labeled",
	"unlabeled",
	"closed",
}

// SupportedIssueActions は reconcile trigger として受け付ける issues action を宣言順で返す。
func SupportedIssueActions() []string { return slices.Clone(supportedIssueActions) }

// WebhookRejectionCode は delivery を受理しなかった理由の機械可読な分類である。
//
// 呼び出し側は message ではなく code で HTTP status へ写像する。GitHub への outbound I/O
// 失敗を表す TransportFailure とは別の値空間であり、両者を混ぜない。
type WebhookRejectionCode string

const (
	// WebhookBodyUnreadable は request body を最後まで読めなかったことを表す。
	WebhookBodyUnreadable WebhookRejectionCode = "body_unreadable"
	// WebhookPayloadTooLarge は raw body が設定上限を超えたことを表す。
	WebhookPayloadTooLarge WebhookRejectionCode = "payload_too_large"
	// WebhookSignatureInvalid は署名の欠落、形式不正、不一致をまとめて表す。
	// 理由を細分しないのは、送信者へ「どこまで合っていたか」を返さないためである。
	WebhookSignatureInvalid WebhookRejectionCode = "signature_invalid"
	// WebhookMissingEvent は event 名 header が無いことを表す。
	WebhookMissingEvent WebhookRejectionCode = "missing_event"
	// WebhookUnsupportedMediaType は JSON 以外の delivery format を表す。
	WebhookUnsupportedMediaType WebhookRejectionCode = "unsupported_media_type"
	// WebhookMalformedPayload は JSON として解釈できない payload を表す。
	WebhookMalformedPayload WebhookRejectionCode = "malformed_payload"
	// WebhookMissingIdentity は delivery ID、repository、Issue number のいずれかが
	// 欠落または identity として不正であることを表す。
	WebhookMissingIdentity WebhookRejectionCode = "missing_identity"
)

func (c WebhookRejectionCode) Error() string { return string(c) }

// WebhookRejection は受理しなかった delivery の分類と、運用者向けの内部診断である。
// Message は log にだけ使い、HTTP response body へ載せない（送信者へ内部詳細を返さない）。
type WebhookRejection struct {
	Code    WebhookRejectionCode
	Message string
}

func (e *WebhookRejection) Error() string {
	return fmt.Sprintf("webhook delivery を受理しない (%s): %s", e.Code, e.Message)
}

// Unwrap は Code を返し、errors.Is での分類を可能にする。
func (e *WebhookRejection) Unwrap() error { return e.Code }

// RejectedWebhook は err が delivery の拒否かを判定して内容を返す。
func RejectedWebhook(err error) (*WebhookRejection, bool) {
	var target *WebhookRejection
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func reject(code WebhookRejectionCode, message string) error {
	return &WebhookRejection{Code: code, Message: message}
}

// WebhookDelivery は検証済み delivery から取り出した identity である。
//
// Issue 本文、label、assignee といった payload snapshot を持たない。webhook payload は
// 遅延・重複・順不同で届く通知であり、実装入力にも candidate 判定にもしないためである。
type WebhookDelivery struct {
	ID    string
	Event string
	// Action は issues event の action。他の event では空である。
	Action string
	// Issue は Supported な delivery でだけ意味を持つ。
	Issue contract.IssueRef
	// Supported は reconcile trigger として受け付ける delivery かを表す。
	// false は error ではなく、no-op として受理したことを意味する。
	Supported bool
}

// ReconcileRequest は delivery を ReconcileIssue の入力へ変換する。
// 変換は identity と trigger metadata の写しに限り、business rule を持たない。
// 第 2 戻り値は trigger 語彙に無い delivery で false になる。
func (d WebhookDelivery) ReconcileRequest() (workflow.ReconcileRequest, bool) {
	if !d.Supported {
		return workflow.ReconcileRequest{}, false
	}
	return workflow.ReconcileRequest{
		Issue: d.Issue,
		Trigger: workflow.Trigger{
			Source: workflow.TriggerWebhookDelivery,
			ID:     d.ID,
			Action: d.Action,
		},
	}, true
}

// WebhookConfig は ingress adapter が固定する検証境界である。
type WebhookConfig struct {
	// Secret は GitHub App の webhook secret である。空を許さない。
	Secret string
	// MaxPayloadBytes は 0 のとき DefaultWebhookMaxPayloadBytes になる。
	MaxPayloadBytes int64
}

// WebhookVerifier は raw body の署名検証と payload 形式の解釈だけを行う。
// phase 導出、candidate 判定、GitHub への mutation は行わない。
type WebhookVerifier struct {
	secret          []byte
	maxPayloadBytes int64
}

// NewWebhookVerifier は secret を束縛した verifier を返す。
// secret が空、または上限が負の場合は起動時に失敗させる（warning で継続しない）。
func NewWebhookVerifier(config WebhookConfig) (*WebhookVerifier, error) {
	if config.Secret == "" {
		return nil, errors.New("GitHub webhook secret は必須")
	}
	if config.MaxPayloadBytes < 0 || config.MaxPayloadBytes > MaxWebhookPayloadBytes {
		return nil, fmt.Errorf("GitHub webhook payload 上限が不正: %d", config.MaxPayloadBytes)
	}
	limit := config.MaxPayloadBytes
	if limit == 0 {
		limit = DefaultWebhookMaxPayloadBytes
	}
	return &WebhookVerifier{secret: []byte(config.Secret), maxPayloadBytes: limit}, nil
}

// Accept は raw body に対する署名を検証してから payload を解釈する。
//
// 検証順は「上限付きで読む → 署名 → header と payload」である。署名より先に header や
// payload の妥当性を返すと、secret を知らない送信者が検証段階の差分を観測できる。
// body を消費する middleware を前段に置いてはならない（署名は raw body に対する HMAC である）。
//
// error が non-nil のとき、返す WebhookDelivery は署名検証を通過した範囲で確定した
// identity だけを持つ（それ以前の失敗では zero value）。呼び出し側はこれを log の相関に
// 使ってよいが、受理として扱ってはならない。
func (v *WebhookVerifier) Accept(header http.Header, body io.Reader) (WebhookDelivery, error) {
	raw, err := v.readPayload(body)
	if err != nil {
		return WebhookDelivery{}, err
	}
	if err := v.verifySignature(header.Get(webhookSignatureHeader), raw); err != nil {
		return WebhookDelivery{}, err
	}
	var delivery WebhookDelivery
	event := header.Get(webhookEventHeader)
	if !validHeaderToken(event, maxWebhookEventName) {
		return delivery, reject(WebhookMissingEvent, webhookEventHeader+" header が無いか不正")
	}
	delivery.Event = event
	if err := validateJSONMediaType(header.Get("Content-Type")); err != nil {
		return delivery, err
	}
	deliveryID := header.Get(webhookDeliveryHeader)
	if !validHeaderToken(deliveryID, maxWebhookDeliveryID) {
		return delivery, reject(WebhookMissingIdentity, webhookDeliveryHeader+" header が無いか不正")
	}
	delivery.ID = deliveryID
	if event != webhookIssuesEvent {
		return delivery, nil
	}
	return parseIssuesPayload(delivery, raw)
}

func (v *WebhookVerifier) readPayload(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, reject(WebhookBodyUnreadable, "request body が無い")
	}
	raw, err := io.ReadAll(io.LimitReader(body, v.maxPayloadBytes+1))
	if int64(len(raw)) > v.maxPayloadBytes {
		return nil, reject(WebhookPayloadTooLarge, fmt.Sprintf("payload が上限 %d bytes を超えた", v.maxPayloadBytes))
	}
	if err != nil {
		return nil, reject(WebhookBodyUnreadable, "request body を読み取れない")
	}
	return raw, nil
}

func (v *WebhookVerifier) verifySignature(header string, raw []byte) error {
	encoded, ok := strings.CutPrefix(header, webhookSignaturePrefix)
	if !ok {
		return reject(WebhookSignatureInvalid, webhookSignatureHeader+" header が無いか算法 prefix が不正")
	}
	provided, err := hex.DecodeString(encoded)
	if err != nil {
		return reject(WebhookSignatureInvalid, "signature の hex encoding が不正")
	}
	mac := hmac.New(sha256.New, v.secret)
	mac.Write(raw)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return reject(WebhookSignatureInvalid, "signature が raw body と一致しない")
	}
	return nil
}

func validateJSONMediaType(value string) error {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return reject(WebhookUnsupportedMediaType, "delivery format が application/json ではない")
	}
	return nil
}

// validHeaderToken は log field として運ぶ header 値の値域を固定する。
// 署名検証後の値であっても、長さと制御文字は record の形を壊すため受け入れない。
func validHeaderToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	})
}

type apiWebhookIssuesEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
	} `json:"issue"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func parseIssuesPayload(delivery WebhookDelivery, raw []byte) (WebhookDelivery, error) {
	var payload apiWebhookIssuesEvent
	if err := json.Unmarshal(raw, &payload); err != nil {
		return delivery, reject(WebhookMalformedPayload, "issues payload を JSON として解釈できない")
	}
	if payload.Action == "" {
		return delivery, reject(WebhookMalformedPayload, "issues payload に action が無い")
	}
	delivery.Action = payload.Action
	repository := Repository{Owner: payload.Repository.Owner.Login, Name: payload.Repository.Name}
	if err := validateRepository(repository); err != nil {
		return delivery, reject(WebhookMissingIdentity, "issues payload の repository identity が無いか不正")
	}
	if payload.Issue.Number <= 0 {
		return delivery, reject(WebhookMissingIdentity, "issues payload の Issue number が無いか不正")
	}
	// GitHub は登録時の表記のまま owner / name を送る。canonical 化せずに IssueRef へ
	// 写すと、同じ Issue を configuration 文字列から組み立てる polling 経路の IssueRef と
	// 等値比較で一致せず、二つの identity として扱われる。
	repository = repository.canonical()
	delivery.Issue = contract.IssueRef{
		Owner:      repository.Owner,
		Repository: repository.Name,
		Number:     payload.Issue.Number,
	}
	delivery.Supported = slices.Contains(supportedIssueActions, payload.Action)
	return delivery, nil
}
