package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
	"github.com/mrbaron3/kudo/internal/livecontext"
)

var _ issueworker.GitHub = (*Gateway)(nil)

type apiRepository struct {
	DefaultBranch string `json:"default_branch"`
}

type apiGitCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Tree    struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

// ObserveClaimIssue は共通 observer の snapshot を claim handler 所有の read model へ投影する。
// PR body の表現解釈は record surface を所有する adapter 内に閉じ込める。
func (g *Gateway) ObserveClaimIssue(ctx context.Context, issue contract.IssueRef) (issueworker.ClaimObservation, error) {
	if err := g.validateIssueRef(issue); err != nil {
		return issueworker.ClaimObservation{}, err
	}
	snapshot, err := g.ObserveIssue(ctx, int64(issue.Number))
	if err != nil {
		return issueworker.ClaimObservation{}, err
	}
	result := issueworker.ClaimObservation{
		Issue: issueworker.ClaimIssue{
			Ref:           issue,
			State:         snapshot.Issue.State,
			Title:         snapshot.Issue.Title,
			IsPullRequest: snapshot.Issue.IsPullRequest,
			Assignees:     actorLogins(snapshot.Issue.Assignees),
			Labels:        labelNames(snapshot.Issue.Labels),
			RawBody:       cloneBytes(snapshot.RawBody),
		},
	}
	if snapshot.Branch != nil {
		result.Branch = &issueworker.ClaimBranch{Name: snapshot.Branch.Name, SHA: snapshot.Branch.SHA}
	}
	result.PullRequests = make([]issueworker.ClaimPullRequest, 0, len(snapshot.PullRequests))
	for _, pull := range snapshot.PullRequests {
		converted := issueworker.ClaimPullRequest{
			Number:  int(pull.Number),
			State:   pull.State,
			Draft:   pull.Draft,
			HeadSHA: pull.Head.SHA,
			BaseSHA: pull.Base.SHA,
			Merged:  pull.MergedAt != nil,
		}
		if strings.EqualFold(pull.State, "open") {
			checkpoint, parseErr := g.claimCheckpoint(pull, issue)
			if parseErr != nil {
				return issueworker.ClaimObservation{}, parseErr
			}
			converted.Checkpoint = checkpoint
		}
		result.PullRequests = append(result.PullRequests, converted)
	}
	return result, nil
}

func actorLogins(actors []Actor) []string {
	result := make([]string, len(actors))
	for index, actor := range actors {
		result[index] = actor.Login
	}
	return result
}

func labelNames(labels []Label) []string {
	result := make([]string, len(labels))
	for index, label := range labels {
		result[index] = label.Name
	}
	return result
}

func (g *Gateway) claimCheckpoint(pull PullRequest, issue contract.IssueRef) (*contract.ClaimCheckpoint, error) {
	body := string(pull.Body)
	if !strings.Contains(body, markerPrefix) && !strings.Contains(body, machinePrefix) {
		return nil, nil
	}
	surface, err := ParseRecordSurface(body)
	if err != nil {
		return nil, invalidResponse("parse claim checkpoint", "Pull Request body の record surface が不正", err)
	}
	if surface.Marker.Kind != string(contract.ArtifactKindClaimCheckpoint) {
		return nil, nil
	}
	if surface.Marker.Repository.canonical() != g.repository || surface.Marker.Issue != int64(issue.Number) ||
		surface.Marker.Run != strconv.FormatInt(pull.Number, 10) {
		return nil, invalidResponse("parse claim checkpoint", "claim marker identity が Pull Request と一致しない", nil)
	}
	if surface.MachineBlock == nil || surface.MachineBlock.Kind != string(contract.ArtifactKindClaimCheckpoint) ||
		surface.MachineBlock.MediaType != contract.MediaTypeJSON || surface.MachineBlock.Digest != surface.Marker.Digest {
		return nil, invalidResponse("parse claim checkpoint", "claim machine block と marker が一致しない", nil)
	}
	digest := contract.Digest(surface.MachineBlock.Digest)
	ref := contract.ClaimCheckpointRef{Schema: contract.ClaimCheckpointSchemaV1Alpha1, Digest: digest}
	payload := contract.ArtifactPayload{
		Kind: contract.ArtifactKindClaimCheckpoint, Schema: ref.Schema, MediaType: contract.MediaTypeJSON,
		Digest: digest, Data: append([]byte(nil), surface.MachineBlock.Payload...),
	}
	checkpoint, err := contract.ReadClaimCheckpointArtifact(ref, payload)
	if err != nil {
		return nil, invalidResponse("parse claim checkpoint", "claim checkpoint payload が不正", err)
	}
	return &checkpoint, nil
}

