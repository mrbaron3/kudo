// Package github は GitHub REST API と application の間の単一 gateway を提供する。
// GitHub 固有の response DTO と record surface の表現を package 内へ閉じ込め、
// actor ごとに注入された credential 以外の権限を保持しない。
package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL    = "https://api.github.com"
	DefaultAPIVersion = "2026-03-10"
)

// TokenSource は一回の API request に使う actor credential を返す。
// 実装は refresh を内部で行ってよいが、空 token を成功として返してはならない。
type TokenSource interface {
	Token(context.Context) (string, error)
}

// Repository は一つの configured repository を表す。GitHub 上の identity は
// case-insensitive であり、record surface へ書くときは小文字へ canonicalize する。
type Repository struct {
	Owner string
	Name  string
}

func (r Repository) String() string {
	return fmt.Sprintf("github://%s/%s", strings.ToLower(r.Owner), strings.ToLower(r.Name))
}

func (r Repository) canonical() Repository {
	return Repository{Owner: strings.ToLower(r.Owner), Name: strings.ToLower(r.Name)}
}

// Config は actor-scoped gateway instance の接続先を固定する。
type Config struct {
	BaseURL          string
	APIVersion       string
	Repository       Repository
	RecorderIdentity *RecorderIdentity
}

// Gateway は一つの TokenSource と repository に束縛された GitHub capability instance である。
// package-level client や credential は持たないため、actor 間で identity を共有しない。
type Gateway struct {
	client     *http.Client
	tokens     TokenSource
	baseURL    string
	apiVersion string
	repository Repository
	recorder   *RecorderIdentity

	// mu は rate limit 観測だけを守る。gateway は同時に複数の request を実行できる
	// ため、最後の観測は共有 mutable state になる。credential と接続先は
	// constructor で固定され変わらないので、この lock の対象ではない。
	mu                sync.Mutex
	rateLimit         RateLimitSnapshot
	rateLimitObserved bool
}

// Actor は GitHub user または App user の観測 metadata である。
type Actor struct {
	ID    int64
	Login string
}

// AppIdentity は check run を作成した GitHub App の検証用 identity である。
type AppIdentity struct {
	ID   int64
	Slug string
	Name string
}

// RecorderIdentity は一つの actor-scoped TokenSource が GitHub 上で持つ
// comment author と check run App の immutable numeric identity を固定する。
// GitHub App の bot user ID と App ID は別 namespace なので両方を明示する。
type RecorderIdentity struct {
	CommentAuthor Actor
	CheckRunApp   AppIdentity
}

type Label struct {
	Name  string
	Color string
}

// IssueMetadata は raw Markdown body を含まない Issue の観測 metadata である。
type IssueMetadata struct {
	ID            int64
	NodeID        string
	Repository    Repository
	Number        int64
	State         string
	StateReason   string
	Title         string
	IsPullRequest bool
	Author        Actor
	Assignees     []Actor
	Labels        []Label
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedAt      *time.Time
}

// ObservedIssue は exact raw body と GitHub metadata を別 field で運ぶ。
// RawBody は trim、改行変換、Markdown 解釈を行っていない defensive copy である。
type ObservedIssue struct {
	Issue   IssueMetadata
	RawBody []byte
}

type Comment struct {
	ID        int64
	NodeID    string
	Body      []byte
	Author    Actor
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Branch struct {
	Name      string
	SHA       string
	Protected bool
}

type PullRequestRef struct {
	Name string
	SHA  string
}

type CheckRunOutput struct {
	Title   string
	Summary string
	Text    string
}

type CheckRun struct {
	ID          int64
	NodeID      string
	Name        string
	HeadSHA     string
	Status      string
	Conclusion  string
	App         AppIdentity
	Output      CheckRunOutput
	StartedAt   *time.Time
	CompletedAt *time.Time
}

type PullRequest struct {
	ID        int64
	NodeID    string
	Number    int64
	State     string
	Draft     bool
	Title     string
	Body      []byte
	Author    Actor
	Head      PullRequestRef
	Base      PullRequestRef
	MergedAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	Comments  []Comment
	CheckRuns []CheckRun
}

// IssueSnapshot は phase 導出に必要な Issue 周辺の read model である。
// GitHub API response type は含まず、workflow の判断も行わない。
type IssueSnapshot struct {
	Issue         IssueMetadata
	RawBody       []byte
	Parent        *IssueMetadata
	SubIssues     []IssueMetadata
	IssueComments []Comment
	Branch        *Branch
	PullRequests  []PullRequest
}

type CandidateFilter struct {
	Assignee string
	Label    string
}

type RepositoryContent struct {
	Path string
	SHA  string
	Data []byte
}

type CommentRecord struct {
	Marker       Marker
	Body         string
	MachineBlock *MachineBlock
}

type CheckRunRecord struct {
	Marker       Marker
	Name         string
	HeadSHA      string
	Conclusion   string
	DetailsURL   string
	Title        string
	Summary      string
	MachineBlock *MachineBlock
}
