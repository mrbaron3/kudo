package github

import (
	"bytes"
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
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	installationTokenOperation     = "POST installation access token"
	providerGitHubTokenEnv         = "GH_TOKEN"
	jwtClockSkew                   = time.Minute
	jwtValidityAfterIssue          = 9 * time.Minute
	installationTokenRefreshWindow = 5 * time.Minute
)

// ActorRole は credential と GitHub 上の発話主体を結び付ける。
// 任意の文字列を許すと未知 actor が強い既定権限へ流れるため、定義済み値以外は拒否する。
type ActorRole string

const (
	ActorCoordinator ActorRole = "coordinator"
	ActorImplementer ActorRole = "implementer"
	ActorReviewer    ActorRole = "reviewer"
)

var actorPermissionSubsets = map[ActorRole]map[string]string{
	ActorCoordinator: {
		"metadata":      "read",
		"issues":        "write",
		"pull_requests": "read",
		"checks":        "read",
	},
	ActorImplementer: {
		"metadata":      "read",
		"issues":        "write",
		"contents":      "write",
		"pull_requests": "write",
		"checks":        "write",
	},
	ActorReviewer: {
		"metadata": "read",
		// GitHub は PR conversation comment を Issues permission で表現する。
		// pull_requests は read のままなので、Reviewer は PR state や本文を変更できない。
		"issues":        "write",
		"contents":      "read",
		"pull_requests": "read",
		"checks":        "write",
	},
}

// ActorPermissions は runtime-platform の actor authority を GitHub REST permission へ写像する。
// 返却 map は呼び出し側専用の copy であり、他 actor や後続発行へ変更を波及させない。
func ActorPermissions(actor ActorRole) (map[string]string, error) {
	permissions, ok := actorPermissionSubsets[actor]
	if !ok {
		return nil, fmt.Errorf("GitHub actor role が不正: %q", actor)
	}
	return maps.Clone(permissions), nil
}

// ProviderCredentialEnvironmentAllowlist は provider child へ渡してよい GitHub credential 名を返す。
// App key や *_FILE path は child へ渡さず、operation 用の短命 token だけを許可する。
func ProviderCredentialEnvironmentAllowlist(actor ActorRole) ([]string, error) {
	if _, err := ActorPermissions(actor); err != nil {
		return nil, err
	}
	return []string{providerGitHubTokenEnv}, nil
}

// Clock は token の有効期限と JWT claim を同じ時刻源へ固定する。
type Clock interface {
	Now() time.Time
}

// AppTokenSourceConfig は一つの actor と対象 repository に GitHub App installation を固定する。
// credential path を含むため、この値を log、record surface、telemetry へ出してはならない。
type AppTokenSourceConfig struct {
	Actor              ActorRole
	Repository         Repository
	AppIDFile          string
	PrivateKeyFile     string
	InstallationIDFile string
	BaseURL            string
	APIVersion         string
}

type appCredentialEnvironmentKeys struct {
	appID          string
	privateKey     string
	installationID string
}

// AppTokenSourceConfigFromEnvironment は actor ごとの *_FILE 設定だけを読み取る。
// file 内容は constructor まで読み込まず、欠落や空値を起動時設定エラーとして拒否する。
func AppTokenSourceConfigFromEnvironment(
	actor ActorRole,
	repository Repository,
	lookupEnv func(string) (string, bool),
) (AppTokenSourceConfig, error) {
	if lookupEnv == nil {
		return AppTokenSourceConfig{}, errors.New("environment lookup は必須")
	}
	keys, err := credentialEnvironmentKeys(actor)
	if err != nil {
		return AppTokenSourceConfig{}, err
	}
	if err := validateRepository(repository); err != nil {
		return AppTokenSourceConfig{}, err
	}
	readPath := func(key string) (string, error) {
		value, ok := lookupEnv(key)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return "", fmt.Errorf("required configuration %s が欠落または不正", key)
		}
		return value, nil
	}
	appIDFile, err := readPath(keys.appID)
	if err != nil {
		return AppTokenSourceConfig{}, err
	}
	privateKeyFile, err := readPath(keys.privateKey)
	if err != nil {
		return AppTokenSourceConfig{}, err
	}
	installationIDFile, err := readPath(keys.installationID)
	if err != nil {
		return AppTokenSourceConfig{}, err
	}
	return AppTokenSourceConfig{
		Actor:              actor,
		Repository:         repository.canonical(),
		AppIDFile:          appIDFile,
		PrivateKeyFile:     privateKeyFile,
		InstallationIDFile: installationIDFile,
	}, nil
}

