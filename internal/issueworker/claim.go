// Package issueworker は branch、worktree、Pull Request を変更できる Issue Worker の
// Operation を実装する。status labelやController名義のrecordはこの権限境界で変更しない。
package issueworker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/livecontext"
	"github.com/mrbaron3/kudo/internal/workflow"
)

type Status string

const (
	StatusClaimed              Status = "claimed"
	StatusWaitingDependency    Status = "waiting_dependency"
	StatusWaitingCapacity      Status = "waiting_capacity"
	StatusSkippedNotCandidate  Status = "skipped_not_candidate"
	StatusSkippedAlreadyMerged Status = "skipped_already_merged"
	StatusClaimRejected        Status = "claim_rejected"
	StatusFailedTransport      Status = "failed_transport"
)

// routing 対象の既定値は workflow の宣言を唯一の正本にする。値を書き写すと、
// polling と claim が別の assignee / label を候補条件に使う状態を作れてしまう。
const (
	DefaultTargetAssignee = workflow.DefaultTargetAssignee
	DefaultReadyLabel     = workflow.LabelReady
)

// ErrClaimConflict は観測済みのclaim residueを正規の同一Runへ結び付けられないことを表す。
// transport failureではないため、adapterはこのsentinelを保持して返す。
var ErrClaimConflict = errors.New("claim conflict")

type RejectionCode string

const (
	RejectionOperationInvalid   RejectionCode = "operation_invalid"
	RejectionObservationInvalid RejectionCode = "observation_invalid"
	RejectionContractInvalid    RejectionCode = "contract_invalid"
	RejectionNotReady           RejectionCode = "readiness_not_ready"
	RejectionAuthorityInvalid   RejectionCode = "authority_invalid"
	RejectionIssueChanged       RejectionCode = "issue_changed_during_claim"
	RejectionClaimConflict      RejectionCode = "claim_conflict"
)

type Rejection struct {
	Code       RejectionCode
	Message    string
	Validation []contract.ValidationError
}

type Result struct {
	Status          Status
	OperationResult *contract.OperationResult
	PullRequest     contract.PullRequestRef
	Rejection       *Rejection
	Err             error
}

type ClaimIssue struct {
	Ref           contract.IssueRef
	State         string
	Title         string
	IsPullRequest bool
	Assignees     []string
	Labels        []string
	RawBody       []byte
}

type ClaimBranch struct {
	Name string
	SHA  string
}

type ClaimPullRequest struct {
	Number     int
	State      string
	Draft      bool
	HeadSHA    string
	BaseSHA    string
	Merged     bool
	Checkpoint *contract.ClaimCheckpoint
}

type ClaimObservation struct {
	Issue        ClaimIssue
	Branch       *ClaimBranch
	PullRequests []ClaimPullRequest
}

type ClaimBase struct {
	Name string
	SHA  string
}

type DraftPullRequestInput struct {
	Issue      contract.IssueRef
	Title      string
	BranchName string
	HeadSHA    string
	Base       ClaimBase
	Checkpoint contract.ClaimCheckpoint
}

// GitHub は claim が必要とする consumer-owned capability である。
// 実装は actor 単位に構築し、Reviewer capability をこの境界へ注入してはならない。
type GitHub interface {
	livecontext.Source
	ObserveClaimIssue(context.Context, contract.IssueRef) (ClaimObservation, error)
	ResolveClaimBase(context.Context, contract.IssueRef, *ClaimBranch) (ClaimBase, error)
	CreateClaimBranch(context.Context, contract.IssueRef, string, string) (bool, error)
	EnsureBootstrapCommit(context.Context, contract.IssueRef, string, string) (string, error)
	EnsureDraftPullRequest(context.Context, DraftPullRequestInput) (ClaimPullRequest, error)
}

type Compiler interface {
	Version() string
	Compile(string, contract.IssueRef) (*contract.CompiledIssue, []contract.ValidationError)
}

type Clock interface {
	Now() time.Time
}

type ClaimConfig struct {
	TargetAssignee   string
	ReadyLabel       string
	EscalationPolicy contract.EscalationPolicyRef
}

type ClaimHandler struct {
	github   GitHub
	compiler Compiler
	config   ClaimConfig
	clock    Clock
	resolver *livecontext.Resolver
}

