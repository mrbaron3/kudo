package workflow

import (
	"errors"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

func testIssueRef() contract.IssueRef {
	return contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18}
}

func TestTriggerSourcesIsClosedVocabulary(t *testing.T) {
	t.Parallel()

	got := TriggerSources()
	want := []TriggerSource{TriggerWebhookDelivery, TriggerScheduledPoll, TriggerStartup}
	if len(got) != len(want) {
		t.Fatalf("TriggerSources() = %v, want %v", got, want)
	}
	for index, source := range want {
		if got[index] != source {
			t.Errorf("TriggerSources()[%d] = %q, want %q", index, got[index], source)
		}
	}
	if TriggerSource("push").Valid() {
		t.Error(`TriggerSource("push").Valid() = true, want false`)
	}
	for _, source := range want {
		if !source.Valid() {
			t.Errorf("%q.Valid() = false, want true", source)
		}
	}
}

func TestReconcileRequestValidateAcceptsEachTriggerSource(t *testing.T) {
	t.Parallel()

	requests := []ReconcileRequest{
		{Issue: testIssueRef(), Trigger: Trigger{Source: TriggerWebhookDelivery, ID: "d-1", Action: "opened"}},
		{Issue: testIssueRef(), Trigger: Trigger{Source: TriggerScheduledPoll, ID: "poll-1"}},
		{Issue: testIssueRef(), Trigger: Trigger{Source: TriggerStartup, ID: "startup-1"}},
	}
	for _, request := range requests {
		if err := request.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", request, err)
		}
	}
}

func TestReconcileRequestValidateRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	valid := ReconcileRequest{
		Issue:   testIssueRef(),
		Trigger: Trigger{Source: TriggerWebhookDelivery, ID: "d-1", Action: "opened"},
	}
	cases := map[string]func(*ReconcileRequest){
		"owner":         func(r *ReconcileRequest) { r.Issue.Owner = "" },
		"repository":    func(r *ReconcileRequest) { r.Issue.Repository = "" },
		"number":        func(r *ReconcileRequest) { r.Issue.Number = 0 },
		"negative":      func(r *ReconcileRequest) { r.Issue.Number = -1 },
		"source":        func(r *ReconcileRequest) { r.Trigger.Source = "" },
		"unknownSource": func(r *ReconcileRequest) { r.Trigger.Source = "push" },
		"triggerID":     func(r *ReconcileRequest) { r.Trigger.ID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			request := valid
			mutate(&request)
			err := request.Validate()
			if err == nil {
				t.Fatalf("Validate(%+v) = nil, want error", request)
			}
			if !errors.Is(err, ErrInvalidReconcileRequest) {
				t.Errorf("Validate(%+v) = %v, want ErrInvalidReconcileRequest", request, err)
			}
		})
	}
}

// Action は webhook delivery の観測 metadata である。他 source が値を持つと、
// 「どの経路の trigger か」が log と ID 以外の場所でも表現できてしまう。
func TestReconcileRequestValidateRejectsActionOutsideWebhookDelivery(t *testing.T) {
	t.Parallel()

	for _, source := range []TriggerSource{TriggerScheduledPoll, TriggerStartup} {
		request := ReconcileRequest{
			Issue:   testIssueRef(),
			Trigger: Trigger{Source: source, ID: "id-1", Action: "opened"},
		}
		if err := request.Validate(); !errors.Is(err, ErrInvalidReconcileRequest) {
			t.Errorf("Validate(%q with action) = %v, want ErrInvalidReconcileRequest", source, err)
		}
	}
}

// Trigger は observability に使う。同じ Issue に対する reconcile 入力は、trigger が
// 違っても同一でなければならない（webhook を捨てても polling が同じ Run を作る）。
func TestReconcileRequestIssueIdentityIsIndependentOfTrigger(t *testing.T) {
	t.Parallel()

	webhook := ReconcileRequest{
		Issue:   testIssueRef(),
		Trigger: Trigger{Source: TriggerWebhookDelivery, ID: "d-1", Action: "labeled"},
	}
	poll := ReconcileRequest{Issue: testIssueRef(), Trigger: Trigger{Source: TriggerScheduledPoll, ID: "poll-9"}}
	if webhook.Issue != poll.Issue {
		t.Errorf("Issue identity differs: webhook=%+v poll=%+v", webhook.Issue, poll.Issue)
	}
}
