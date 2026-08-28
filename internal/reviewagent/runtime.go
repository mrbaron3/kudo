// Package reviewagent は deterministic な Review Worker と provider session の間を接続する。
// GitHub の観測・再構築・記録は扱わず、検証済み immutable input と package output の binding だけを所有する。
package reviewagent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mrbaron3/kudo/internal/agentpackage"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/livecontext"
)

const (
	TestValidityInputSchemaV1Alpha1  = "kudo.agent-input/test_validity/v1alpha1"
	TestValidityOutputSchemaV1Alpha1 = "kudo.agent-output/test_validity/v1alpha1"
)

var ErrInvalidProviderOutput = errors.New("provider output が不正")

type ArtifactMaterial struct {
	Name      string
	Schema    string
	MediaType string
	Data      []byte
}

// PreparedReview は deterministic prerequisite と live reconstruction を終えた input である。
// constructor を公開しない代わりに BuildTestValidityRequest が closure 全体を再検証するため、
// fake や別 handler が未検証値を直接組み立てても provider 境界を越えない。
type PreparedReview struct {
	Request    contract.ReviewRequest
	Package    agentpackage.Package
	Resolution *livecontext.Resolution
	Manifest   contract.ArtifactManifest
	Artifacts  []ArtifactMaterial
}

type VersionedRef struct {
	Schema string          `json:"schema"`
	Digest contract.Digest `json:"digest"`
}

type Material struct {
	Schema    string          `json:"schema"`
	Digest    contract.Digest `json:"digest"`
	MediaType string          `json:"mediaType"`
	Encoding  string          `json:"encoding"`
	Content   string          `json:"content"`
}

type AuthorityMaterial struct {
	Ref       string          `json:"ref"`
	Digest    contract.Digest `json:"digest"`
	MediaType string          `json:"mediaType"`
	Encoding  string          `json:"encoding"`
	Content   string          `json:"content"`
}

type NamedMaterial struct {
	Name      string          `json:"name"`
	Digest    contract.Digest `json:"digest"`
	MediaType string          `json:"mediaType"`
	Encoding  string          `json:"encoding"`
	Content   string          `json:"content"`
}

// TestValidityRequest は provider が解釈する package 固有 input である。
// Review Request の audit metadata、provider policy、local checkout path は含めない。
type TestValidityRequest struct {
	Schema        string                   `json:"schema"`
	RequestDigest contract.Digest          `json:"requestDigest"`
	AgentPackage  contract.AgentPackageRef `json:"agentPackage"`
	HeadSHA       string                   `json:"headSha"`
	PolicyRefs    []string                 `json:"policyRefs"`
	TaskContext   Material                 `json:"taskContext"`
	Authorities   []AuthorityMaterial      `json:"authorities"`
	Artifacts     []NamedMaterial          `json:"artifacts"`
}

// BuildTestValidityRequest は live 再構築結果と Artifact Manifest の bytes binding を
// provider 起動直前に再検証し、package input schema に一致する canonical JSON を返す。
func BuildTestValidityRequest(prepared PreparedReview) ([]byte, error) {
	if err := validatePreparedReview(prepared); err != nil {
		return nil, err
	}
	requestDigest, err := contract.ReviewRequestDigest(prepared.Request)
	if err != nil {
		return nil, err
	}
	taskPayload := prepared.Resolution.Compiled.TaskContextPayload
	taskEncoding, taskContent := encodeContent(taskPayload.Data)
	request := TestValidityRequest{
		Schema:        TestValidityInputSchemaV1Alpha1,
		RequestDigest: requestDigest,
		AgentPackage:  prepared.Package.Ref,
		HeadSHA:       prepared.Request.HeadSHA,
		PolicyRefs:    append([]string(nil), prepared.Request.PolicyRefs...),
		TaskContext: Material{
			Schema: taskPayload.Schema, Digest: taskPayload.Digest, MediaType: taskPayload.MediaType,
			Encoding: taskEncoding, Content: taskContent,
		},
		Authorities: make([]AuthorityMaterial, len(prepared.Resolution.Authorities)),
		Artifacts:   make([]NamedMaterial, len(prepared.Artifacts)),
	}
	slices.Sort(request.PolicyRefs)
	for i, authority := range prepared.Resolution.Authorities {
		encoding, content := encodeContent(authority.Content)
		mediaType := "text/plain; charset=utf-8"
		if encoding == "base64" {
			mediaType = "application/octet-stream"
		}
		request.Authorities[i] = AuthorityMaterial{
			Ref: authority.Ref.String(), Digest: authority.Digest, MediaType: mediaType,
			Encoding: encoding, Content: content,
		}
	}
	for i, artifact := range prepared.Artifacts {
		encoding, content := encodeContent(artifact.Data)
		request.Artifacts[i] = NamedMaterial{
			Name: artifact.Name, Digest: contract.SHA256(artifact.Data), MediaType: artifact.MediaType,
			Encoding: encoding, Content: content,
		}
	}
	slices.SortFunc(request.Artifacts, func(a, b NamedMaterial) int { return strings.Compare(a.Name, b.Name) })
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("test_validity request encode: %w", err)
	}
	if err := agentpackage.ValidateJSON(prepared.Package.InputSchema, data); err != nil {
		return nil, fmt.Errorf("test_validity input schema: %w", err)
	}
	return append([]byte(nil), data...), nil
}