func (g *Gateway) ReadIssue(ctx context.Context, issue contract.IssueRef) ([]byte, error) {
	if err := g.validateIssueRef(issue); err != nil {
		return nil, err
	}
	observed, err := g.getIssue(ctx, int64(issue.Number))
	if err != nil {
		return nil, sourceError(err)
	}
	return cloneBytes(observed.RawBody), nil
}

func (g *Gateway) ReadRepositoryContent(ctx context.Context, issue contract.IssueRef, filePath, ref string) ([]byte, error) {
	if err := g.validateIssueRef(issue); err != nil {
		return nil, err
	}
	content, err := g.ReadContent(ctx, filePath, ref)
	if err != nil {
		return nil, sourceError(err)
	}
	return cloneBytes(content.Data), nil
}

func sourceError(err error) error {
	var failure *TransportFailure
	if errors.As(err, &failure) && failure.Class == FailureNotFound {
		return fmt.Errorf("%w: %v", livecontext.ErrSourceNotFound, err)
	}
	return err
}

func (g *Gateway) ResolveClaimBase(ctx context.Context, issue contract.IssueRef, residue *issueworker.ClaimBranch) (issueworker.ClaimBase, error) {
	if err := g.validateIssueRef(issue); err != nil {
		return issueworker.ClaimBase{}, err
	}
	response, err := g.request(ctx, http.MethodGet, g.endpoint(strings.TrimSuffix(g.repositoryPath(""), "/"), nil), nil, http.StatusOK)
	if err != nil {
		return issueworker.ClaimBase{}, err
	}
	var repository apiRepository
	if err := json.Unmarshal(response.Body, &repository); err != nil || repository.DefaultBranch == "" {
		return issueworker.ClaimBase{}, invalidResponse("GET repository", "default branch を取得できない", err)
	}
	branch, err := g.getBranch(ctx, repository.DefaultBranch)
	if err != nil {
		return issueworker.ClaimBase{}, err
	}
	if branch == nil {
		return issueworker.ClaimBase{}, fmt.Errorf("%w: default branch %s", livecontext.ErrBaseMissing, repository.DefaultBranch)
	}
	if !shaPattern.MatchString(branch.SHA) {
		return issueworker.ClaimBase{}, invalidResponse("GET default branch", "base branch の commit SHA が不正", nil)
	}
	base := issueworker.ClaimBase{Name: repository.DefaultBranch, SHA: branch.SHA}
	if residue == nil || residue.SHA == branch.SHA {
		return base, nil
	}
	if residue.Name != IssueBranchName(int64(issue.Number)) || !shaPattern.MatchString(residue.SHA) {
		return issueworker.ClaimBase{}, fmt.Errorf("%w: claim residue branch identityが不正", issueworker.ErrClaimConflict)
	}
	residueBase, err := g.validateClaimBootstrap(ctx, issue, residue.SHA, "")
	if err != nil {
		return issueworker.ClaimBase{}, err
	}
	base.SHA = residueBase
	return base, nil
}