func credentialEnvironmentKeys(actor ActorRole) (appCredentialEnvironmentKeys, error) {
	var actorName string
	switch actor {
	case ActorCoordinator:
		actorName = "COORDINATOR"
	case ActorImplementer:
		actorName = "IMPLEMENTER"
	case ActorReviewer:
		actorName = "REVIEWER"
	default:
		return appCredentialEnvironmentKeys{}, fmt.Errorf("GitHub actor role が不正: %q", actor)
	}
	prefix := "KUDO_GITHUB_" + actorName + "_"
	return appCredentialEnvironmentKeys{
		appID:          prefix + "APP_ID_FILE",
		privateKey:     prefix + "PRIVATE_KEY_FILE",
		installationID: prefix + "INSTALLATION_ID_FILE",
	}, nil
}

type credentialFileReader func(string) ([]byte, error)

type cachedInstallationToken struct {
	value     string
	expiresAt time.Time
}

type installationTokenRefresh struct {
	done    chan struct{}
	failure error
}

// AppTokenSource は一つの App identity、permission subset、repository に束縛される。
// Token は concurrent-safe で、同じ instance の refresh を一つにまとめる。
type AppTokenSource struct {
	client         *http.Client
	clock          Clock
	appID          int64
	privateKey     *rsa.PrivateKey
	installationID int64
	permissions    map[string]string
	repository     Repository
	baseURL        string
	apiVersion     string

	mu      sync.Mutex
	cached  cachedInstallationToken
	refresh *installationTokenRefresh
}

var _ TokenSource = (*AppTokenSource)(nil)

func (*AppTokenSource) String() string {
	return "GitHub App TokenSource [REDACTED]"
}

func (s *AppTokenSource) GoString() string {
	return s.String()
}

// ActorAppTokenSources は一つの Task repository に束縛された actor 別 source 集合である。
// Implementer と Reviewer は異なる App ID でなければ構築できない。
type ActorAppTokenSources struct {
	Coordinator *AppTokenSource
	Implementer *AppTokenSource
	Reviewer    *AppTokenSource
}

// NewActorAppTokenSources は required actor をまとめて検証し、identity 分離済みの source を返す。
// production の設定境界では、単体 constructor ではなくこの関数を使う。
func NewActorAppTokenSources(
	client *http.Client,
	clock Clock,
	configs map[ActorRole]AppTokenSourceConfig,
) (ActorAppTokenSources, error) {
	return newActorAppTokenSourcesWithFileReader(client, clock, os.ReadFile, configs)
}

func newActorAppTokenSourcesWithFileReader(
	client *http.Client,
	clock Clock,
	readFile credentialFileReader,
	configs map[ActorRole]AppTokenSourceConfig,
) (ActorAppTokenSources, error) {
	roles := []ActorRole{ActorCoordinator, ActorImplementer, ActorReviewer}
	if len(configs) != len(roles) {
		return ActorAppTokenSources{}, errors.New("Coordinator、Implementer、Reviewer の GitHub App 設定が必要")
	}
	sources := make(map[ActorRole]*AppTokenSource, len(roles))
	var repository Repository
	for _, role := range roles {
		config, ok := configs[role]
		if !ok || config.Actor != role {
			return ActorAppTokenSources{}, fmt.Errorf("%s の GitHub App 設定が欠落または不一致", role)
		}
		source, err := newAppTokenSourceWithFileReader(client, clock, readFile, config)
		if err != nil {
			return ActorAppTokenSources{}, fmt.Errorf("%s GitHub App を構成できない: %w", role, err)
		}
		if len(sources) == 0 {
			repository = source.repository
		} else if source.repository != repository {
			return ActorAppTokenSources{}, errors.New("actor 別 GitHub App は同じ Task repository を対象にする必要がある")
		}
		sources[role] = source
	}
	implementer := sources[ActorImplementer]
	reviewer := sources[ActorReviewer]
	if implementer.appID == reviewer.appID ||
		implementer.installationID == reviewer.installationID ||
		(implementer.privateKey.E == reviewer.privateKey.E && implementer.privateKey.N.Cmp(reviewer.privateKey.N) == 0) {
		return ActorAppTokenSources{}, errors.New("Implementer と Reviewer には異なる GitHub App credential が必要")
	}
	return ActorAppTokenSources{
		Coordinator: sources[ActorCoordinator],
		Implementer: sources[ActorImplementer],
		Reviewer:    sources[ActorReviewer],
	}, nil
}

// NewAppTokenSource は *_FILE の credential を一度だけ読み、actor-scoped source を返す。
// file path や private key を含む設定エラーの詳細は、secret path の漏洩を避けて返さない。
// production で required actor を構成する場合は identity 分離も検証する NewActorAppTokenSources を使う。
func NewAppTokenSource(client *http.Client, clock Clock, config AppTokenSourceConfig) (*AppTokenSource, error) {
	return newAppTokenSourceWithFileReader(client, clock, os.ReadFile, config)
}