func validatePreparedReview(prepared PreparedReview) error {
	if err := agentpackage.Validate(prepared.Package); err != nil {
		return fmt.Errorf("Agent Package closure: %w", err)
	}
	if err := contract.ValidateReviewRequest(prepared.Request); err != nil {
		return err
	}
	if prepared.Request.Kind != contract.ReviewTestValidity {
		return fmt.Errorf("Agent Package operation と Review kind が一致しない: %q", prepared.Request.Kind)
	}
	if prepared.Package.Manifest.Name != string(prepared.Request.Kind) ||
		prepared.Package.Manifest.Operation != string(prepared.Request.Kind) ||
		prepared.Package.Ref != prepared.Request.AgentPackage {
		return fmt.Errorf("Review Request が検証済み Agent Package identity と一致しない")
	}
	if prepared.Resolution == nil || prepared.Resolution.Compiled == nil {
		return errors.New("live reconstruction result は必須")
	}
	if prepared.Resolution.ManifestRef != prepared.Request.ContextManifest {
		return errors.New("live Context Manifest が Review Request と一致しない")
	}
	if prepared.Resolution.Manifest.TaskContext != prepared.Resolution.Compiled.TaskContextRef {
		return errors.New("live Task Context が Context Manifest と一致しない")
	}
	if _, err := contract.ReadContextManifestArtifact(prepared.Resolution.ManifestRef, prepared.Resolution.ManifestPayload); err != nil {
		return fmt.Errorf("Context Manifest payload: %w", err)
	}
	if _, err := contract.ReadTaskContextArtifact(prepared.Resolution.Compiled.TaskContextRef, prepared.Resolution.Compiled.TaskContextPayload); err != nil {
		return fmt.Errorf("Task Context payload: %w", err)
	}
	if err := contract.BindReviewRequestManifest(prepared.Request, prepared.Manifest); err != nil {
		return err
	}
	if len(prepared.Resolution.Authorities) != len(prepared.Resolution.Manifest.AuthorityRefs) {
		return errors.New("authority closure の件数が Context Manifest と一致しない")
	}
	for i, authority := range prepared.Resolution.Authorities {
		expected := prepared.Resolution.Manifest.AuthorityRefs[i]
		if authority.Ref.String() != expected.Ref.String() || authority.Digest != expected.ContentDigest ||
			contract.SHA256(authority.Content) != authority.Digest {
			return fmt.Errorf("authority[%d] が Context Manifest と一致しない", i)
		}
	}

	entries := make(map[string]contract.ArtifactEntry, len(prepared.Manifest.Entries))
	for _, entry := range prepared.Manifest.Entries {
		entries[entry.Name] = entry
	}
	seen := make(map[string]bool, len(prepared.Artifacts))
	for i, artifact := range prepared.Artifacts {
		entry, ok := entries[artifact.Name]
		if !ok || seen[artifact.Name] {
			return fmt.Errorf("artifact[%d] %q が manifest に無いか重複している", i, artifact.Name)
		}
		seen[artifact.Name] = true
		if artifact.MediaType != entry.MediaType || int64(len(artifact.Data)) != entry.Length ||
			contract.SHA256(artifact.Data) != entry.Digest {
			return fmt.Errorf("artifact[%d] %q が manifest entry と一致しない", i, artifact.Name)
		}
	}
	if len(seen) != len(entries) {
		return fmt.Errorf("manifest artifact が不足: got %d, want %d", len(seen), len(entries))
	}
	return nil
}