func NewClaimHandler(github GitHub, compiler Compiler, config ClaimConfig, clock Clock) (*ClaimHandler, error) {
	if github == nil || compiler == nil || clock == nil {
		return nil, errors.New("GitHub、Compiler、Clock は必須")
	}
	if config.TargetAssignee == "" {
		config.TargetAssignee = DefaultTargetAssignee
	}
	if config.ReadyLabel == "" {
		config.ReadyLabel = DefaultReadyLabel
	}
	fields := []struct{ name, value string }{
		{"target assignee", config.TargetAssignee},
		{"ready label", config.ReadyLabel},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("%s は必須", field.name)
		}
	}
	if !config.EscalationPolicy.Valid() {
		return nil, errors.New("Escalation Policy ref が不正")
	}
	return &ClaimHandler{
		github: github, compiler: compiler, config: config, clock: clock,
		resolver: livecontext.NewResolver(github),
	}, nil
}

func (h *ClaimHandler) Handle(ctx context.Context, operation contract.WorkerOperation, attemptID string) Result {
	if err := contract.ValidateWorkerOperation(operation); err != nil || operation.Kind != contract.OperationClaim {
		if err == nil {
			err = fmt.Errorf("Operation kind は %q でなければならない", contract.OperationClaim)
		}
		return rejected(RejectionOperationInvalid, err.Error(), nil)
	}
	if err := contract.ValidateOperationAttemptID(attemptID); err != nil {
		return rejected(RejectionOperationInvalid, err.Error(), nil)
	}
	observation, err := h.github.ObserveClaimIssue(ctx, operation.Issue)
	if err != nil {
		return failedTransport(err)
	}
	if observation.Issue.Ref.String() != operation.Issue.String() {
		return rejected(RejectionObservationInvalid, "live Issue identity が Operation と一致しない", nil)
	}
	if !strings.EqualFold(observation.Issue.State, "open") || observation.Issue.IsPullRequest {
		return Result{Status: StatusSkippedNotCandidate}
	}
	for _, pull := range observation.PullRequests {
		if pull.Merged {
			return Result{Status: StatusSkippedAlreadyMerged}
		}
	}

	existingPull, existingCheckpoint, found, conflict := h.completedCheckpoint(observation)
	// Controllerのstatus投影でai-readyが外れた後は、完全なcheckpointだけを
	// 同じRunへの回復根拠にできる。branchや未完成PRだけではcandidate gateを迂回しない。
	if !h.candidate(observation.Issue) && !found {
		return Result{Status: StatusSkippedNotCandidate}
	}
	if found || conflict != nil {
		if conflict != nil {
			return rejected(RejectionClaimConflict, conflict.Error(), nil)
		}
		return h.completeExisting(ctx, operation, attemptID, existingPull, existingCheckpoint)
	}

	compiled, validation := h.compiler.Compile(string(observation.Issue.RawBody), operation.Issue)
	if len(validation) > 0 {
		return rejected(RejectionContractInvalid, "Issue Contract を compile できない", validation)
	}
	if compiled == nil || compiled.CompilerVersion != h.compiler.Version() {
		return rejected(RejectionContractInvalid, "Issue Compiler が version binding を満たす結果を返さない", nil)
	}
	if compiled.ClaimRequirements.Readiness != contract.ReadinessReady {
		return rejected(RejectionNotReady,
			fmt.Sprintf("readiness は ready でなければならない: %s", compiled.ClaimRequirements.Readiness), nil)
	}
	if len(compiled.ClaimRequirements.DependsOn) > 0 {
		return Result{Status: StatusWaitingDependency}
	}

	base, err := h.github.ResolveClaimBase(ctx, operation.Issue, claimResidueBranch(observation))
	if err != nil {
		switch {
		case errors.Is(err, ErrClaimConflict):
			return rejected(RejectionClaimConflict, err.Error(), nil)
		case errors.Is(err, livecontext.ErrBaseMissing):
			return rejected(RejectionAuthorityInvalid, err.Error(), nil)
		default:
			return failedTransport(err)
		}
	}
	resolution, err := h.resolver.Resolve(ctx, compiled, base.SHA)
	if err != nil {
		switch {
		case errors.Is(err, livecontext.ErrWaitingDependency):
			return Result{Status: StatusWaitingDependency}
		case errors.Is(err, livecontext.ErrNotReady):
			return rejected(RejectionNotReady, err.Error(), nil)
		case errors.Is(err, livecontext.ErrAuthorityMissing):
			return rejected(RejectionAuthorityInvalid, err.Error(), nil)
		default:
			return failedTransport(err)
		}
	}

	latestBody, err := h.github.ReadIssue(ctx, operation.Issue)
	if err != nil {
		return failedTransport(err)
	}
	if contract.SHA256(latestBody) != compiled.Observation.BodyDigest {
		return rejected(RejectionIssueChanged, "branch mutation 直前に Issue body が変わった", nil)
	}

	branchName := "kudo/issue-" + strconv.Itoa(operation.Issue.Number)
	if observation.Branch == nil {
		if _, err := h.github.CreateClaimBranch(ctx, operation.Issue, branchName, base.SHA); err != nil {
			return failedTransport(err)
		}
	}
	headSHA, err := h.github.EnsureBootstrapCommit(ctx, operation.Issue, branchName, base.SHA)
	if err != nil {
		if errors.Is(err, ErrClaimConflict) {
			return rejected(RejectionClaimConflict, err.Error(), nil)
		}
		return failedTransport(err)
	}
	claimContext := contract.ClaimContext{
		Compiler:        compiled.CompilerVersion,
		Observation:     compiled.ObservationRef,
		BodyDigest:      compiled.Observation.BodyDigest,
		TaskContext:     compiled.TaskContextRef,
		ContextManifest: resolution.ManifestRef,
		BaseSHA:         base.SHA,
	}
	checkpoint := contract.ClaimCheckpoint{
		Schema:           contract.ClaimCheckpointSchemaV1Alpha1,
		Context:          claimContext,
		ExecutionPolicy:  operation.ExecutionPolicy,
		EscalationPolicy: h.config.EscalationPolicy,
	}
	if _, _, err := contract.EncodeClaimCheckpoint(checkpoint); err != nil {
		return rejected(RejectionClaimConflict, err.Error(), nil)
	}
	pull, err := h.github.EnsureDraftPullRequest(ctx, DraftPullRequestInput{
		Issue: operation.Issue, Title: observation.Issue.Title, BranchName: branchName,
		HeadSHA: headSHA, Base: base, Checkpoint: checkpoint,
	})
	if err != nil {
		return failedTransport(err)
	}
	if pull.Number <= 0 || !pull.Draft || pull.State != "open" {
		return rejected(RejectionClaimConflict, "draft Pull Request の観測が claim 条件を満たさない", nil)
	}
	return h.finish(operation, attemptID, pull, claimContext)
}

