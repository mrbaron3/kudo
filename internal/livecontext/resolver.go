// Package livecontext は live GitHub と固定済みbaseからversioned Issue inputを再構築する。
// durable stateを持たず、canonical payloadとauthority bytesはAttempt内だけで保持する。
package livecontext

import (
	"context"
	"errors"
	"fmt"

	"github.com/mrbaron3/kudo/internal/contract"
)

var (
	ErrContractInvalid   = errors.New("Issue Contract が不正")
	ErrNotReady          = errors.New("Issue readiness が ready でない")
	ErrWaitingDependency = errors.New("dependency completion 待ち")
	ErrAuthorityMissing  = errors.New("authority が見つからない")
	ErrBaseMissing       = errors.New("claim base が見つからない")
	// ErrSourceNotFound は Source adapter が 404 と transport failure を区別して返すための sentinel である。
	ErrSourceNotFound = errors.New("live source が見つからない")
)

// Source は Context Resolver が必要とする read capability だけを所有する。
// repository content は必ず呼び出し側が pin した ref から取得する。
type Source interface {
	ReadIssue(context.Context, contract.IssueRef) ([]byte, error)
	ReadRepositoryContent(context.Context, contract.IssueRef, string, string) ([]byte, error)
}

type Authority struct {
	Ref     contract.AuthorityRef
	Digest  contract.Digest
	Content []byte
}

type Resolution struct {
	Compiled        *contract.CompiledIssue
	Manifest        contract.ContextManifest
	ManifestRef     contract.ContextManifestRef
	ManifestPayload contract.ArtifactPayload
	Authorities     []Authority
}

// Reconstruction はlive再生成したidentityとclaim時の期待値の比較結果を束ねる。
// Observationの差分はaudit情報であり、Task ContextとContext Manifestが一致する限り
// semantic inputをstaleにしない。
type Reconstruction struct {
	Resolution         *Resolution
	ObservationMatches bool
	TaskContextMatches bool
	ManifestMatches    bool
}

func (r Reconstruction) SameSemanticInput() bool {
	return r.TaskContextMatches && r.ManifestMatches
}

// CompileError は strict parser の構造化 error を失わず application 境界へ返す。
type CompileError struct {
	Validation []contract.ValidationError
}

func (e *CompileError) Error() string { return fmt.Sprintf("%s: %v", ErrContractInvalid, e.Validation) }
func (e *CompileError) Unwrap() error { return ErrContractInvalid }

type Resolver struct {
	source Source
}

func NewResolver(source Source) *Resolver { return &Resolver{source: source} }

// Resolve は compiler が宣言した ClaimRequirements だけに従って authority closure を解決する。
// S1 では dependency completion を推測せず、1件でも宣言されていれば waiting として返す。
func (r *Resolver) Resolve(ctx context.Context, compiled *contract.CompiledIssue, baseSHA string) (*Resolution, error) {
	if r == nil || r.source == nil {
		return nil, errors.New("live context Source は必須")
	}
	if compiled == nil {
		return nil, errors.New("CompiledIssue は必須")
	}
	requirements := compiled.ClaimRequirements
	if requirements.Readiness != contract.ReadinessReady {
		return nil, fmt.Errorf("%w: %s", ErrNotReady, requirements.Readiness)
	}
	if len(requirements.DependsOn) > 0 {
		return nil, fmt.Errorf("%w: %d件", ErrWaitingDependency, len(requirements.DependsOn))
	}

	authorities := make([]Authority, 0, len(requirements.AuthorityRefs))
	manifestAuthorities := make([]contract.AuthorityContent, 0, len(requirements.AuthorityRefs))
	for _, ref := range requirements.AuthorityRefs {
		content, err := r.resolveAuthority(ctx, compiled.TaskContext.Issue, ref, baseSHA)
		if err != nil {
			if errors.Is(err, ErrSourceNotFound) {
				return nil, fmt.Errorf("%w: %s", ErrAuthorityMissing, ref.String())
			}
			return nil, err
		}
		digest := contract.SHA256(content)
		authorities = append(authorities, Authority{Ref: ref, Digest: digest, Content: append([]byte(nil), content...)})
		manifestAuthorities = append(manifestAuthorities, contract.AuthorityContent{Ref: ref, ContentDigest: digest})
	}

	manifest := contract.ContextManifest{
		Schema:        contract.ContextManifestSchemaV1Alpha1,
		TaskContext:   compiled.TaskContextRef,
		BaseSHA:       baseSHA,
		Parent:        cloneIssueRef(requirements.Parent),
		Dependencies:  []contract.DependencyCompletion{},
		AuthorityRefs: manifestAuthorities,
	}
	manifestRef, payload, err := contract.EncodeContextManifest(requirements, manifest)
	if err != nil {
		return nil, err
	}
	return &Resolution{
		Compiled:        compiled,
		Manifest:        manifest,
		ManifestRef:     manifestRef,
		ManifestPayload: payload,
		Authorities:     authorities,
	}, nil
}

func (r *Resolver) resolveAuthority(ctx context.Context, task contract.IssueRef, ref contract.AuthorityRef, baseSHA string) ([]byte, error) {
	if ref.Issue != nil {
		return r.source.ReadIssue(ctx, *ref.Issue)
	}
	return r.source.ReadRepositoryContent(ctx, task, ref.Path, baseSHA)
}

// Reconstruct は pin 済み compiler と base を使い、後続 Operation と同じ手順で live input を再生成する。
func (r *Resolver) Reconstruct(ctx context.Context, compiler contract.Compiler, issue contract.IssueRef, baseSHA string) (*Resolution, error) {
	if r == nil || r.source == nil {
		return nil, errors.New("live context Source は必須")
	}
	body, err := r.source.ReadIssue(ctx, issue)
	if err != nil {
		return nil, err
	}
	compiled, validation := compiler.Compile(string(body), issue)
	if len(validation) > 0 {
		return nil, &CompileError{Validation: append([]contract.ValidationError(nil), validation...)}
	}
	return r.Resolve(ctx, compiled, baseSHA)
}

// ReconstructClaim はcheckpointがpinしたCompilerとbaseを選び、live identityを比較する。
// 未対応Compilerをdeployment既定へfallbackせず、そのままprotocol errorとして返す。
func (r *Resolver) ReconstructClaim(ctx context.Context, issue contract.IssueRef, checkpoint contract.ClaimContext) (Reconstruction, error) {
	if err := contract.ValidateClaimContext(checkpoint); err != nil {
		return Reconstruction{}, err
	}
	compiler, err := contract.CompilerForVersion(checkpoint.Compiler)
	if err != nil {
		return Reconstruction{}, err
	}
	resolved, err := r.Reconstruct(ctx, compiler, issue, checkpoint.BaseSHA)
	if err != nil {
		return Reconstruction{}, err
	}
	return Reconstruction{
		Resolution:         resolved,
		ObservationMatches: resolved.Compiled.ObservationRef == checkpoint.Observation,
		TaskContextMatches: resolved.Compiled.TaskContextRef == checkpoint.TaskContext,
		ManifestMatches:    resolved.ManifestRef == checkpoint.ContextManifest,
	}, nil
}

func cloneIssueRef(value *contract.IssueRef) *contract.IssueRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
