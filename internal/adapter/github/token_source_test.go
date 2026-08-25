package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppTokenSourceConfigFromEnvironment(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"KUDO_GITHUB_REVIEWER_APP_ID_FILE":          "/run/secrets/reviewer-app-id",
		"KUDO_GITHUB_REVIEWER_PRIVATE_KEY_FILE":     "/run/secrets/reviewer-private-key",
		"KUDO_GITHUB_REVIEWER_INSTALLATION_ID_FILE": "/run/secrets/reviewer-installation-id",
	}
	config, err := AppTokenSourceConfigFromEnvironment(ActorReviewer, func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Actor != ActorReviewer ||
		config.AppIDFile != values["KUDO_GITHUB_REVIEWER_APP_ID_FILE"] ||
		config.PrivateKeyFile != values["KUDO_GITHUB_REVIEWER_PRIVATE_KEY_FILE"] ||
		config.InstallationIDFile != values["KUDO_GITHUB_REVIEWER_INSTALLATION_ID_FILE"] {
		t.Fatalf("config = %#v", config)
	}
}

func TestAppTokenSourceConfigFromEnvironmentRejectsMissingOrAmbiguousInput(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		actor  ActorRole
		values map[string]string
	}{
		"unknown actor": {
			actor: "administrator",
		},
		"missing file setting": {
			actor: ActorImplementer,
			values: map[string]string{
				"KUDO_GITHUB_IMPLEMENTER_APP_ID_FILE":          "/app-id",
				"KUDO_GITHUB_IMPLEMENTER_PRIVATE_KEY_FILE":     "/private-key",
				"KUDO_GITHUB_IMPLEMENTER_INSTALLATION_ID_FILE": " ",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := AppTokenSourceConfigFromEnvironment(test.actor, func(key string) (string, bool) {
				value, ok := test.values[key]
				return value, ok
			})
			if err == nil {
				t.Fatal("error = nil, want strict configuration rejection")
			}
		})
	}
}

func TestActorPermissions(t *testing.T) {
	t.Parallel()

	tests := map[ActorRole]map[string]string{
		ActorCoordinator: {
			"metadata":      "read",
			"issues":        "write",
			"pull_requests": "read",
			"checks":        "read",
		},
		ActorImplementer: {
			"metadata":      "read",
			"issues":        "read",
			"contents":      "write",
			"pull_requests": "write",
			"checks":        "write",
		},
		ActorReviewer: {
			"metadata":      "read",
			"issues":        "write",
			"contents":      "read",
			"pull_requests": "read",
			"checks":        "write",
		},
	}

	for actor, want := range tests {
		actor, want := actor, want
		t.Run(string(actor), func(t *testing.T) {
			t.Parallel()

			got, err := ActorPermissions(actor)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("permissions = %#v, want %#v", got, want)
			}
			got["administration"] = "write"
			again, err := ActorPermissions(actor)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(got, again) {
				t.Fatal("caller mutation changed the canonical permission subset")
			}
		})
	}

	if _, err := ActorPermissions("administrator"); err == nil {
		t.Fatal("unknown actor permissions were accepted")
	}
}

func TestProviderCredentialEnvironmentAllowlistExposesOnlyOperationToken(t *testing.T) {
	t.Parallel()

	for _, actor := range []ActorRole{ActorCoordinator, ActorImplementer, ActorReviewer} {
		got, err := ProviderCredentialEnvironmentAllowlist(actor)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{"GH_TOKEN"}) {
			t.Fatalf("%s allowlist = %q, want only GH_TOKEN", actor, got)
		}
	}
}

