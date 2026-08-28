package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
	"github.com/mrbaron3/kudo/internal/workflow"
)

// DevelopmentFixtureFile は Reviewer fixture の test-only commit に追加する一ファイルである。
type DevelopmentFixtureFile struct {
	Path string
	Data []byte
}

// DevelopmentFixtureHead は claim bootstrap と、その直後の test-only commit を束ねる。
type DevelopmentFixtureHead struct {
	Base         issueworker.ClaimBase
	BranchName   string
	BootstrapSHA string
	HeadSHA      string
}

// EnsureDevelopmentFixtureHead は branch、no-op bootstrap、test file 一件だけを追加する
// commit を順に収束させる。既存 head は commit lineage、変更 file、content を照合し、
// branch 名だけが一致する任意 commit を fixture として採用しない。
func (g *Gateway) EnsureDevelopmentFixtureHead(ctx context.Context, issue contract.IssueRef,
	file DevelopmentFixtureFile) (DevelopmentFixtureHead, error) {
	if err := g.validateDevelopmentFixture(issue, file); err != nil {
		return DevelopmentFixtureHead{}, err
	}
	base, err := g.ResolveClaimBase(ctx, issue, nil)
	if err != nil {
		return DevelopmentFixtureHead{}, err
	}
	branchName := workflow.IssueBranchName(int64(issue.Number))
	branch, err := g.getBranch(ctx, branchName)
	if err != nil {
		return DevelopmentFixtureHead{}, err
	}
	if branch == nil {
		if _, err := g.CreateClaimBranch(ctx, issue, branchName, base.SHA); err != nil {
			return DevelopmentFixtureHead{}, err
		}
		branch = &Branch{Name: branchName, SHA: base.SHA}
	}

	if branch.SHA == base.SHA {
		bootstrapSHA, err := g.EnsureBootstrapCommit(ctx, issue, branchName, base.SHA)
		if err != nil {
			return DevelopmentFixtureHead{}, err
		}
		headSHA, err := g.ensureDevelopmentFixtureCommit(ctx, issue, branchName, bootstrapSHA, file)
		if err != nil {
			return DevelopmentFixtureHead{}, err
		}
		return DevelopmentFixtureHead{
			Base: base, BranchName: branchName, BootstrapSHA: bootstrapSHA, HeadSHA: headSHA,
		}, nil
	}

	commit, err := g.getGitCommit(ctx, branch.SHA)
	if err != nil {
		return DevelopmentFixtureHead{}, err
	}
	if commit.Message == fmt.Sprintf("claim: #%d", issue.Number) {
		baseSHA, err := g.validateClaimBootstrap(ctx, issue, branch.SHA, "")
		if err != nil {
			return DevelopmentFixtureHead{}, err
		}
		base.SHA = baseSHA
		headSHA, err := g.ensureDevelopmentFixtureCommit(ctx, issue, branchName, branch.SHA, file)
		if err != nil {
			return DevelopmentFixtureHead{}, err
		}
		return DevelopmentFixtureHead{
			Base: base, BranchName: branchName, BootstrapSHA: branch.SHA, HeadSHA: headSHA,
		}, nil
	}

	bootstrapSHA, baseSHA, err := g.validateDevelopmentFixtureCommit(ctx, issue, branch.SHA, file)
	if err != nil {
		return DevelopmentFixtureHead{}, err
	}
	base.SHA = baseSHA
	return DevelopmentFixtureHead{
		Base: base, BranchName: branchName, BootstrapSHA: bootstrapSHA, HeadSHA: branch.SHA,
	}, nil
}