func (g *Gateway) CreateClaimBranch(ctx context.Context, issue contract.IssueRef, branchName, baseSHA string) (bool, error) {
	if err := g.validateClaimMutation(issue, branchName, baseSHA); err != nil {
		return false, err
	}
	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("git/refs"), nil), struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{Ref: "refs/heads/" + branchName, SHA: baseSHA}, http.StatusCreated, http.StatusUnprocessableEntity)
	if err != nil {
		return false, err
	}
	if response.Status == http.StatusUnprocessableEntity {
		branch, readErr := g.getBranch(ctx, branchName)
		if readErr != nil {
			return false, readErr
		}
		if branch == nil {
			return false, invalidResponse("POST git ref", "422 response 後も claim branch が存在しない", nil)
		}
		return false, nil
	}
	var reference apiGitReference
	if err := json.Unmarshal(response.Body, &reference); err != nil {
		return false, invalidResponse("POST git ref", "created ref response を decode できない", err)
	}
	if reference.Ref != "refs/heads/"+branchName || reference.Object.SHA != baseSHA || reference.Object.Type != "commit" {
		return false, invalidResponse("POST git ref", "created ref が request と一致しない", nil)
	}
	return true, nil
}

func (g *Gateway) EnsureBootstrapCommit(ctx context.Context, issue contract.IssueRef, branchName, baseSHA string) (string, error) {
	if err := g.validateClaimMutation(issue, branchName, baseSHA); err != nil {
		return "", err
	}
	branch, err := g.getBranch(ctx, branchName)
	if err != nil {
		return "", err
	}
	if branch == nil {
		return "", invalidResponse("GET claim branch", "bootstrap 対象 branch が存在しない", nil)
	}
	if branch.SHA != baseSHA {
		if !shaPattern.MatchString(branch.SHA) {
			return "", invalidResponse("GET claim branch", "claim branch SHA が不正", nil)
		}
		if _, err := g.validateClaimBootstrap(ctx, issue, branch.SHA, baseSHA); err != nil {
			return "", err
		}
		return branch.SHA, nil
	}

	baseCommit, err := g.getGitCommit(ctx, baseSHA)
	if err != nil {
		return "", err
	}
	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("git/commits"), nil), struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{
		Message: fmt.Sprintf("claim: #%d", issue.Number),
		Tree:    baseCommit.Tree.SHA,
		Parents: []string{baseSHA},
	}, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var created apiGitCommit
	if err := json.Unmarshal(response.Body, &created); err != nil || !shaPattern.MatchString(created.SHA) {
		return "", invalidResponse("POST git commit", "bootstrap commit response が不正", err)
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
		if observed != nil && observed.SHA != baseSHA && shaPattern.MatchString(observed.SHA) {
			if _, validationErr := g.validateClaimBootstrap(ctx, issue, observed.SHA, baseSHA); validationErr != nil {
				return "", validationErr
			}
			return observed.SHA, nil
		}
		return "", &TransportFailure{Class: FailureConflict, Operation: "PATCH git ref", StatusCode: update.Status, Message: responseMessage(update.Body)}
	}
	var reference apiGitReference
	if err := json.Unmarshal(update.Body, &reference); err != nil || reference.Object.SHA != created.SHA {
		return "", invalidResponse("PATCH git ref", "updated ref response が bootstrap commit と一致しない", err)
	}
	return created.SHA, nil
}

// validateClaimBootstrap はno-op bootstrapのmessage、単一parent、treeを検証する。
// branch名やcommit messageだけでは人が作った任意commitをclaim residueとして採用できるため、
// parentと同一treeであることまで確認して初めてbase identityを復元する。
func (g *Gateway) validateClaimBootstrap(ctx context.Context, issue contract.IssueRef, headSHA, expectedBaseSHA string) (string, error) {
	commit, err := g.getGitCommit(ctx, headSHA)
	if err != nil {
		return "", err
	}
	if commit.Message != fmt.Sprintf("claim: #%d", issue.Number) || len(commit.Parents) != 1 ||
		!shaPattern.MatchString(commit.Parents[0].SHA) {
		return "", fmt.Errorf("%w: claim residue commitのmessageまたはparentが不正", issueworker.ErrClaimConflict)
	}
	baseSHA := commit.Parents[0].SHA
	if expectedBaseSHA != "" && baseSHA != expectedBaseSHA {
		return "", fmt.Errorf("%w: claim residue commitのparentが期待baseと一致しない", issueworker.ErrClaimConflict)
	}
	base, err := g.getGitCommit(ctx, baseSHA)
	if err != nil {
		return "", err
	}
	if commit.Tree.SHA != base.Tree.SHA {
		return "", fmt.Errorf("%w: claim residue commitがbaseのtreeを変更している", issueworker.ErrClaimConflict)
	}
	return baseSHA, nil
}