func TestActorAppTokenSourcesRequireDistinctImplementerAndReviewerApps(t *testing.T) {
	t.Parallel()

	implementerKey := testPrivateKey(t)
	reviewerKey := testPrivateKey(t)
	privateKeys := map[ActorRole]*rsa.PrivateKey{
		ActorCoordinator: implementerKey,
		ActorImplementer: implementerKey,
		ActorReviewer:    reviewerKey,
	}
	configs := map[ActorRole]AppTokenSourceConfig{
		ActorCoordinator: testActorSourceConfig(ActorCoordinator),
		ActorImplementer: testActorSourceConfig(ActorImplementer),
		ActorReviewer:    testActorSourceConfig(ActorReviewer),
	}
	files := make(map[string][]byte)
	for actor, config := range configs {
		appID := map[ActorRole]string{
			ActorCoordinator: "100",
			ActorImplementer: "101",
			ActorReviewer:    "202",
		}[actor]
		installationID := map[ActorRole]string{
			ActorCoordinator: "1000",
			ActorImplementer: "1001",
			ActorReviewer:    "2002",
		}[actor]
		files[config.AppIDFile] = []byte(appID)
		files[config.PrivateKeyFile] = pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privateKeys[actor]),
		})
		files[config.InstallationIDFile] = []byte(installationID)
	}
	readFile := func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, errors.New("unexpected credential file")
		}
		return slices.Clone(value), nil
	}

	sources, err := newActorAppTokenSourcesWithFileReader(http.DefaultClient, &fakeClock{now: time.Now()}, readFile, configs)
	if err != nil {
		t.Fatal(err)
	}
	if sources.Coordinator == nil || sources.Implementer == nil || sources.Reviewer == nil {
		t.Fatalf("sources = %#v", sources)
	}

	reviewerConfig := configs[ActorReviewer]
	implementerConfig := configs[ActorImplementer]
	originalAppID := files[reviewerConfig.AppIDFile]
	originalPrivateKey := files[reviewerConfig.PrivateKeyFile]
	originalInstallationID := files[reviewerConfig.InstallationIDFile]
	for name, mutate := range map[string]func(){
		"App ID": func() {
			files[reviewerConfig.AppIDFile] = files[implementerConfig.AppIDFile]
		},
		"private key": func() {
			files[reviewerConfig.PrivateKeyFile] = files[implementerConfig.PrivateKeyFile]
		},
		"installation ID": func() {
			files[reviewerConfig.InstallationIDFile] = files[implementerConfig.InstallationIDFile]
		},
	} {
		files[reviewerConfig.AppIDFile] = originalAppID
		files[reviewerConfig.PrivateKeyFile] = originalPrivateKey
		files[reviewerConfig.InstallationIDFile] = originalInstallationID
		mutate()
		if _, err := newActorAppTokenSourcesWithFileReader(http.DefaultClient, &fakeClock{now: time.Now()}, readFile, configs); err == nil {
			t.Fatalf("Implementer と Reviewer で同じ %s が受理された", name)
		}
	}
}

func TestAppTokenSourceSignsJWTRequestsActorPermissionsAndCachesUntilRefreshWindow(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	clock := &fakeClock{now: time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		requestNumber := requests
		mu.Unlock()

		if request.Method != http.MethodPost || request.URL.Path != "/app/installations/4242/access_tokens" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("X-GitHub-Api-Version"); got != "2026-03-10" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		assertJWT(t, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), &privateKey.PublicKey, 101, clock.Now())

		var input struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("request body: %v", err)
		}
		wantPermissions, _ := ActorPermissions(ActorImplementer)
		if !reflect.DeepEqual(input.Permissions, wantPermissions) {
			t.Errorf("permissions = %#v, want %#v", input.Permissions, wantPermissions)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       fmt.Sprintf("installation-token-%d", requestNumber),
			"expires_at":  clock.Now().Add(time.Hour).Format(time.RFC3339),
			"permissions": input.Permissions,
		})
	}))
	t.Cleanup(server.Close)

	source := testAppTokenSource(t, server.Client(), clock, privateKey, AppTokenSourceConfig{
		Actor:      ActorImplementer,
		BaseURL:    server.URL,
		APIVersion: "2026-03-10",
	})
	first, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != "installation-token-1" || second != first {
		t.Fatalf("tokens = %q, %q", first, second)
	}
	formatted := fmt.Sprintf("%v %#v", source, source)
	if strings.Contains(formatted, first) || strings.Contains(formatted, privateKey.D.String()) {
		t.Fatalf("formatted App source leaked credential: %s", formatted)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want cached single request", requests)
	}

	clock.Advance(55*time.Minute - time.Nanosecond)
	stillCached, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stillCached != first || requests != 1 {
		t.Fatalf("token before refresh window = %q, requests = %d", stillCached, requests)
	}

	clock.Advance(time.Nanosecond)
	refreshed, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed != "installation-token-2" || requests != 2 {
		t.Fatalf("refreshed token = %q, requests = %d", refreshed, requests)
	}
}

