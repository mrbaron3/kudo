package telemetry

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

func logAttrs(t *testing.T, attrs ...slog.Attr) map[string]any {
	t.Helper()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.LogAttrs(t.Context(), slog.LevelInfo, "record", attrs...)

	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log record is not JSON: %v (%s)", err, buffer.String())
	}
	return record
}

func group(t *testing.T, record map[string]any, key string) map[string]any {
	t.Helper()

	value, ok := record[key].(map[string]any)
	if !ok {
		t.Fatalf("record[%q] = %v, want a group", key, record[key])
	}
	return value
}

func TestIssueAttrCorrelatesOnCanonicalRepositoryIdentity(t *testing.T) {
	t.Parallel()

	record := logAttrs(t, Issue(contract.IssueRef{Owner: "MrBaron3", Repository: "Kudo", Number: 18}))
	issue := group(t, record, FieldIssue)
	if got := issue[FieldIssueRepository]; got != "mrbaron3/kudo" {
		t.Errorf("%s.%s = %v, want %q", FieldIssue, FieldIssueRepository, got, "mrbaron3/kudo")
	}
	if got := issue[FieldIssueNumber]; got != float64(18) {
		t.Errorf("%s.%s = %v, want 18", FieldIssue, FieldIssueNumber, got)
	}
}

func TestTriggerAttrCarriesSourceAndDeliveryIdentity(t *testing.T) {
	t.Parallel()

	record := logAttrs(t, Trigger(workflow.Trigger{
		Source: workflow.TriggerWebhookDelivery,
		ID:     "delivery-1",
		Action: "labeled",
	}))
	trigger := group(t, record, FieldTrigger)
	want := map[string]any{
		FieldTriggerSource: string(workflow.TriggerWebhookDelivery),
		FieldTriggerID:     "delivery-1",
		FieldTriggerAction: "labeled",
	}
	for key, value := range want {
		if trigger[key] != value {
			t.Errorf("%s.%s = %v, want %v", FieldTrigger, key, trigger[key], value)
		}
	}
}

// action は webhook 固有の観測である。polling / startup の record に空の action が
// 並ぶと、値の無い field が「観測したが空だった」と読めてしまう。
func TestTriggerAttrOmitsActionOutsideWebhookDelivery(t *testing.T) {
	t.Parallel()

	record := logAttrs(t, Trigger(workflow.Trigger{Source: workflow.TriggerScheduledPoll, ID: "poll-1"}))
	trigger := group(t, record, FieldTrigger)
	if _, exists := trigger[FieldTriggerAction]; exists {
		t.Errorf("%s.%s = %v, want the field to be absent", FieldTrigger, FieldTriggerAction, trigger[FieldTriggerAction])
	}
}

// telemetry へ Issue 本文や credential を載せないため、helper が受け取れる値を
// identity だけに閉じてある。ここでは Issue 由来の prose が record に現れないことを固定する。
func TestAttrsCarryIdentityOnly(t *testing.T) {
	t.Parallel()

	record := logAttrs(t,
		Issue(contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18}),
		Trigger(workflow.Trigger{Source: workflow.TriggerWebhookDelivery, ID: "delivery-1", Action: "opened"}),
	)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, key := range []string{"body", "title", "signature", "secret", "token"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Errorf("record %s contains %q", encoded, key)
		}
	}
}
