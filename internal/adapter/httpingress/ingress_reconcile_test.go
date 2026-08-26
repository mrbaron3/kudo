package httpingress_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/adapter/httpingress"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/controller"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const secret = "webhook-secret"

// fakeLiveState は ReconcileIssue の外形だけを模した deterministic な fake である。
// 観測のたびに live state を読み直し、Run の生成は branch ref create の CAS 相当で
// 1 度しか成立させない。Kudo の phase 導出そのものは #70 の core が所有する。
type fakeLiveState struct {
	mu           sync.Mutex
	candidate    bool
	runs         int
	observations []workflow.ReconcileRequest
	block        chan struct{}
	completed    chan workflow.ReconcileRequest
}

func newFakeLiveState(candidate bool, capacity int) *fakeLiveState {
	return &fakeLiveState{
		candidate: candidate,
		completed: make(chan workflow.ReconcileRequest, capacity),
	}
}

func (s *fakeLiveState) reconcile(_ context.Context, request workflow.ReconcileRequest) error {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.observations = append(s.observations, request)
	if s.candidate && s.runs == 0 {
		s.runs++
	}
	s.mu.Unlock()
	s.completed <- request
	return nil
}

func (s *fakeLiveState) snapshot() (int, []workflow.ReconcileRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs, append([]workflow.ReconcileRequest(nil), s.observations...)
}

func newIngressServer(t *testing.T, state *fakeLiveState, maxInFlight int) *httptest.Server {
	t.Helper()

	verifier, err := github.NewWebhookVerifier(github.WebhookConfig{Secret: secret})
	if err != nil {
		t.Fatalf("NewWebhookVerifier() error = %v", err)
	}
	dispatcher, err := controller.NewTriggerDispatcher(
		state.reconcile,
		controller.TriggerDispatcherConfig{MaxInFlight: maxInFlight},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("NewTriggerDispatcher() error = %v", err)
	}
	handler, err := httpingress.NewHandler(httpingress.Config{
		Verifier:  verifier,
		Trigger:   dispatcher,
		Readiness: func(context.Context) error { return nil },
		Logger:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	})
	return server
}

func deliver(t *testing.T, server *httptest.Server, deliveryID, action string, number int) *http.Response {
	t.Helper()

	body := []byte(fmt.Sprintf(
		`{"action":%q,"issue":{"number":%d},"repository":{"name":"kudo","owner":{"login":"mrbaron3"}}}`,
		action, number))
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+httpingress.WebhookPath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "issues")
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func waitForReconciles(t *testing.T, state *fakeLiveState, count int) {
	t.Helper()

	for range count {
		select {
		case <-state.completed:
		case <-time.After(10 * time.Second):
			runs, observations := state.snapshot()
			t.Fatalf("reconcile が %d 件しか完了していない (runs=%d)", len(observations), runs)
		}
	}
}

// AC-1: 応答は worker の完了を待たない。
func TestWebhookRespondsBeforeReconcileCompletes(t *testing.T) {
	t.Parallel()

	state := newFakeLiveState(true, 1)
	state.block = make(chan struct{})
	server := newIngressServer(t, state, 1)

	response := deliver(t, server, "delivery-1", "opened", 18)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if runs, observations := state.snapshot(); runs != 0 || len(observations) != 0 {
		t.Fatalf("応答時点で reconcile が進行していた: runs=%d observations=%d", runs, len(observations))
	}

	close(state.block)
	waitForReconciles(t, state, 1)
	if runs, _ := state.snapshot(); runs != 1 {
		t.Errorf("runs = %d, want 1", runs)
	}
}

// AC-3: 重複配送と順不同配送は観測の再実行になるだけで、二重 Run を作らない。
func TestDuplicateAndReorderedDeliveriesReobserveWithoutASecondRun(t *testing.T) {
	t.Parallel()

	deliveries := []struct{ id, action string }{
		{"delivery-3", "labeled"},
		{"delivery-1", "opened"},
		{"delivery-1", "opened"},
		{"delivery-2", "assigned"},
		{"delivery-4", "edited"},
	}
	state := newFakeLiveState(true, len(deliveries))
	server := newIngressServer(t, state, 1)

	for _, delivery := range deliveries {
		response := deliver(t, server, delivery.id, delivery.action, 18)
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("status(%s) = %d, want %d", delivery.id, response.StatusCode, http.StatusAccepted)
		}
		waitForReconciles(t, state, 1)
	}

	runs, observations := state.snapshot()
	if runs != 1 {
		t.Errorf("runs = %d, want 1 (branch ref create の CAS が排他する)", runs)
	}
	if len(observations) != len(deliveries) {
		t.Fatalf("observations = %d, want %d", len(observations), len(deliveries))
	}
	wantIssue := contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 18}
	for index, observation := range observations {
		if observation.Issue != wantIssue {
			t.Errorf("observations[%d].Issue = %+v, want %+v", index, observation.Issue, wantIssue)
		}
		if observation.Trigger.Source != workflow.TriggerWebhookDelivery {
			t.Errorf("observations[%d].Trigger.Source = %q, want %q",
				index, observation.Trigger.Source, workflow.TriggerWebhookDelivery)
		}
	}
}

// AC-4: 候補成立に関係しない supported action も同じ reconciliation を実行し、
// live state に基づいて no-op になる。failure でも escalation でもない。
func TestSupportedActionsOutsideCandidacyAreNoOps(t *testing.T) {
	t.Parallel()

	actions := github.SupportedIssueActions()
	state := newFakeLiveState(false, len(actions))
	server := newIngressServer(t, state, len(actions))

	for index, action := range actions {
		response := deliver(t, server, fmt.Sprintf("delivery-%d", index), action, 18)
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("status(%s) = %d, want %d", action, response.StatusCode, http.StatusAccepted)
		}
	}
	waitForReconciles(t, state, len(actions))

	runs, observations := state.snapshot()
	if runs != 0 {
		t.Errorf("runs = %d, want 0", runs)
	}
	if len(observations) != len(actions) {
		t.Errorf("observations = %d, want %d", len(observations), len(actions))
	}
}