func TestReviewerTokenRequestCannotGainTargetMutationPermissions(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	clock := &fakeClock{now: time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)}
	var issuedPermissions map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/app/installations/4242/access_tokens" {
			if request.Header.Get("Authorization") != "Bearer reviewer-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			permission := "contents"
			if strings.Contains(request.URL.Path, "/pulls/") {
				permission = "pull_requests"
			}
			if issuedPermissions[permission] != "write" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("request body: %v", err)
		}
		if input.Permissions["contents"] != "read" || input.Permissions["pull_requests"] != "read" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Reviewer cannot mutate review target"}`))
			return
		}
		if input.Permissions["checks"] != "write" || input.Permissions["issues"] != "write" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Reviewer cannot record verdict and finding"}`))
			return
		}
		issuedPermissions = maps.Clone(input.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       "reviewer-token",
			"expires_at":  clock.Now().Add(time.Hour).Format(time.RFC3339),
			"permissions": input.Permissions,
		})
	}))
	t.Cleanup(server.Close)

	source := testAppTokenSource(t, server.Client(), clock, privateKey, AppTokenSourceConfig{
		Actor:   ActorReviewer,
		BaseURL: server.URL,
	})
	token, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPut, path: "/repos/acme/widgets/contents/README.md"},
		{method: http.MethodPatch, path: "/repos/acme/widgets/pulls/1"},
	} {
		request, err := http.NewRequestWithContext(t.Context(), target.method, server.URL+target.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", target.method, target.path, response.StatusCode)
		}
	}
}

func TestAppTokenSourceReturnsStructuredTransportFailures(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	now := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		status  int
		headers map[string]string
		body    string
		class   FailureClass
		retry   bool
	}{
		"rate limit": {
			status:  http.StatusTooManyRequests,
			headers: map[string]string{"X-RateLimit-Remaining": "0"},
			body:    `{"message":"API rate limit exceeded"}`,
			class:   FailureRateLimit,
			retry:   true,
		},
		"permission": {
			status: http.StatusForbidden,
			body:   `{"message":"Resource not accessible by integration"}`,
			class:  FailurePermission,
		},
	}

	for name, test := range tests {
		name, test := name, test
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

			source := testAppTokenSource(t, server.Client(), &fakeClock{now: now}, privateKey, AppTokenSourceConfig{
				Actor:   ActorCoordinator,
				BaseURL: server.URL,
			})
			_, err := source.Token(t.Context())
			var failure *TransportFailure
			if !errors.As(err, &failure) || failure.Class != test.class || failure.Retryable() != test.retry {
				t.Fatalf("error = %#v, want class %s retryable %v", err, test.class, test.retry)
			}
			if strings.Contains(failure.Operation, "4242") {
				t.Fatalf("operation leaked installation credential: %q", failure.Operation)
			}
		})
	}
}

func TestAppTokenSourceRejectsTokenWithUnexpectedPermissions(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	clock := &fakeClock{now: time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("request body: %v", err)
		}
		input.Permissions["administration"] = "write"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       "overprivileged-token",
			"expires_at":  clock.Now().Add(time.Hour).Format(time.RFC3339),
			"permissions": input.Permissions,
		})
	}))
	t.Cleanup(server.Close)

	source := testAppTokenSource(t, server.Client(), clock, privateKey, AppTokenSourceConfig{
		Actor:   ActorReviewer,
		BaseURL: server.URL,
	})
	token, err := source.Token(t.Context())
	var failure *TransportFailure
	if token != "" || !errors.As(err, &failure) || failure.Class != FailureInvalidResponse {
		t.Fatalf("Token() = %q, %#v, want rejected overprivileged response", token, err)
	}
}

func TestAppTokenSourceClassifiesTimeout(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	source := testAppTokenSource(t, client, &fakeClock{now: time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)}, privateKey, AppTokenSourceConfig{
		Actor:   ActorCoordinator,
		BaseURL: "https://api.github.invalid",
	})
	_, err := source.Token(t.Context())
	var failure *TransportFailure
	if !errors.As(err, &failure) || failure.Class != FailureTimeout || !failure.Retryable() {
		t.Fatalf("error = %#v, want retryable timeout TransportFailure", err)
	}
}