func (g *Gateway) getGitCommit(ctx context.Context, sha string) (apiGitCommit, error) {
	response, err := g.request(ctx, http.MethodGet,
		g.endpoint(g.repositoryPath("git/commits")+"/"+url.PathEscape(sha), nil), nil, http.StatusOK)
	if err != nil {
		return apiGitCommit{}, err
	}
	var commit apiGitCommit
	if err := json.Unmarshal(response.Body, &commit); err != nil || commit.SHA != sha || !shaPattern.MatchString(commit.Tree.SHA) {
		return apiGitCommit{}, invalidResponse("GET git commit", "base commit response が不正", err)
	}
	return commit, nil
}

func (g *Gateway) EnsureDraftPullRequest(ctx context.Context, input issueworker.DraftPullRequestInput) (issueworker.ClaimPullRequest, error) {
	if err := g.validateClaimMutation(input.Issue, input.BranchName, input.Base.SHA); err != nil {
		return issueworker.ClaimPullRequest{}, err
	}
	if input.Base.Name == "" || input.Title == "" || !shaPattern.MatchString(input.HeadSHA) {
		return issueworker.ClaimPullRequest{}, fmt.Errorf("draft Pull Request input が不正")
	}
	checkpointRef, checkpointPayload, err := contract.EncodeClaimCheckpoint(input.Checkpoint)
	if err != nil {
		return issueworker.ClaimPullRequest{}, err
	}
	pulls, err := g.listPullRequests(ctx, input.BranchName)
	if err != nil {
		return issueworker.ClaimPullRequest{}, err
	}
	var open []PullRequest
	for _, pull := range pulls {
		if strings.EqualFold(pull.State, "open") {
			open = append(open, pull)
		}
	}
	if len(open) > 1 {
		return issueworker.ClaimPullRequest{}, invalidResponse("ensure draft Pull Request", "open Pull Request が複数ある", nil)
	}
	var pull PullRequest
	if len(open) == 1 {
		pull = open[0]
		if !pull.Draft || pull.Head.Name != input.BranchName || pull.Head.SHA != input.HeadSHA || pull.Base.SHA != input.Base.SHA {
			return issueworker.ClaimPullRequest{}, invalidResponse("ensure draft Pull Request", "既存 Pull Request が claim identity と一致しない", nil)
		}
	} else {
		pull, err = g.createDraftPullRequest(ctx, input)
		if err != nil {
			return issueworker.ClaimPullRequest{}, err
		}
	}

	body, err := g.renderClaimPullRequestBody(input.Issue, pull.Number, checkpointRef, checkpointPayload)
	if err != nil {
		return issueworker.ClaimPullRequest{}, err
	}
	if string(pull.Body) != body {
		if strings.Contains(string(pull.Body), markerPrefix) || strings.Contains(string(pull.Body), machinePrefix) {
			existing, parseErr := g.claimCheckpoint(pull, input.Issue)
			if parseErr != nil {
				return issueworker.ClaimPullRequest{}, parseErr
			}
			if existing != nil && *existing != input.Checkpoint {
				return issueworker.ClaimPullRequest{}, invalidResponse("ensure draft Pull Request", "既存 checkpoint が別 identity を持つ", nil)
			}
		}
		pull, err = g.updatePullRequestBody(ctx, pull.Number, body)
		if err != nil {
			return issueworker.ClaimPullRequest{}, err
		}
	}
	checkpoint := input.Checkpoint
	return issueworker.ClaimPullRequest{
		Number: int(pull.Number), State: pull.State, Draft: pull.Draft,
		HeadSHA: pull.Head.SHA, BaseSHA: pull.Base.SHA, Merged: pull.MergedAt != nil, Checkpoint: &checkpoint,
	}, nil
}