func (g *Gateway) validateDevelopmentFixture(issue contract.IssueRef, file DevelopmentFixtureFile) error {
	if err := g.validateIssueRef(issue); err != nil {
		return err
	}
	if file.Path == "" || path.IsAbs(file.Path) || path.Clean(file.Path) != file.Path ||
		strings.HasPrefix(file.Path, "../") || strings.Contains(file.Path, "\\") ||
		strings.ContainsFunc(file.Path, unicode.IsControl) ||
		strings.HasPrefix(strings.ToLower(file.Path), ".git/") || !strings.HasSuffix(file.Path, "_test.go") ||
		len(file.Data) == 0 || len(file.Data) > MaxRecordSurfaceBytes || !utf8.Valid(file.Data) {
		return errors.New("development fixture test file が不正")
	}
	return nil
}

func (g *Gateway) ensureDevelopmentFixtureCommit(ctx context.Context, issue contract.IssueRef,
	branchName, bootstrapSHA string, file DevelopmentFixtureFile) (string, error) {
	branch, err := g.getBranch(ctx, branchName)
	if err != nil {
		return "", err
	}
	if branch == nil {
		return "", invalidResponse("GET fixture branch", "test commit 対象 branch が存在しない", nil)
	}
	if branch.SHA != bootstrapSHA {
		observedBootstrap, _, validationErr := g.validateDevelopmentFixtureCommit(ctx, issue, branch.SHA, file)
		if validationErr != nil {
			return "", validationErr
		}
		if observedBootstrap != bootstrapSHA {
			return "", fmt.Errorf("%w: fixture test commit の bootstrap が期待値と一致しない", issueworker.ErrClaimConflict)
		}
		return branch.SHA, nil
	}

	bootstrap, err := g.getGitCommit(ctx, bootstrapSHA)
	if err != nil {
		return "", err
	}
	blobResponse, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("git/blobs"), nil), struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{Content: base64.StdEncoding.EncodeToString(file.Data), Encoding: "base64"}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var blob struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(blobResponse.Body, &blob); err != nil || !shaPattern.MatchString(blob.SHA) {
		return "", invalidResponse("POST git blob", "fixture blob response が不正", err)
	}

	treeResponse, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("git/trees"), nil), struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}{
		BaseTree: bootstrap.Tree.SHA,
		Tree: []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		}{{Path: file.Path, Mode: "100644", Type: "blob", SHA: blob.SHA}},
	}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(treeResponse.Body, &tree); err != nil || !shaPattern.MatchString(tree.SHA) {
		return "", invalidResponse("POST git tree", "fixture tree response が不正", err)
	}

	commitResponse, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("git/commits"), nil), struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{
		Message: fmt.Sprintf("fixture: reviewer test-only #%d", issue.Number),
		Tree:    tree.SHA, Parents: []string{bootstrapSHA},
	}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var created apiGitCommit
	if err := json.Unmarshal(commitResponse.Body, &created); err != nil ||
		!shaPattern.MatchString(created.SHA) || created.Tree.SHA != tree.SHA {
		return "", invalidResponse("POST git commit", "fixture test commit response が不正", err)
	}

	update, err := g.request(ctx, http.MethodPatch,
		g.endpoint(g.repositoryPath("git/refs/heads")+"/"+url.PathEscape(branchName), nil),
		struct {
			SHA   string `json:"sha"`
			Force bool   `json:"force"`
		}{SHA: created.SHA, Force: false},
		http.StatusOK, http.StatusConflict, http.StatusUnprocessableEntity)
	if err != nil {
		return "", err
	}
	if update.Status != http.StatusOK {
		observed, readErr := g.getBranch(ctx, branchName)
		if readErr != nil {
			return "", readErr
		}
		if observed != nil {
			observedBootstrap, _, validationErr := g.validateDevelopmentFixtureCommit(ctx, issue, observed.SHA, file)
			if validationErr == nil && observedBootstrap == bootstrapSHA {
				return observed.SHA, nil
			}
		}
		return "", &TransportFailure{
			Class: FailureConflict, Operation: "PATCH git ref", StatusCode: update.Status,
			Message: responseMessage(update.Body),
		}
	}
	var reference apiGitReference
	if err := json.Unmarshal(update.Body, &reference); err != nil || reference.Object.Type != "commit" || reference.Object.SHA != created.SHA {
		return "", invalidResponse("PATCH git ref", "updated ref response が fixture commit と一致しない", err)
	}
	observedBootstrap, _, err := g.validateDevelopmentFixtureCommit(ctx, issue, created.SHA, file)
	if err != nil {
		return "", err
	}
	if observedBootstrap != bootstrapSHA {
		return "", fmt.Errorf("%w: 作成した fixture test commit の bootstrap が期待値と一致しない", issueworker.ErrClaimConflict)
	}
	return created.SHA, nil
}

