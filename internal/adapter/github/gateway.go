package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxResponseBytes = 16 * 1024 * 1024
	maxPages         = 10_000
)

type FailureClass string

const (
	FailureCanceled           FailureClass = "canceled"
	FailureTimeout            FailureClass = "timeout"
	FailureNetwork            FailureClass = "network"
	FailureCredential         FailureClass = "credential"
	FailureRateLimit          FailureClass = "rate_limit"
	FailureSecondaryRateLimit FailureClass = "secondary_rate_limit"
	FailurePermission         FailureClass = "permission"
	FailureNotFound           FailureClass = "not_found"
	FailureConflict           FailureClass = "conflict"
	FailureInvalidRequest     FailureClass = "invalid_request"
	FailureUnavailable        FailureClass = "unavailable"
	FailureInvalidResponse    FailureClass = "invalid_response"
)

// TransportFailure は GitHub I/O の失敗だけを表し、contract rejection や
// review verdict とは別の値空間を保つ。機械判定には Class を使う。
type TransportFailure struct {
	Class          FailureClass
	Operation      string
	StatusCode     int
	RequestID      string
	RetryAfter     time.Duration
	RateLimitReset time.Time
	Message        string
	Err            error
}

func (e *TransportFailure) Error() string {
	detail := e.Message
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("GitHub %s failed (%s, HTTP %d): %s", e.Operation, e.Class, e.StatusCode, detail)
	}
	return fmt.Sprintf("GitHub %s failed (%s): %s", e.Operation, e.Class, detail)
}

func (e *TransportFailure) Unwrap() error { return e.Err }

func (e *TransportFailure) Retryable() bool {
	switch e.Class {
	case FailureTimeout, FailureNetwork, FailureRateLimit, FailureSecondaryRateLimit, FailureUnavailable:
		return true
	default:
		return false
	}
}

// NewGateway は一つの HTTP client、TokenSource、repository に束縛された instance を返す。
func NewGateway(client *http.Client, tokens TokenSource, config Config) (*Gateway, error) {
	if client == nil {
		return nil, errors.New("GitHub HTTP client は必須")
	}
	if tokens == nil {
		return nil, errors.New("GitHub TokenSource は必須")
	}
	if err := validateRepository(config.Repository); err != nil {
		return nil, err
	}
	var recorder *RecorderIdentity
	if config.RecorderIdentity != nil {
		if config.RecorderIdentity.CommentAuthor.ID <= 0 || config.RecorderIdentity.CheckRunApp.ID <= 0 {
			return nil, errors.New("RecorderIdentity には正の comment author ID と check run App ID が必要")
		}
		identity := *config.RecorderIdentity
		recorder = &identity
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("GitHub BaseURL が不正: %q", config.BaseURL)
	}
	apiVersion := config.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	if strings.ContainsFunc(apiVersion, unicode.IsSpace) || apiVersion == "" {
		return nil, fmt.Errorf("GitHub API version が不正: %q", apiVersion)
	}
	return &Gateway{
		client:     client,
		tokens:     tokens,
		baseURL:    baseURL,
		apiVersion: apiVersion,
		repository: config.Repository.canonical(),
		recorder:   recorder,
	}, nil
}

func validateRepository(repository Repository) error {
	validPart := func(value string) bool {
		return value != "" && len(value) <= 255 && value != "." && value != ".." &&
			!strings.ContainsAny(value, "/\\") && !strings.ContainsFunc(value, unicode.IsControl)
	}
	if !validPart(repository.Owner) || !validPart(repository.Name) {
		return fmt.Errorf("GitHub repository identity が不正: %q/%q", repository.Owner, repository.Name)
	}
	return nil
}

type responseData struct {
	Status int
	Header http.Header
	Body   []byte
}

func (g *Gateway) request(
	ctx context.Context,
	method string,
	requestURL string,
	input any,
	accepted ...int,
) (responseData, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return responseData{}, fmt.Errorf("GitHub request body を encode する: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return responseData{}, fmt.Errorf("GitHub request を構築する: %w", err)
	}
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return responseData{}, &TransportFailure{
			Class:     classifyContextError(ctx, err, FailureCredential),
			Operation: method + " " + request.URL.Path,
			Message:   "actor credential を取得できない",
			Err:       err,
		}
	}
	if token == "" {
		return responseData{}, &TransportFailure{
			Class:     FailureCredential,
			Operation: method + " " + request.URL.Path,
			Message:   "TokenSource が空 token を返した",
		}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", g.apiVersion)
	request.Header.Set("User-Agent", "kudo-github-gateway")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := g.client.Do(request)
	if err != nil {
		return responseData{}, &TransportFailure{
			Class:     classifyContextError(ctx, err, FailureNetwork),
			Operation: method + " " + request.URL.Path,
			Message:   "request を完了できない",
			Err:       err,
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return responseData{}, &TransportFailure{
			Class:      classifyContextError(ctx, err, FailureNetwork),
			Operation:  method + " " + request.URL.Path,
			StatusCode: response.StatusCode,
			RequestID:  response.Header.Get("X-GitHub-Request-Id"),
			Message:    "response body を読み取れない",
			Err:        err,
		}
	}
	if len(data) > maxResponseBytes {
		return responseData{}, invalidResponse(method+" "+request.URL.Path, "response body が上限を超えた", nil)
	}
	result := responseData{Status: response.StatusCode, Header: response.Header.Clone(), Body: data}
	for _, status := range accepted {
		if response.StatusCode == status {
			return result, nil
		}
	}
	return responseData{}, classifyHTTPFailure(method+" "+request.URL.Path, response, data)
}

func classifyContextError(ctx context.Context, err error, fallback FailureClass) FailureClass {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return FailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return FailureTimeout
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return FailureTimeout
	}
	return fallback
}