func (g *Gateway) createDraftPullRequest(ctx context.Context, input issueworker.DraftPullRequestInput) (PullRequest, error) {
	response, err := g.request(ctx, http.MethodPost, g.endpoint(g.repositoryPath("pulls"), nil), struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
		Draft bool   `json:"draft"`
	}{
		Title: input.Title,
		Head:  input.BranchName,
		Base:  input.Base.Name,
		Body:  "Kudo claim checkpoint を記録しています。",
		Draft: true,
	}, http.StatusCreated, http.StatusUnprocessableEntity)
	if err != nil {
		return PullRequest{}, err
	}
	if response.Status == http.StatusUnprocessableEntity {
		pulls, readErr := g.listPullRequests(ctx, input.BranchName)
		if readErr != nil {
			return PullRequest{}, readErr
		}
		for _, pull := range pulls {
			if pull.State == "open" && pull.Draft && pull.Head.Name == input.BranchName && pull.Head.SHA == input.HeadSHA {
				return pull, nil
			}
		}
		return PullRequest{}, &TransportFailure{
			Class: FailureConflict, Operation: "POST pull", StatusCode: response.Status,
			Message: "422 response 後も同じ claim の draft Pull Request が存在しない",
		}
	}
	var value apiPullRequest
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return PullRequest{}, invalidResponse("POST pull", "created Pull Request response を decode できない", err)
	}
	pull, err := convertPullRequest(value)
	if err != nil {
		return PullRequest{}, invalidResponse("POST pull", "created Pull Request response が不正", err)
	}
	if pull.Number <= 0 || pull.State != "open" || !pull.Draft ||
		pull.Head.Name != input.BranchName || pull.Head.SHA != input.HeadSHA ||
		pull.Base.Name != input.Base.Name || pull.Base.SHA != input.Base.SHA {
		return PullRequest{}, invalidResponse("POST pull", "created Pull Request が request と一致しない", nil)
	}
	return pull, nil
}

func (g *Gateway) renderClaimPullRequestBody(issue contract.IssueRef, pullNumber int64, ref contract.ClaimCheckpointRef, payload contract.ArtifactPayload) (string, error) {
	return RenderComment(
		fmt.Sprintf("Kudo が Issue #%d を claim しました。この Pull Request が Run #%d の記録面です。", issue.Number, pullNumber),
		Marker{
			Repository: g.repository, Issue: int64(issue.Number), Run: strconv.FormatInt(pullNumber, 10),
			Kind: string(contract.ArtifactKindClaimCheckpoint), Digest: string(ref.Digest),
		},
		&MachineBlock{
			Kind: string(contract.ArtifactKindClaimCheckpoint), MediaType: contract.MediaTypeJSON,
			Digest: string(ref.Digest), Payload: payload.Data,
		},
	)
}

func (g *Gateway) updatePullRequestBody(ctx context.Context, number int64, body string) (PullRequest, error) {
	response, err := g.request(ctx, http.MethodPatch, g.endpoint(g.repositoryPath("pulls")+"/"+strconv.FormatInt(number, 10), nil), struct {
		Body string `json:"body"`
	}{Body: body}, http.StatusOK)
	if err != nil {
		return PullRequest{}, err
	}
	var value apiPullRequest
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return PullRequest{}, invalidResponse("PATCH pull", "updated Pull Request response を decode できない", err)
	}
	pull, err := convertPullRequest(value)
	if err != nil || string(pull.Body) != body || pull.Number != number {
		return PullRequest{}, invalidResponse("PATCH pull", "updated Pull Request body が request と一致しない", err)
	}
	return pull, nil
}

func (g *Gateway) validateIssueRef(issue contract.IssueRef) error {
	if issue.Number <= 0 || !strings.EqualFold(issue.Owner, g.repository.Owner) || !strings.EqualFold(issue.Repository, g.repository.Name) {
		return fmt.Errorf("Issue reference %s は gateway repository %s と一致しない", issue.String(), g.repository.String())
	}
	return nil
}

func (g *Gateway) validateClaimMutation(issue contract.IssueRef, branchName, sha string) error {
	if err := g.validateIssueRef(issue); err != nil {
		return err
	}
	if branchName != IssueBranchName(int64(issue.Number)) || !shaPattern.MatchString(sha) {
		return fmt.Errorf("claim branch name または SHA が不正")
	}
	return nil
}
