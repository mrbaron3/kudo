package reviewerfixture

import (
	"context"
	"errors"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
)

// LiveTarget は開発専用 seeder port を actor-scoped GitHub gateway へ接続する。
type LiveTarget struct {
	gateway *githubadapter.Gateway
}

func NewLiveTarget(gateway *githubadapter.Gateway) (*LiveTarget, error) {
	if gateway == nil {
		return nil, errors.New("live fixture GitHub gateway は必須")
	}
	return &LiveTarget{gateway: gateway}, nil
}

func (t *LiveTarget) EnsureFixtureHead(ctx context.Context, issue contract.IssueRef, file TestFile) (FixtureHead, error) {
	head, err := t.gateway.EnsureDevelopmentFixtureHead(ctx, issue, githubadapter.DevelopmentFixtureFile{
		Path: file.Path, Data: file.Data,
	})
	if err != nil {
		return FixtureHead{}, err
	}
	return FixtureHead{
		Base: head.Base, BranchName: head.BranchName, BootstrapSHA: head.BootstrapSHA,
		HeadSHA: head.HeadSHA, TestFile: TestFile{Path: file.Path, Data: append([]byte(nil), file.Data...)},
	}, nil
}

func (t *LiveTarget) EnsureDraftPullRequest(ctx context.Context, input issueworker.DraftPullRequestInput) (issueworker.ClaimPullRequest, error) {
	return t.gateway.EnsureDraftPullRequest(ctx, input)
}

func (t *LiveTarget) EnsureComment(ctx context.Context, targetNumber int64,
	record githubadapter.CommentRecord) (githubadapter.Comment, bool, error) {
	return t.gateway.EnsureComment(ctx, targetNumber, record)
}

func (t *LiveTarget) EnsureUnmarkedComment(ctx context.Context, targetNumber int64,
	body string) (githubadapter.Comment, bool, error) {
	return t.gateway.EnsureUnmarkedComment(ctx, targetNumber, body)
}

func (t *LiveTarget) EnsureCheckRun(ctx context.Context,
	record githubadapter.CheckRunRecord) (githubadapter.CheckRun, bool, error) {
	return t.gateway.EnsureCheckRun(ctx, record)
}

var _ Target = (*LiveTarget)(nil)
