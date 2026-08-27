package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mrbaron3/kudo/internal/workflow"
)

type apiUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type apiLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (label *apiLabel) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		label.Name = name
		return nil
	}
	type plainLabel apiLabel
	var value plainLabel
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*label = apiLabel(value)
	return nil
}

type apiIssue struct {
	ID            int64           `json:"id"`
	NodeID        string          `json:"node_id"`
	RepositoryURL string          `json:"repository_url"`
	Number        int64           `json:"number"`
	State         string          `json:"state"`
	StateReason   string          `json:"state_reason"`
	Title         string          `json:"title"`
	Body          json.RawMessage `json:"body"`
	User          apiUser         `json:"user"`
	Assignees     []apiUser       `json:"assignees"`
	Labels        []apiLabel      `json:"labels"`
	PullRequest   json.RawMessage `json:"pull_request"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	ClosedAt      *time.Time      `json:"closed_at"`
}

type apiComment struct {
	ID        int64           `json:"id"`
	NodeID    string          `json:"node_id"`
	Body      json.RawMessage `json:"body"`
	User      apiUser         `json:"user"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type apiBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type apiGitReference struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

type apiPullRequest struct {
	ID     int64           `json:"id"`
	NodeID string          `json:"node_id"`
	Number int64           `json:"number"`
	State  string          `json:"state"`
	Draft  bool            `json:"draft"`
	Title  string          `json:"title"`
	Body   json.RawMessage `json:"body"`
	User   apiUser         `json:"user"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	MergedAt  *time.Time `json:"merged_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type apiCheckRun struct {
	ID         int64  `json:"id"`
	NodeID     string `json:"node_id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"app"`
	Output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type apiIssueEvent struct {
	ID        int64     `json:"id"`
	Event     string    `json:"event"`
	Actor     apiUser   `json:"actor"`
	Label     apiLabel  `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

type apiContent struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// ObserveIssue は live Issue とその固定 branch、Pull Request、record surface を読み、
// phase 判定を含まない snapshot を組み立てる。
func (g *Gateway) ObserveIssue(ctx context.Context, number int64) (IssueSnapshot, error) {
	if number <= 0 {
		return IssueSnapshot{}, fmt.Errorf("Issue number は正数でなければならない")
	}
	observed, err := g.getIssue(ctx, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	if observed.Issue.Number != number {
		return IssueSnapshot{}, invalidResponse("GET issue", "response の Issue number が request と一致しない", nil)
	}
	parent, err := g.getParentIssue(ctx, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	subIssues, err := g.listSubIssues(ctx, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	issueComments, err := g.listComments(ctx, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	labelEvents, err := g.ListIssueLabelEvents(ctx, number)
	if err != nil {
		return IssueSnapshot{}, err
	}
	branchName := workflow.IssueBranchName(number)
	branch, err := g.getBranch(ctx, branchName)
	if err != nil {
		return IssueSnapshot{}, err
	}
	pulls, err := g.listPullRequests(ctx, branchName)
	if err != nil {
		return IssueSnapshot{}, err
	}
	for index := range pulls {
		comments, listErr := g.listComments(ctx, pulls[index].Number)
		if listErr != nil {
			return IssueSnapshot{}, listErr
		}
		checks, listErr := g.listCheckRuns(ctx, pulls[index].Head.SHA, "")
		if listErr != nil {
			return IssueSnapshot{}, listErr
		}
		pulls[index].Comments = comments
		pulls[index].CheckRuns = checks
	}
	return IssueSnapshot{
		Issue:         observed.Issue,
		RawBody:       cloneBytes(observed.RawBody),
		Parent:        parent,
		SubIssues:     subIssues,
		IssueComments: issueComments,
		LabelEvents:   labelEvents,
		Branch:        branch,
		PullRequests:  pulls,
	}, nil
}

// ListCandidateIssues は routing query の全 page を読み、Pull Request を除外する。
// 同じ Issue が page 境界に重複して現れた場合は最初の exact 観測へ収束する。
func (g *Gateway) ListCandidateIssues(ctx context.Context, filter CandidateFilter) ([]ObservedIssue, error) {
	if filter.Assignee == "" || filter.Label == "" {
		return nil, fmt.Errorf("candidate assignee と label は必須")
	}
	query := url.Values{
		"state":     {"open"},
		"assignee":  {filter.Assignee},
		"labels":    {filter.Label},
		"sort":      {"created"},
		"direction": {"asc"},
	}
	seen := make(map[int64]struct{})
	var result []ObservedIssue
	err := g.paginate(ctx, g.repositoryPath("issues"), query, func(data []byte) error {
		var page []apiIssue
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			if hasPullRequest(value.PullRequest) {
				continue
			}
			if _, exists := seen[value.Number]; exists {
				continue
			}
			observed, err := g.convertObservedIssue(value)
			if err != nil {
				return err
			}
			if observed.Issue.Repository.canonical() != g.repository {
				return fmt.Errorf("candidate Issue repository が configured repository と一致しない")
			}
			seen[value.Number] = struct{}{}
			result = append(result, observed)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListIssueLabelEvents は Issue の label 付与・除去を発生順で返す。
//
// 現在の label set では復元できない事実（直近の`ai-ready`付与、Kudo 自身が完了を
// 記録したか）を導出へ渡すために必要である。label 以外の event は落とし、workflow が
// GitHub の event 語彙全体に依存しないようにする。
func (g *Gateway) ListIssueLabelEvents(ctx context.Context, issueNumber int64) ([]IssueLabelEvent, error) {
	if issueNumber <= 0 {
		return nil, fmt.Errorf("Issue number は正数でなければならない")
	}
	seen := make(map[int64]struct{})
	var result []IssueLabelEvent
	err := g.paginate(ctx, g.issuePath(issueNumber)+"/events", nil, func(data []byte) error {
		var page []apiIssueEvent
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			added := value.Event == "labeled"
			if !added && value.Event != "unlabeled" {
				continue
			}
			if value.Label.Name == "" {
				return fmt.Errorf("label event に label name が無い")
			}
			if _, exists := seen[value.ID]; exists {
				continue
			}
			seen[value.ID] = struct{}{}
			result = append(result, IssueLabelEvent{
				ID:         value.ID,
				Label:      value.Label.Name,
				Added:      added,
				Actor:      Actor{ID: value.Actor.ID, Login: value.Actor.Login},
				OccurredAt: value.CreatedAt,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReadContent は指定 ref の repository file bytes を返す。base64 decode 後の bytes を
// newline 変換せず保持し、directory response は曖昧な成功として扱わない。
func (g *Gateway) ReadContent(ctx context.Context, filePath, ref string) (RepositoryContent, error) {
	escapedPath, err := escapeRepositoryPath(filePath)
	if err != nil {
		return RepositoryContent{}, err
	}
	if ref == "" {
		return RepositoryContent{}, fmt.Errorf("repository content ref は必須")
	}
	response, err := g.request(ctx, http.MethodGet, g.endpoint(
		g.repositoryPath("contents")+"/"+escapedPath,
		url.Values{"ref": {ref}},
	), nil, http.StatusOK)
	if err != nil {
		return RepositoryContent{}, err
	}
	var value apiContent
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return RepositoryContent{}, invalidResponse("GET content", "content response を decode できない", err)
	}
	if value.Type != "file" || value.Encoding != "base64" {
		return RepositoryContent{}, invalidResponse("GET content", "file の base64 response ではない", nil)
	}
	data, err := base64.StdEncoding.DecodeString(value.Content)
	if err != nil {
		return RepositoryContent{}, invalidResponse("GET content", "content base64 が不正", err)
	}
	return RepositoryContent{Path: value.Path, SHA: value.SHA, Data: data}, nil
}

func (g *Gateway) getIssue(ctx context.Context, number int64) (ObservedIssue, error) {
	response, err := g.request(ctx, http.MethodGet, g.endpoint(g.issuePath(number), nil), nil, http.StatusOK)
	if err != nil {
		return ObservedIssue{}, err
	}
	var value apiIssue
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return ObservedIssue{}, invalidResponse("GET issue", "Issue response を decode できない", err)
	}
	observed, err := g.convertObservedIssue(value)
	if err != nil {
		return ObservedIssue{}, err
	}
	if observed.Issue.Repository.canonical() != g.repository {
		return ObservedIssue{}, invalidResponse("GET issue", "response の repository が configured repository と一致しない", nil)
	}
	return observed, nil
}

func (g *Gateway) getParentIssue(ctx context.Context, number int64) (*IssueMetadata, error) {
	response, err := g.request(ctx, http.MethodGet, g.endpoint(g.issuePath(number)+"/parent", nil), nil,
		http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	if response.Status == http.StatusNotFound {
		return nil, nil
	}
	var value apiIssue
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return nil, invalidResponse("GET parent issue", "parent Issue response を decode できない", err)
	}
	metadata, err := g.convertIssueMetadata(value)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (g *Gateway) listSubIssues(ctx context.Context, number int64) ([]IssueMetadata, error) {
	seen := make(map[string]struct{})
	var result []IssueMetadata
	err := g.paginate(ctx, g.issuePath(number)+"/sub_issues", nil, func(data []byte) error {
		var page []apiIssue
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			metadata, err := g.convertIssueMetadata(value)
			if err != nil {
				return err
			}
			key := metadata.Repository.String() + "/issues/" + strconv.FormatInt(metadata.Number, 10)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, metadata)
		}
		return nil
	})
	return result, err
}

func (g *Gateway) getBranch(ctx context.Context, branchName string) (*Branch, error) {
	requestPath := g.repositoryPath("branches") + "/" + url.PathEscape(branchName)
	response, err := g.request(ctx, http.MethodGet, g.endpoint(requestPath, nil), nil, http.StatusOK)
	if err != nil {
		var failure *TransportFailure
		if !errors.As(err, &failure) || failure.Class != FailureNotFound {
			return nil, err
		}
		absent, confirmErr := g.confirmBranchAbsent(ctx, branchName)
		if confirmErr != nil {
			return nil, confirmErr
		}
		if absent {
			return nil, nil
		}
		response, err = g.request(ctx, http.MethodGet, g.endpoint(requestPath, nil), nil, http.StatusOK)
		if err != nil {
			return nil, err
		}
	}
	var value apiBranch
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return nil, invalidResponse("GET branch", "branch response を decode できない", err)
	}
	return &Branch{Name: value.Name, SHA: value.Commit.SHA, Protected: value.Protected}, nil
}

// branch endpoint の 404 は存在しない ref と権限不足を区別できない。
// matching-refs は ref 不在を 200 の配列で返すため、nil を返す前の確認に使う。
func (g *Gateway) confirmBranchAbsent(ctx context.Context, branchName string) (bool, error) {
	requestPath := g.repositoryPath("git/matching-refs/heads") + "/" + url.PathEscape(branchName)
	response, err := g.request(ctx, http.MethodGet, g.endpoint(requestPath, nil), nil, http.StatusOK)
	if err != nil {
		return false, err
	}
	var references []apiGitReference
	if err := json.Unmarshal(response.Body, &references); err != nil {
		return false, invalidResponse("GET matching refs", "matching refs response を decode できない", err)
	}
	if references == nil {
		return false, invalidResponse("GET matching refs", "matching refs response が配列ではない", nil)
	}
	expected := "refs/heads/" + branchName
	for _, reference := range references {
		if reference.Ref == expected {
			return false, nil
		}
	}
	return true, nil
}

func (g *Gateway) listPullRequests(ctx context.Context, branchName string) ([]PullRequest, error) {
	query := url.Values{
		"state": {"all"},
		"head":  {g.repository.Owner + ":" + branchName},
	}
	seen := make(map[int64]struct{})
	var result []PullRequest
	err := g.paginate(ctx, g.repositoryPath("pulls"), query, func(data []byte) error {
		var page []apiPullRequest
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			if value.Number <= 0 {
				return fmt.Errorf("Pull Request number が不正")
			}
			if _, exists := seen[value.Number]; exists {
				continue
			}
			converted, err := convertPullRequest(value)
			if err != nil {
				return err
			}
			seen[value.Number] = struct{}{}
			result = append(result, converted)
		}
		return nil
	})
	return result, err
}

func (g *Gateway) listComments(ctx context.Context, number int64) ([]Comment, error) {
	seen := make(map[int64]struct{})
	var result []Comment
	err := g.paginate(ctx, g.issuePath(number)+"/comments", nil, func(data []byte) error {
		var page []apiComment
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page {
			if _, exists := seen[value.ID]; exists {
				continue
			}
			converted, err := convertComment(value)
			if err != nil {
				return err
			}
			seen[value.ID] = struct{}{}
			result = append(result, converted)
		}
		return nil
	})
	return result, err
}

func (g *Gateway) listCheckRuns(ctx context.Context, headSHA, name string) ([]CheckRun, error) {
	if headSHA == "" {
		return nil, nil
	}
	query := url.Values{"filter": {"all"}}
	if name != "" {
		query.Set("check_name", name)
	}
	seen := make(map[int64]struct{})
	var result []CheckRun
	requestPath := g.repositoryPath("commits") + "/" + url.PathEscape(headSHA) + "/check-runs"
	err := g.paginate(ctx, requestPath, query, func(data []byte) error {
		var page struct {
			CheckRuns []apiCheckRun `json:"check_runs"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		for _, value := range page.CheckRuns {
			if _, exists := seen[value.ID]; exists {
				continue
			}
			seen[value.ID] = struct{}{}
			result = append(result, convertCheckRun(value))
		}
		return nil
	})
	return result, err
}