func classifyHTTPFailure(operation string, response *http.Response, body []byte) *TransportFailure {
	message := responseMessage(body)
	lowerMessage := strings.ToLower(message)
	class := FailureInvalidResponse
	switch {
	case (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests) &&
		response.Header.Get("Retry-After") != "":
		class = FailureSecondaryRateLimit
	case response.StatusCode == http.StatusForbidden &&
		(strings.Contains(lowerMessage, "secondary rate limit") || strings.Contains(lowerMessage, "abuse detection")):
		class = FailureSecondaryRateLimit
	case (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests) &&
		response.Header.Get("X-RateLimit-Remaining") == "0":
		class = FailureRateLimit
	case response.StatusCode == http.StatusTooManyRequests:
		class = FailureRateLimit
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		class = FailurePermission
	case response.StatusCode == http.StatusNotFound:
		class = FailureNotFound
	case response.StatusCode == http.StatusConflict:
		class = FailureConflict
	case response.StatusCode == http.StatusUnprocessableEntity || response.StatusCode == http.StatusBadRequest:
		class = FailureInvalidRequest
	case response.StatusCode >= 500:
		class = FailureUnavailable
	}
	failure := &TransportFailure{
		Class:      class,
		Operation:  operation,
		StatusCode: response.StatusCode,
		RequestID:  response.Header.Get("X-GitHub-Request-Id"),
		Message:    message,
	}
	if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
		failure.RetryAfter = time.Duration(seconds) * time.Second
	}
	if reset, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
		failure.RateLimitReset = time.Unix(reset, 0).UTC()
	}
	return failure
}

func responseMessage(data []byte) string {
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Message != "" {
		return truncateMessage(envelope.Message)
	}
	return truncateMessage(strings.TrimSpace(string(data)))
}

func truncateMessage(value string) string {
	const limit = 1024
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func invalidResponse(operation, message string, err error) *TransportFailure {
	return &TransportFailure{Class: FailureInvalidResponse, Operation: operation, Message: message, Err: err}
}

func (g *Gateway) endpoint(path string, query url.Values) string {
	result := g.baseURL + path
	if len(query) > 0 {
		result += "?" + query.Encode()
	}
	return result
}

func (g *Gateway) paginate(
	ctx context.Context,
	path string,
	query url.Values,
	decode func([]byte) error,
) error {
	query = cloneValues(query)
	if query.Get("per_page") == "" {
		query.Set("per_page", "100")
	}
	next := g.endpoint(path, query)
	seen := make(map[string]struct{})
	for page := 0; next != ""; page++ {
		if page >= maxPages {
			return invalidResponse("paginate "+path, "pagination page 上限を超えた", nil)
		}
		if _, exists := seen[next]; exists {
			return invalidResponse("paginate "+path, "pagination cycle を検出した", nil)
		}
		seen[next] = struct{}{}
		response, err := g.request(ctx, http.MethodGet, next, nil, http.StatusOK)
		if err != nil {
			return err
		}
		if err := decode(response.Body); err != nil {
			return invalidResponse("GET "+path, "paginated response を decode できない", err)
		}
		next, err = g.nextPageURL(next, response.Header.Get("Link"))
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *Gateway) nextPageURL(currentURL, header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", nil
	}
	links, err := parseLinkHeader(header)
	if err != nil {
		return "", invalidResponse("parse Link", "pagination Link header が不正", err)
	}
	next := links["next"]
	if next == "" {
		return "", nil
	}
	base, _ := url.Parse(g.baseURL)
	current, err := url.Parse(currentURL)
	if err != nil {
		return "", invalidResponse("parse Link", "current pagination URL が不正", err)
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return "", invalidResponse("parse Link", "pagination next URL が不正", err)
	}
	parsed = current.ResolveReference(parsed)
	if !strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host) {
		return "", invalidResponse("parse Link", "pagination next URL の origin が configured API と異なる", nil)
	}
	return parsed.String(), nil
}

func parseLinkHeader(header string) (map[string]string, error) {
	result := make(map[string]string)
	for _, entry := range splitLinkEntries(header) {
		entry = strings.TrimSpace(entry)
		if !strings.HasPrefix(entry, "<") {
			return nil, fmt.Errorf("URL の開始 '<' がない")
		}
		close := strings.Index(entry, ">")
		if close < 2 {
			return nil, fmt.Errorf("URL の終了 '>' がない")
		}
		target := entry[1:close]
		params := strings.Split(entry[close+1:], ";")
		var relations []string
		for _, parameter := range params {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(key, "rel") {
				continue
			}
			value = strings.Trim(value, `"`)
			relations = strings.Fields(value)
		}
		if len(relations) == 0 {
			return nil, fmt.Errorf("rel parameter がない")
		}
		for _, relation := range relations {
			result[relation] = target
		}
	}
	return result, nil
}

func splitLinkEntries(header string) []string {
	var entries []string
	start := 0
	quoted := false
	for index, character := range header {
		switch character {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				entries = append(entries, header[start:index])
				start = index + 1
			}
		}
	}
	entries = append(entries, header[start:])
	return entries
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, input := range values {
		cloned[key] = append([]string(nil), input...)
	}
	return cloned
}