func TestCredentialFileErrorsDoNotExposePaths(t *testing.T) {
	t.Parallel()

	paths := AppTokenSourceConfig{
		Actor:              ActorReviewer,
		AppIDFile:          "/sensitive/reviewer-app-id",
		PrivateKeyFile:     "/sensitive/reviewer-private-key",
		InstallationIDFile: "/sensitive/reviewer-installation-id",
	}
	_, err := newAppTokenSourceWithFileReader(http.DefaultClient, &fakeClock{now: time.Now()}, func(path string) ([]byte, error) {
		if path == paths.AppIDFile {
			return []byte("101"), nil
		}
		return nil, errors.New("read failed for " + path)
	}, paths)
	if err == nil {
		t.Fatal("error = nil")
	}
	for _, path := range []string{paths.AppIDFile, paths.PrivateKeyFile, paths.InstallationIDFile} {
		if strings.Contains(err.Error(), path) {
			t.Fatalf("error leaked credential path: %v", err)
		}
	}
}

func TestDevelopmentPATTokenSource(t *testing.T) {
	t.Parallel()

	source, err := NewDevelopmentPATTokenSource("github_pat_test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.Token(t.Context())
	if err != nil || got != "github_pat_test" {
		t.Fatalf("Token() = %q, %v", got, err)
	}
	if _, err := NewDevelopmentPATTokenSource(" \n"); err == nil {
		t.Fatal("blank PAT was accepted")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.Token(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Token() error = %v", err)
	}
	if formatted := fmt.Sprintf("%v %#v", source, source); strings.Contains(formatted, "github_pat_test") {
		t.Fatalf("formatted PAT source leaked token: %s", formatted)
	}
}

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}

func testPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func testAppTokenSource(
	t *testing.T,
	client *http.Client,
	clock Clock,
	privateKey *rsa.PrivateKey,
	config AppTokenSourceConfig,
) *AppTokenSource {
	t.Helper()

	config.AppIDFile = "/credentials/app-id"
	config.PrivateKeyFile = "/credentials/private-key"
	config.InstallationIDFile = "/credentials/installation-id"
	files := map[string][]byte{
		config.AppIDFile:          []byte("101\n"),
		config.PrivateKeyFile:     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
		config.InstallationIDFile: []byte("4242\n"),
	}
	source, err := newAppTokenSourceWithFileReader(client, clock, func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, errors.New("unexpected credential file")
		}
		return slices.Clone(value), nil
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func testActorSourceConfig(actor ActorRole) AppTokenSourceConfig {
	prefix := "/credentials/" + string(actor)
	return AppTokenSourceConfig{
		Actor:              actor,
		AppIDFile:          prefix + "-app-id",
		PrivateKeyFile:     prefix + "-private-key",
		InstallationIDFile: prefix + "-installation-id",
	}
}

func assertJWT(t *testing.T, token string, publicKey *rsa.PublicKey, appID int64, now time.Time) {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("JWT segments = %d", len(parts))
		return
	}
	decode := func(value string) []byte {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Errorf("decode JWT: %v", err)
		}
		return decoded
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Errorf("decode JWT header: %v", err)
	}
	if header.Algorithm != "RS256" || header.Type != "JWT" {
		t.Errorf("JWT header = %#v", header)
	}
	var claims struct {
		Issuer   int64 `json:"iss"`
		IssuedAt int64 `json:"iat"`
		Expires  int64 `json:"exp"`
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Errorf("decode JWT claims: %v", err)
	}
	if claims.Issuer != appID {
		t.Errorf("JWT issuer = %d, want %d", claims.Issuer, appID)
	}
	if claims.IssuedAt > now.Unix() || claims.Expires <= now.Unix() || claims.Expires > now.Add(10*time.Minute).Unix() {
		t.Errorf("JWT times = iat %d exp %d at %d", claims.IssuedAt, claims.Expires, now.Unix())
	}
	if claims.Expires-claims.IssuedAt > int64((10*time.Minute)/time.Second) {
		t.Errorf("JWT lifetime = %ds, want <= 600s", claims.Expires-claims.IssuedAt)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], decode(parts[2])); err != nil {
		t.Errorf("JWT signature: %v", err)
	}
}