func (g *Gateway) convertObservedIssue(value apiIssue) (ObservedIssue, error) {
	metadata, err := g.convertIssueMetadata(value)
	if err != nil {
		return ObservedIssue{}, err
	}
	body, err := decodeRawBody(value.Body)
	if err != nil {
		return ObservedIssue{}, invalidResponse("decode Issue", "raw body field が不正または欠落している", err)
	}
	return ObservedIssue{Issue: metadata, RawBody: body}, nil
}

func (g *Gateway) convertIssueMetadata(value apiIssue) (IssueMetadata, error) {
	if value.Number <= 0 {
		return IssueMetadata{}, invalidResponse("decode Issue", "Issue number が不正", nil)
	}
	repository := g.repository
	if value.RepositoryURL != "" {
		parsed, err := repositoryFromAPIURL(value.RepositoryURL)
		if err != nil {
			return IssueMetadata{}, invalidResponse("decode Issue", "repository_url が不正", err)
		}
		repository = parsed
	}
	assignees := make([]Actor, len(value.Assignees))
	for index, assignee := range value.Assignees {
		assignees[index] = Actor{ID: assignee.ID, Login: assignee.Login}
	}
	labels := make([]Label, len(value.Labels))
	for index, label := range value.Labels {
		labels[index] = Label{Name: label.Name, Color: label.Color}
	}
	return IssueMetadata{
		ID:            value.ID,
		NodeID:        value.NodeID,
		Repository:    repository,
		Number:        value.Number,
		State:         value.State,
		StateReason:   value.StateReason,
		Title:         value.Title,
		IsPullRequest: hasPullRequest(value.PullRequest),
		Author:        Actor{ID: value.User.ID, Login: value.User.Login},
		Assignees:     assignees,
		Labels:        labels,
		CreatedAt:     value.CreatedAt,
		UpdatedAt:     value.UpdatedAt,
		ClosedAt:      value.ClosedAt,
	}, nil
}