func newAppTokenSourceWithFileReader(
	client *http.Client,
	clock Clock,
	readFile credentialFileReader,
	config AppTokenSourceConfig,
) (*AppTokenSource, error) {
	if client == nil {
		return nil, errors.New("GitHub App HTTP client は必須")
	}
	if clock == nil {
		return nil, errors.New("GitHub App token clock は必須")
	}
	if readFile == nil {
		return nil, errors.New("GitHub App credential file reader は必須")
	}
	permissions, err := ActorPermissions(config.Actor)
	if err != nil {
		return nil, err
	}
	if err := validateRepository(config.Repository); err != nil {
		return nil, err
	}
	fileSettings := []struct {
		label string
		path  string
	}{
		{label: "App ID", path: config.AppIDFile},
		{label: "private key", path: config.PrivateKeyFile},
		{label: "installation ID", path: config.InstallationIDFile},
	}
	for _, setting := range fileSettings {
		if setting.path == "" || setting.path != strings.TrimSpace(setting.path) {
			return nil, fmt.Errorf("GitHub App %s file configuration が欠落または不正", setting.label)
		}
	}
	appID, err := readPositiveCredentialID(readFile, config.AppIDFile, "App ID")
	if err != nil {
		return nil, err
	}
	privateKey, err := readPrivateKey(readFile, config.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	installationID, err := readPositiveCredentialID(readFile, config.InstallationIDFile, "installation ID")
	if err != nil {
		return nil, err
	}
	baseURL, err := appAPIBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	apiVersion := config.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	if strings.ContainsFunc(apiVersion, unicode.IsSpace) {
		return nil, errors.New("GitHub App API version が不正")
	}
	return &AppTokenSource{
		client:         client,
		clock:          clock,
		appID:          appID,
		privateKey:     privateKey,
		installationID: installationID,
		permissions:    permissions,
		repository:     config.Repository.canonical(),
		baseURL:        baseURL,
		apiVersion:     apiVersion,
	}, nil
}

func readPositiveCredentialID(readFile credentialFileReader, path, label string) (int64, error) {
	data, err := readCredentialFile(readFile, path, label)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(data))
	id, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil || id <= 0 {
		return 0, fmt.Errorf("GitHub App %s file の内容が不正", label)
	}
	return id, nil
}

func readPrivateKey(readFile credentialFileReader, path string) (*rsa.PrivateKey, error) {
	data, err := readCredentialFile(readFile, path, "private key")
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("GitHub App private key file の内容が不正")
	}
	var privateKey *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			privateKey, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				err = errors.New("private key is not RSA")
			}
		}
	default:
		err = errors.New("unsupported PEM block")
	}
	if err != nil || privateKey.Validate() != nil {
		return nil, errors.New("GitHub App private key file の内容が不正")
	}
	return privateKey, nil
}

func readCredentialFile(readFile credentialFileReader, path, label string) ([]byte, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("GitHub App %s file を読み込めない", label)
	}
	if len(data) == 0 || len(data) > 1024*1024 {
		return nil, fmt.Errorf("GitHub App %s file の内容が不正", label)
	}
	return data, nil
}

func appAPIBaseURL(value string) (string, error) {
	value = strings.TrimRight(value, "/")
	if value == "" {
		value = DefaultBaseURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GitHub App BaseURL が不正")
	}
	return value, nil
}

// Token は送信中の失効と GitHub との clock skew を避けるため、期限の5分前まで
// installation token を再利用し、refresh window に入った最初の呼び出しで再発行する。
func (s *AppTokenSource) Token(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", &TransportFailure{
			Class:     FailureCredential,
			Operation: installationTokenOperation,
			Message:   "context は必須",
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", tokenContextFailure(ctx, err)
		}
		now := s.clock.Now().UTC()

		s.mu.Lock()
		if s.cached.value != "" && now.Before(s.cached.expiresAt.Add(-installationTokenRefreshWindow)) {
			token := s.cached.value
			s.mu.Unlock()
			return token, nil
		}
		if s.refresh != nil {
			refresh := s.refresh
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", tokenContextFailure(ctx, ctx.Err())
			case <-refresh.done:
				if err := ctx.Err(); err != nil {
					return "", tokenContextFailure(ctx, err)
				}
				if refresh.failure != nil {
					return "", refresh.failure
				}
				continue
			}
		}

		refresh := &installationTokenRefresh{done: make(chan struct{})}
		s.refresh = refresh
		s.mu.Unlock()

		token, expiresAt, err := s.fetch(ctx, now)
		s.mu.Lock()
		if err == nil {
			s.cached = cachedInstallationToken{value: token, expiresAt: expiresAt}
		} else if tokenRefreshFailureShareable(ctx, err) {
			refresh.failure = err
		}
		s.refresh = nil
		close(refresh.done)
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