func (g *Gateway) validateDevelopmentFixtureCommit(ctx context.Context, issue contract.IssueRef,
	headSHA string, file DevelopmentFixtureFile) (string, string, error) {
	commit, err := g.getGitCommit(ctx, headSHA)
	if err != nil {
		return "", "", err
	}
	if commit.Message != fmt.Sprintf("fixture: reviewer test-only #%d", issue.Number) ||
		len(commit.Parents) != 1 || !shaPattern.MatchString(commit.Parents[0].SHA) {
		return "", "", fmt.Errorf("%w: fixture test commit の message または parent が不正", issueworker.ErrClaimConflict)
	}
	bootstrapSHA := commit.Parents[0].SHA
	baseSHA, err := g.validateClaimBootstrap(ctx, issue, bootstrapSHA, "")
	if err != nil {
		return "", "", err
	}
	response, err := g.request(ctx, http.MethodGet,
		g.endpoint(g.repositoryPath("commits")+"/"+url.PathEscape(headSHA), nil), nil, http.StatusOK)
	if err != nil {
		return "", "", err
	}
	var observed struct {
		SHA   string `json:"sha"`
		Files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			SHA      string `json:"sha"`
		} `json:"files"`
	}
	if err := json.Unmarshal(response.Body, &observed); err != nil || observed.SHA != headSHA ||
		len(observed.Files) != 1 || observed.Files[0].Filename != file.Path ||
		observed.Files[0].Status != "added" || !shaPattern.MatchString(observed.Files[0].SHA) {
		return "", "", invalidResponse("GET fixture commit", "fixture commit は test file 一件だけを追加しなければならない", err)
	}
	content, err := g.ReadContent(ctx, file.Path, headSHA)
	if err != nil {
		return "", "", err
	}
	if content.Path != file.Path || content.SHA != observed.Files[0].SHA || !bytes.Equal(content.Data, file.Data) {
		return "", "", fmt.Errorf("%w: fixture test file content が期待 payload と一致しない", issueworker.ErrClaimConflict)
	}
	return bootstrapSHA, baseSHA, nil
}

// EnsureUnmarkedComment は marker 欠落 negative fixture だけのために exact body で収束する。
// 正常 record は EnsureComment を使い、この弱い identity 規則へ流さない。
func (g *Gateway) EnsureUnmarkedComment(ctx context.Context, targetNumber int64, body string) (Comment, bool, error) {
	if targetNumber <= 0 || body == "" || len(body) > MaxRecordSurfaceBytes || !utf8.ValidString(body) {
		return Comment{}, false, errors.New("unmarked fixture comment input が不正")
	}
	if g.recorder == nil {
		return Comment{}, false, ErrRecorderIdentityRequired
	}
	comments, err := g.listComments(ctx, targetNumber)
	if err != nil {
		return Comment{}, false, err
	}
	var matches []Comment
	for _, comment := range comments {
		if comment.Author.ID == g.recorder.CommentAuthor.ID && string(comment.Body) == body {
			matches = append(matches, comment)
		}
	}
	if len(matches) > 1 {
		return Comment{}, false, invalidResponse("search unmarked fixture comments", "同じ body の comment が複数ある", nil)
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}
	created, err := g.createComment(ctx, targetNumber, body)
	if err != nil {
		return Comment{}, false, err
	}
	return created, true, nil
}
