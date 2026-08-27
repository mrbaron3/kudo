package github

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTransportFailureClassification(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		status  int
		headers map[string]string
		body    string
		class   FailureClass
		retry   bool
	}{
		"primary rate limit": {
			status: http.StatusForbidden,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     "1787356800",
			},
			body:  `{"message":"API rate limit exceeded"}`,
			class: FailureRateLimit,
			retry: true,
		},
		"secondary rate limit": {
			status:  http.StatusForbidden,
			headers: map[string]string{"Retry-After": "60"},
			body:    `{"message":"Please wait before retrying."}`,
			class:   FailureSecondaryRateLimit,
			retry:   true,
		},
		"permission": {
			status: http.StatusForbidden,
			body:   `{"message":"Resource not accessible by integration"}`,
			class:  FailurePermission,
		},
		"permission hidden as not found": {
			status: http.StatusNotFound,
			body:   `{"message":"Resource not accessible by integration"}`,
			class:  FailurePermission,
		},
		"credential": {
			status: http.StatusUnauthorized,
			body:   `{"message":"Bad credentials"}`,
			class:  FailureCredential,
		},
		"service": {
			status: http.StatusBadGateway,
			body:   `{"message":"upstream failed"}`,
			class:  FailureUnavailable,
			retry:  true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range test.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			_, err := testGateway(server.Client(), server.URL).ReadContent(t.Context(), "README.md", "main")
			var failure *TransportFailure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want *TransportFailure", err)
			}
			if failure.Class != test.class || failure.Retryable() != test.retry {
				t.Fatalf("failure = %#v, want class %s retryable %v", failure, test.class, test.retry)
			}
			if test.class == FailureSecondaryRateLimit && failure.RetryAfter != time.Minute {
				t.Fatalf("RetryAfter = %s, want 1m", failure.RetryAfter)
			}
			if test.class == FailureRateLimit && failure.RateLimitReset.Unix() != 1787356800 {
				t.Fatalf("RateLimitReset = %s", failure.RateLimitReset)
			}
		})
	}
}

func TestTokenSourceFailureClassification(t *testing.T) {
	t.Parallel()

	rateLimit := &TransportFailure{
		Class:     FailureRateLimit,
		Operation: "POST installation access token",
		Message:   "rate limited",
	}
	tests := map[string]struct {
		err      error
		class    FailureClass
		retry    bool
		preserve *TransportFailure
	}{
		"structured failure": {
			err:      rateLimit,
			class:    FailureRateLimit,
			retry:    true,
			preserve: rateLimit,
		},
		"network": {
			err:   testNetworkError{},
			class: FailureNetwork,
			retry: true,
		},
		"credential": {
			err:   errors.New("private key が不正"),
			class: FailureCredential,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gateway, err := NewGateway(http.DefaultClient, tokenSourceFunc(func(context.Context) (string, error) {
				return "", test.err
			}), Config{
				Repository: Repository{Owner: "acme", Name: "widgets"},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = gateway.ReadContent(t.Context(), "README.md", "main")
			var failure *TransportFailure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want *TransportFailure", err)
			}
			if failure.Class != test.class || failure.Retryable() != test.retry {
				t.Fatalf("failure = %#v, want class %s retryable %v", failure, test.class, test.retry)
			}
			if test.preserve != nil && failure != test.preserve {
				t.Fatalf("failure = %p, want original %p", failure, test.preserve)
			}
		})
	}
}

func TestTransportFailureClassifiesTimeout(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	gateway := testGateway(client, "https://api.github.invalid")
	_, err := gateway.ReadContent(t.Context(), "README.md", "main")
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureTimeout || !failure.Retryable() {
		t.Fatalf("error = %#v, want retryable timeout TransportFailure", err)
	}
}

func TestPaginationRejectsCrossOriginAndCycles(t *testing.T) {
	t.Parallel()

	for name, link := range map[string]string{
		"cross origin": `<https://attacker.invalid/repos/acme/widgets/issues?page=2>; rel="next"`,
		"cycle":        "self",
	} {
		t.Run(name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := link
				if value == "self" {
					value = `<` + server.URL + r.URL.RequestURI() + `>; rel="next"`
				}
				w.Header().Set("Link", value)
				_, _ = w.Write([]byte(`[]`))
			}))
			t.Cleanup(server.Close)

			_, err := testGateway(server.Client(), server.URL).ListCandidateIssues(t.Context(), CandidateFilter{
				Assignee: "worker",
				Label:    "ai-ready",
			})
			var failure *TransportFailure
			if !errors.As(err, &failure) || failure.Class != FailureInvalidResponse {
				t.Fatalf("error = %v, want invalid response", err)
			}
			if !strings.Contains(failure.Error(), nameWord(name)) {
				t.Fatalf("error = %q, want diagnostic for %q", failure.Error(), name)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type testNetworkError struct{}

func (testNetworkError) Error() string   { return "connection reset" }
func (testNetworkError) Timeout() bool   { return false }
func (testNetworkError) Temporary() bool { return true }

func nameWord(name string) string {
	if name == "cross origin" {
		return "origin"
	}
	return "cycle"
}

// Retry-After は秒で届く。表現できない大きさをそのまま Duration へ掛けると wrap して
// 負値になり、RetryAfterHint が「指示なし」へ落ちる。指示が消えると呼び出し側は通常の
// backoff で再試行し、GitHub が示した下限を黙って破る。
func TestRetryAfterSecondsSaturateInsteadOfWrapping(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		seconds int64
		want    time.Duration
	}{
		"表現できる最大":    {maxDurationSeconds, time.Duration(maxDurationSeconds) * time.Second},
		"最大の 1 つ上":   {maxDurationSeconds + 1, time.Duration(math.MaxInt64)},
		"int64 に近い値": {math.MaxInt64 - 1, time.Duration(math.MaxInt64)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := secondsToDuration(testCase.seconds)
			if got != testCase.want {
				t.Fatalf("secondsToDuration(%d) = %v, want %v", testCase.seconds, got, testCase.want)
			}
			failure := &TransportFailure{Class: FailureRateLimit, RetryAfter: got}
			if hint, ok := failure.RetryAfterHint(time.Unix(0, 0)); !ok || hint <= 0 {
				t.Fatalf("RetryAfterHint() = %v, %v, want a positive hint", hint, ok)
			}
		})
	}
}