func tokenRefreshFailureShareable(ctx context.Context, err error) bool {
	callerErr := ctx.Err()
	return callerErr == nil || !errors.Is(err, callerErr)
}

func tokenContextFailure(ctx context.Context, err error) *TransportFailure {
	return &TransportFailure{
		Class:     classifyContextError(ctx, err, FailureCredential),
		Operation: installationTokenOperation,
		Message:   "installation token request を開始できない",
		Err:       err,
	}
}

func (s *AppTokenSource) fetch(ctx context.Context, now time.Time) (string, time.Time, error) {
	jwt, err := s.signJWT(now)
	if err != nil {
		return "", time.Time{}, &TransportFailure{
			Class:     FailureCredential,
			Operation: installationTokenOperation,
			Message:   "GitHub App JWT を署名できない",
			Err:       err,
		}
	}
	input, err := json.Marshal(struct {
		Permissions  map[string]string `json:"permissions"`
		Repositories []string          `json:"repositories"`
	}{
		Permissions: maps.Clone(s.permissions),
		// installation ID が owner account を固定するため、この API は repository 名だけを受け取る。
		Repositories: []string{s.repository.Name},
	})
	if err != nil {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "permission request を encode できない", err)
	}
	endpoint := s.baseURL + "/app/installations/" + strconv.FormatInt(s.installationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(input))
	if err != nil {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "request を構築できない", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "kudo-github-app-token-source")
	request.Header.Set("X-GitHub-Api-Version", s.apiVersion)

	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, &TransportFailure{
			Class:     classifyContextError(ctx, err, FailureNetwork),
			Operation: installationTokenOperation,
			Message:   "installation token request を完了できない",
			Err:       err,
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return "", time.Time{}, &TransportFailure{
			Class:      classifyContextError(ctx, err, FailureNetwork),
			Operation:  installationTokenOperation,
			StatusCode: response.StatusCode,
			RequestID:  response.Header.Get("X-GitHub-Request-Id"),
			Message:    "installation token response を読み取れない",
			Err:        err,
		}
	}
	if len(data) > maxResponseBytes {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response body が上限を超えた", nil)
	}
	if response.StatusCode != http.StatusCreated {
		return "", time.Time{}, classifyHTTPFailure(installationTokenOperation, response, data)
	}
	var output struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response を decode できない", err)
	}
	if output.Token == "" || output.Token != strings.TrimSpace(output.Token) || strings.ContainsFunc(output.Token, unicode.IsSpace) {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response token が欠落または不正", nil)
	}
	if !now.Before(output.ExpiresAt) {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response token の期限が切れている", nil)
	}
	if !maps.Equal(output.Permissions, s.permissions) {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response permission が要求 subset と一致しない", nil)
	}
	if len(output.Repositories) != 1 || !strings.EqualFold(output.Repositories[0].FullName, s.repository.Owner+"/"+s.repository.Name) {
		return "", time.Time{}, invalidResponse(installationTokenOperation, "response repository が対象 Task repository と一致しない", nil)
	}
	return output.Token, output.ExpiresAt.UTC(), nil
}

func (s *AppTokenSource) signJWT(now time.Time) (string, error) {
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		IssuedAt int64 `json:"iat"`
		Expires  int64 `json:"exp"`
		Issuer   int64 `json:"iss"`
	}{
		IssuedAt: now.Add(-jwtClockSkew).Unix(),
		Expires:  now.Add(jwtValidityAfterIssue).Unix(),
		Issuer:   s.appID,
	})
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claims)
	signingInput := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// DevelopmentPATTokenSource は S1〜S2 の dev / test だけで使う固定 credential である。
// GitHub App identity を提供しないため、production の既定や Reviewer verdict 記録へ使ってはならない。
type DevelopmentPATTokenSource struct {
	token string
}

var _ TokenSource = (*DevelopmentPATTokenSource)(nil)

func (*DevelopmentPATTokenSource) String() string {
	return "development PAT TokenSource [REDACTED]"
}

func (s *DevelopmentPATTokenSource) GoString() string {
	return s.String()
}

// NewDevelopmentPATTokenSource は呼び出し側が明示した dev / test PAT だけを保持する。
// environment や file を暗黙探索しないため、production 設定から誤って選択される既定経路を作らない。
func NewDevelopmentPATTokenSource(token string) (*DevelopmentPATTokenSource, error) {
	if token == "" || token != strings.TrimSpace(token) || strings.ContainsFunc(token, unicode.IsSpace) {
		return nil, errors.New("development PAT が欠落または不正")
	}
	return &DevelopmentPATTokenSource{token: token}, nil
}

func (s *DevelopmentPATTokenSource) Token(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("context は必須")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.token, nil
}