func (h *ClaimHandler) candidate(issue ClaimIssue) bool {
	return containsFold(issue.Assignees, h.config.TargetAssignee) && containsFold(issue.Labels, h.config.ReadyLabel)
}

func claimResidueBranch(observation ClaimObservation) *ClaimBranch {
	if observation.Branch != nil {
		branch := *observation.Branch
		return &branch
	}
	for _, pull := range observation.PullRequests {
		if strings.EqualFold(pull.State, "open") && pull.HeadSHA != "" {
			return &ClaimBranch{Name: "kudo/issue-" + strconv.Itoa(observation.Issue.Ref.Number), SHA: pull.HeadSHA}
		}
	}
	return nil
}

func (h *ClaimHandler) completedCheckpoint(observation ClaimObservation) (ClaimPullRequest, contract.ClaimCheckpoint, bool, error) {
	var open []ClaimPullRequest
	for _, pull := range observation.PullRequests {
		if strings.EqualFold(pull.State, "open") {
			open = append(open, pull)
		}
	}
	if len(open) > 1 {
		return ClaimPullRequest{}, contract.ClaimCheckpoint{}, false, errors.New("同じ claim branch に open Pull Request が複数ある")
	}
	if len(open) == 0 || open[0].Checkpoint == nil {
		return ClaimPullRequest{}, contract.ClaimCheckpoint{}, false, nil
	}
	checkpoint := *open[0].Checkpoint
	if _, _, err := contract.EncodeClaimCheckpoint(checkpoint); err != nil {
		return ClaimPullRequest{}, contract.ClaimCheckpoint{}, false, fmt.Errorf("既存 claim checkpoint が不正: %w", err)
	}
	return open[0], checkpoint, true, nil
}