type agentOutput struct {
	Schema   string         `json:"schema"`
	Verdict  string         `json:"verdict"`
	Findings []agentFinding `json:"findings"`
}

type agentFinding struct {
	ID           string            `json:"id"`
	Severity     string            `json:"severity"`
	Summary      string            `json:"summary"`
	Expected     string            `json:"expected"`
	Observed     string            `json:"observed"`
	EvidenceRefs []contract.Digest `json:"evidenceRefs"`
}

// BindTestValidityOutput は package schema で provider bytes を strict validation した後、
// runtime 所有 metadata を補って versioned Review Result を構築する。
func BindTestValidityOutput(
	prepared PreparedReview,
	requestBytes, outputBytes []byte,
	reviewRunID string,
	createdAt time.Time,
) (contract.ReviewResult, error) {
	wantRequest, err := BuildTestValidityRequest(prepared)
	if err != nil {
		return contract.ReviewResult{}, err
	}
	if !bytes.Equal(requestBytes, wantRequest) {
		return contract.ReviewResult{}, fmt.Errorf("%w: immutable request bytes が構築時と一致しない", ErrInvalidProviderOutput)
	}
	if err := agentpackage.ValidateJSON(prepared.Package.OutputSchema, outputBytes); err != nil {
		return contract.ReviewResult{}, fmt.Errorf("%w: output schema: %v", ErrInvalidProviderOutput, err)
	}
	var output agentOutput
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		return contract.ReviewResult{}, fmt.Errorf("%w: decode: %v", ErrInvalidProviderOutput, err)
	}
	if output.Schema != TestValidityOutputSchemaV1Alpha1 {
		return contract.ReviewResult{}, fmt.Errorf("%w: schema %q", ErrInvalidProviderOutput, output.Schema)
	}
	requestDigest, err := contract.ReviewRequestDigest(prepared.Request)
	if err != nil {
		return contract.ReviewResult{}, err
	}
	allowedEvidence := evidenceSet(prepared)
	findings := make([]contract.ReviewFinding, len(output.Findings))
	for i, finding := range output.Findings {
		for _, ref := range finding.EvidenceRefs {
			if !allowedEvidence[ref] {
				return contract.ReviewResult{}, fmt.Errorf("%w: findings[%d] の evidence %s は request に無い",
					ErrInvalidProviderOutput, i, ref)
			}
		}
		findings[i] = contract.ReviewFinding{
			ID: finding.ID, Severity: contract.FindingSeverity(finding.Severity), Summary: finding.Summary,
			Expected: finding.Expected, Observed: finding.Observed,
			EvidenceRefs: append([]contract.Digest(nil), finding.EvidenceRefs...),
		}
	}
	result := contract.ReviewResult{
		Schema: contract.ReviewResultSchemaV1Alpha1, RequestDigest: requestDigest,
		ReviewRunID: reviewRunID, Verdict: contract.ReviewVerdict(output.Verdict), Findings: findings,
		CreatedAt: createdAt,
	}
	if err := contract.BindReviewResult(prepared.Request, result); err != nil {
		return contract.ReviewResult{}, fmt.Errorf("%w: Review Result binding: %v", ErrInvalidProviderOutput, err)
	}
	return result, nil
}

func evidenceSet(prepared PreparedReview) map[contract.Digest]bool {
	allowed := map[contract.Digest]bool{
		prepared.Package.Manifest.Instructions.Digest:      true,
		prepared.Resolution.Compiled.TaskContextRef.Digest: true,
	}
	for _, authority := range prepared.Resolution.Authorities {
		allowed[authority.Digest] = true
	}
	for _, entry := range prepared.Manifest.Entries {
		allowed[entry.Digest] = true
	}
	return allowed
}

func encodeContent(data []byte) (string, string) {
	if utf8.Valid(data) {
		return "utf-8", string(data)
	}
	return "base64", base64.StdEncoding.EncodeToString(data)
}
