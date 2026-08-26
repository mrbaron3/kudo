package httpingress

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/telemetry"
)

func TestHealthzReportsProcessLiveness(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, func(context.Context) error {
		return errors.New("GitHub App 認証が未設定")
	})
	response := ingress.send(httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if response.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (liveness must not depend on readiness)", response.Code, http.StatusOK)
	}
}

func TestReadyzReflectsConfigurationAndCredentialReadiness(t *testing.T) {
	t.Parallel()

	ready := newTestIngress(t, &recordingTrigger{}, func(context.Context) error { return nil })
	if response := ready.send(httptest.NewRequest(http.MethodGet, ReadyPath, nil)); response.Code != http.StatusOK {
		t.Errorf("ready status = %d, want %d", response.Code, http.StatusOK)
	}

	const detail = "GitHub App private key file が読めない"
	notReady := newTestIngress(t, &recordingTrigger{}, func(context.Context) error { return errors.New(detail) })
	response := notReady.send(httptest.NewRequest(http.MethodGet, ReadyPath, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("not ready status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(response.Body.String(), detail) {
		t.Errorf("response body %q leaks the readiness failure detail", response.Body.String())
	}
	record := findRecord(t, notReady.logs.Bytes(), func(record map[string]any) bool {
		return record[telemetry.FieldEvent] == EventReadiness
	})
	if record[telemetry.FieldOutcome] != string(OutcomeNotReady) {
		t.Errorf("record %v outcome = %v, want %q", record, record[telemetry.FieldOutcome], OutcomeNotReady)
	}
}

// readiness 検査は外部 I/O を含む。応答しない check が readyz を無期限に占有すると、
// orchestrator は「まだ判定中」と「準備できない」を区別できない。
func TestReadyzBoundsTheReadinessCheck(t *testing.T) {
	t.Parallel()

	observed := make(chan error, 1)
	verifier := newVerifierForTest(t)
	handler, err := NewHandler(Config{
		Verifier:         verifier,
		Trigger:          &recordingTrigger{},
		ReadinessTimeout: 10 * time.Millisecond,
		Readiness: func(ctx context.Context) error {
			<-ctx.Done()
			observed <- ctx.Err()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ReadyPath, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	select {
	case err := <-observed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("readiness context error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness check was never bounded")
	}
}

func TestHealthEndpointsRejectNonGetMethods(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	for _, path := range []string{HealthPath, ReadyPath} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		if response := ingress.send(request); response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
		}
	}
}

// health endpoint は無認証で公開される。設定値、credential、内部 error を返さない。
func TestHealthEndpointsExposeNoInternalState(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	for _, path := range []string{HealthPath, ReadyPath} {
		response := ingress.send(httptest.NewRequest(http.MethodGet, path, nil))
		if body := response.Body.String(); strings.Contains(body, testSecret) || len(body) > 16 {
			t.Errorf("GET %s body = %q, want a short body without internal state", path, body)
		}
	}
}

func TestNewServerFixesRequestTimeouts(t *testing.T) {
	t.Parallel()

	ingress := newTestIngress(t, &recordingTrigger{}, nil)
	server := NewServer(":8080", ingress.handler)
	if server.Addr != ":8080" || server.Handler == nil {
		t.Fatalf("NewServer() = %+v, want the configured address and handler", server)
	}
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Errorf("NewServer() timeouts = %+v, want every timeout to be bounded", server)
	}
}

// readiness 検査は credential file を読む。error message をそのまま記録すると
// credential path が log に残る（runtime-platform.md が明示的に禁じている）。
func TestReadinessFailureLogsAReasonCodeWithoutTheErrorMessage(t *testing.T) {
	t.Parallel()

	const leak = "/run/secrets/kudo_implementer_private_key"
	classified := newTestIngress(t, &recordingTrigger{}, func(context.Context) error {
		return &NotReadyError{Reason: "github_app_credential_unreadable"}
	})
	classified.send(httptest.NewRequest(http.MethodGet, ReadyPath, nil))
	record := findRecord(t, classified.logs.Bytes(), func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeNotReady)
	})
	if record[telemetry.FieldReason] != "github_app_credential_unreadable" {
		t.Errorf("record %v does not carry the readiness reason code", record)
	}

	unclassified := newTestIngress(t, &recordingTrigger{}, func(context.Context) error {
		return &fs.PathError{Op: "open", Path: leak, Err: fs.ErrPermission}
	})
	unclassified.send(httptest.NewRequest(http.MethodGet, ReadyPath, nil))
	logs := unclassified.logs.Bytes()
	if bytes.Contains(logs, []byte(leak)) {
		t.Fatalf("logs %s contain the credential path", logs)
	}
	record = findRecord(t, logs, func(record map[string]any) bool {
		return record[telemetry.FieldOutcome] == string(OutcomeNotReady)
	})
	if record[telemetry.FieldErrorType] != "*fs.PathError" {
		t.Errorf("record %v does not carry the error type", record)
	}
}