func (h *ClaimHandler) completeExisting(ctx context.Context, operation contract.WorkerOperation, attemptID string, pull ClaimPullRequest, checkpoint contract.ClaimCheckpoint) Result {
	if checkpoint.ExecutionPolicy != operation.ExecutionPolicy || checkpoint.EscalationPolicy != h.config.EscalationPolicy {
		return rejected(RejectionClaimConflict, "既存 claim checkpoint の policy ref が現在の claim と一致しない", nil)
	}
	if pull.Number <= 0 || !pull.Draft || !strings.EqualFold(pull.State, "open") || pull.BaseSHA != checkpoint.Context.BaseSHA {
		return rejected(RejectionClaimConflict, "既存 Pull Request が claim checkpoint の draft/base identity と一致しない", nil)
	}
	if pull.HeadSHA == "" || pull.HeadSHA == checkpoint.Context.BaseSHA {
		return rejected(RejectionClaimConflict, "既存 Pull Request が検証可能なbootstrap headを持たない", nil)
	}
	base, err := h.github.ResolveClaimBase(ctx, operation.Issue, &ClaimBranch{
		Name: "kudo/issue-" + strconv.Itoa(operation.Issue.Number),
		SHA:  pull.HeadSHA,
	})
	if err != nil {
		if errors.Is(err, ErrClaimConflict) {
			return rejected(RejectionClaimConflict, err.Error(), nil)
		}
		return failedTransport(err)
	}
	if base.SHA != checkpoint.Context.BaseSHA {
		return rejected(RejectionClaimConflict, "既存 Pull Request のbootstrap parentがclaim checkpointのbaseと一致しない", nil)
	}
	reconstructed, err := h.resolver.ReconstructClaim(ctx, operation.Issue, checkpoint.Context)
	if err != nil {
		var compileErr *livecontext.CompileError
		switch {
		case errors.As(err, &compileErr):
			return rejected(RejectionContractInvalid, "既存 claim checkpoint の live Issue Contract を再構築できない", compileErr.Validation)
		case errors.Is(err, livecontext.ErrAuthorityMissing):
			return rejected(RejectionAuthorityInvalid, err.Error(), nil)
		case errors.Is(err, livecontext.ErrNotReady), errors.Is(err, livecontext.ErrWaitingDependency):
			return rejected(RejectionClaimConflict, "既存 claim checkpoint の live input がclaim条件を満たさない", nil)
		default:
			if _, ok := contract.ProtocolViolation(err); ok {
				return rejected(RejectionClaimConflict, err.Error(), nil)
			}
			return failedTransport(err)
		}
	}
	if !reconstructed.SameSemanticInput() {
		return rejected(RejectionClaimConflict, "既存 claim checkpoint のTask ContextまたはContext Manifestがlive inputと一致しない", nil)
	}
	return h.finish(operation, attemptID, pull, checkpoint.Context)
}

func (h *ClaimHandler) finish(operation contract.WorkerOperation, attemptID string, pull ClaimPullRequest, claimContext contract.ClaimContext) Result {
	operationDigest, err := contract.OperationDigest(operation)
	if err != nil {
		return rejected(RejectionOperationInvalid, err.Error(), nil)
	}
	pullRef := contract.PullRequestRef{Owner: operation.Issue.Owner, Repository: operation.Issue.Repository, Number: pull.Number}
	operationResult := contract.OperationResult{
		Schema:          contract.WorkerResultSchemaV1Alpha1,
		OperationDigest: operationDigest,
		AttemptID:       attemptID,
		Outcome:         contract.OutcomeSucceeded,
		ClaimContext:    &claimContext,
		ExternalRefs:    []string{pullRef.String()},
		CompletedAt:     h.clock.Now(),
	}
	if err := contract.BindOperationResult(operation, operationResult); err != nil {
		return rejected(RejectionClaimConflict, err.Error(), nil)
	}
	return Result{Status: StatusClaimed, OperationResult: &operationResult, PullRequest: pullRef}
}

func rejected(code RejectionCode, message string, validation []contract.ValidationError) Result {
	return Result{Status: StatusClaimRejected, Rejection: &Rejection{
		Code: code, Message: message, Validation: append([]contract.ValidationError(nil), validation...),
	}}
}

func failedTransport(err error) Result { return Result{Status: StatusFailedTransport, Err: err} }

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