func hasPullRequest(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null"
}

func convertComment(value apiComment) (Comment, error) {
	body, err := decodeRawBody(value.Body)
	if err != nil {
		return Comment{}, fmt.Errorf("comment body field が不正または欠落している: %w", err)
	}
	return Comment{
		ID:        value.ID,
		NodeID:    value.NodeID,
		Body:      body,
		Author:    Actor{ID: value.User.ID, Login: value.User.Login},
		CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt,
	}, nil
}

func convertPullRequest(value apiPullRequest) (PullRequest, error) {
	body, err := decodeRawBody(value.Body)
	if err != nil {
		return PullRequest{}, fmt.Errorf("Pull Request body field が不正または欠落している: %w", err)
	}
	return PullRequest{
		ID:        value.ID,
		NodeID:    value.NodeID,
		Number:    value.Number,
		State:     value.State,
		Draft:     value.Draft,
		Title:     value.Title,
		Body:      body,
		Author:    Actor{ID: value.User.ID, Login: value.User.Login},
		Head:      PullRequestRef{Name: value.Head.Ref, SHA: value.Head.SHA},
		Base:      PullRequestRef{Name: value.Base.Ref, SHA: value.Base.SHA},
		MergedAt:  value.MergedAt,
		CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt,
	}, nil
}

func convertCheckRun(value apiCheckRun) CheckRun {
	return CheckRun{
		ID:          value.ID,
		NodeID:      value.NodeID,
		Name:        value.Name,
		HeadSHA:     value.HeadSHA,
		Status:      value.Status,
		Conclusion:  value.Conclusion,
		App:         AppIdentity{ID: value.App.ID, Slug: value.App.Slug, Name: value.App.Name},
		Output:      CheckRunOutput{Title: value.Output.Title, Summary: value.Output.Summary, Text: value.Output.Text},
		StartedAt:   value.StartedAt,
		CompletedAt: value.CompletedAt,
	}
}

func decodeRawBody(value json.RawMessage) ([]byte, error) {
	if len(value) == 0 {
		return nil, fmt.Errorf("body field がない")
	}
	if string(value) == "null" {
		return nil, nil
	}
	var body string
	if err := json.Unmarshal(value, &body); err != nil {
		return nil, err
	}
	result := make([]byte, len(body))
	copy(result, body)
	return result, nil
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func repositoryFromAPIURL(value string) (Repository, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return Repository{}, err
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := range segments {
		if segments[index] == "repos" && index+2 < len(segments) {
			owner, ownerErr := url.PathUnescape(segments[index+1])
			name, nameErr := url.PathUnescape(segments[index+2])
			if ownerErr != nil || nameErr != nil {
				return Repository{}, fmt.Errorf("repository path escape が不正")
			}
			repository := Repository{Owner: owner, Name: name}.canonical()
			if err := validateRepository(repository); err != nil {
				return Repository{}, err
			}
			return repository, nil
		}
	}
	return Repository{}, fmt.Errorf("repository path が見つからない")
}

func escapeRepositoryPath(value string) (string, error) {
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") {
		return "", fmt.Errorf("repository-relative path が不正: %q", value)
	}
	parts := strings.Split(value, "/")
	escaped := make([]string, len(parts))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("repository-relative path が不正: %q", value)
		}
		escaped[index] = url.PathEscape(part)
	}
	return strings.Join(escaped, "/"), nil
}

func (g *Gateway) repositoryPath(resource string) string {
	return "/repos/" + url.PathEscape(g.repository.Owner) + "/" + url.PathEscape(g.repository.Name) + "/" + resource
}

func (g *Gateway) issuePath(number int64) string {
	return g.repositoryPath("issues") + "/" + strconv.FormatInt(number, 10)
}
