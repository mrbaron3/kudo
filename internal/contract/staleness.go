package contract

import (
	"slices"
)

// SemanticComparison は既存 Operation / Review Result を最新入力へ再利用できるかを表す。
type SemanticComparison string

const (
	// SameSemanticInput は semantic identity が一致し、既存 Operation と approval を
	// そのまま継続できることを表す。exact Issue 観測だけの差分はここに含まれる。
	SameSemanticInput SemanticComparison = "same_semantic_input"
	// ChangedSemanticInput は semantic identity が変わり、既存の結果が stale で
	// あることを表す。新しい入力を既存 Operation へ差し替えてはならない。
	ChangedSemanticInput SemanticComparison = "changed_semantic_input"
)

// semantic input field の名前は comparison の出力であると同時に、`stale_input` Result の
// 記録欄でもある。両者が別の語彙を持つと、Controller は受け取った changedInputFields を
// comparison の結果と突き合わせられない。
const (
	fieldContextManifest  = "contextManifest"
	fieldExecutionPolicy  = "executionPolicy"
	fieldHeadSHA          = "headSha"
	fieldInputArtifacts   = "inputArtifacts"
	fieldPolicyRefs       = "policyRefs"
	fieldArtifactManifest = "artifactManifest"
)

// operationInputFields は Operation の semantic input field 名である。
// Issue Observation は identity ではないため、ここには含まれない。
var operationInputFields = map[string]bool{
	fieldContextManifest: true,
	fieldExecutionPolicy: true,
	fieldHeadSHA:         true,
	fieldInputArtifacts:  true,
	fieldPolicyRefs:      true,
}

// SemanticDifference は再利用判定と、その根拠を決定論的に返す。
//
// ObservationChanged は identity の判定には影響しない。exact body が変わったことを
// audit lineage へ追記するための signal であり、Comparison が SameSemanticInput でも
// true になりうる。
type SemanticDifference struct {
	Comparison         SemanticComparison
	ChangedFields      []string
	ObservationChanged bool
}

// LatestOperationInput は model-bearing Operation 開始直前に取り直した入力である。
// Observation は最新 compile 結果のもの、ContextManifest は同じ compile 結果から
// 解決し直した manifest の ref を渡す。
type LatestOperationInput struct {
	Observation     IssueObservationRef
	ContextManifest ContextManifestRef
	ExecutionPolicy ExecutionPolicyRef
	HeadSHA         string
	InputArtifacts  []Digest
	PolicyRefs      []string
}

// LatestReviewInput は review 開始直前に取り直した入力である。
type LatestReviewInput struct {
	Observation      IssueObservationRef
	ContextManifest  ContextManifestRef
	ExecutionPolicy  ExecutionPolicyRef
	HeadSHA          string
	ArtifactManifest ArtifactManifestRef
	PolicyRefs       []string
}

// CompareOperationInput は既存 Operation と最新入力の semantic identity を比較する。
//
// 判定は ref を opaque な (schema, digest) の組として行い、Task Context YAML や
// Issue Contract を parse しない。Task Context が変われば Context Manifest ref が
// 変わるため、closure 全体の変化は manifest ref の比較だけで検出できる。
//
// 比較は純粋関数であり、最新入力を Operation へ書き戻さない。書き戻すと、古い
// approval が新しい入力の approval として黙って再利用されてしまう。
// claim はまだ semantic input を持たないため、比較対象にできない。
func CompareOperationInput(op WorkerOperation, latest LatestOperationInput) (SemanticDifference, error) {
	if err := ValidateWorkerOperation(op); err != nil {
		return SemanticDifference{}, err
	}
	if !operationKindRules[op.Kind].resolvedContext {
		return SemanticDifference{}, protocolErr(ProtocolKindConstraint, "kind",
			"kind %q はまだ semantic input を持たない", op.Kind)
	}
	if err := validateLatestInput(latest.Observation, latest.ContextManifest, latest.ExecutionPolicy, latest.HeadSHA, latest.PolicyRefs); err != nil {
		return SemanticDifference{}, err
	}
	if err := validateDigestSet("inputArtifacts", latest.InputArtifacts); err != nil {
		return SemanticDifference{}, err
	}

	var changed []string
	if *op.ContextManifest != latest.ContextManifest {
		changed = append(changed, fieldContextManifest)
	}
	if op.ExecutionPolicy != latest.ExecutionPolicy {
		changed = append(changed, fieldExecutionPolicy)
	}
	if op.HeadSHA != latest.HeadSHA {
		changed = append(changed, fieldHeadSHA)
	}
	if !slices.Equal(canonicalDigestStrings(op.InputArtifacts), canonicalDigestStrings(latest.InputArtifacts)) {
		changed = append(changed, fieldInputArtifacts)
	}
	if !slices.Equal(canonicalStringSet(op.PolicyRefs), canonicalStringSet(latest.PolicyRefs)) {
		changed = append(changed, fieldPolicyRefs)
	}
	return newSemanticDifference(changed, *op.Observation != latest.Observation), nil
}

// CompareReviewInput は既存 Review Request と最新入力の semantic identity を比較する。
// 判定規則は CompareOperationInput と同じで、比較対象が artifact manifest である点だけが違う。
func CompareReviewInput(req ReviewRequest, latest LatestReviewInput) (SemanticDifference, error) {
	if err := ValidateReviewRequest(req); err != nil {
		return SemanticDifference{}, err
	}
	if err := validateLatestInput(latest.Observation, latest.ContextManifest, latest.ExecutionPolicy, latest.HeadSHA, latest.PolicyRefs); err != nil {
		return SemanticDifference{}, err
	}
	if err := validateVersionedRef("artifactManifest", latest.ArtifactManifest.Schema, latest.ArtifactManifest.Digest, artifactManifestSchemaPrefix); err != nil {
		return SemanticDifference{}, err
	}

	var changed []string
	if req.ContextManifest != latest.ContextManifest {
		changed = append(changed, fieldContextManifest)
	}
	if req.ExecutionPolicy != latest.ExecutionPolicy {
		changed = append(changed, fieldExecutionPolicy)
	}
	if req.HeadSHA != latest.HeadSHA {
		changed = append(changed, fieldHeadSHA)
	}
	if req.ArtifactManifest != latest.ArtifactManifest {
		changed = append(changed, fieldArtifactManifest)
	}
	if !slices.Equal(canonicalStringSet(req.PolicyRefs), canonicalStringSet(latest.PolicyRefs)) {
		changed = append(changed, fieldPolicyRefs)
	}
	return newSemanticDifference(changed, req.Observation != latest.Observation), nil
}

// validateLatestInput は最新入力自体の妥当性を検証する。判定できない入力を
// 「変化なし」へ倒すと、壊れた再取得結果のまま実行を続けてしまう。
func validateLatestInput(observation IssueObservationRef, manifest ContextManifestRef, policy ExecutionPolicyRef, headSHA string, policyRefs []string) error {
	if err := validateVersionedRef("issueObservation", observation.Schema, observation.Digest, issueObservationSchemaPrefix); err != nil {
		return err
	}
	if err := validateVersionedRef("contextManifest", manifest.Schema, manifest.Digest, contextManifestSchemaPrefix); err != nil {
		return err
	}
	if err := validateVersionedRef("executionPolicy", policy.Schema, policy.Digest, executionPolicySchemaPrefix); err != nil {
		return err
	}
	if !validGitSHA(headSHA) {
		return protocolErr(ProtocolFieldInvalid, "headSha", "commit SHA が不正: %q", headSHA)
	}
	return validatePolicyRefs(policyRefs)
}

func newSemanticDifference(changed []string, observationChanged bool) SemanticDifference {
	comparison := SameSemanticInput
	if len(changed) > 0 {
		comparison = ChangedSemanticInput
	}
	return SemanticDifference{
		Comparison:         comparison,
		ChangedFields:      changed,
		ObservationChanged: observationChanged,
	}
}

// ObservationLineage は一つの Operation / Review Request が観測した Issue Observation を
// 発生順に保持する audit record である。content identity へは寄与しないため、
// digest 計算へ渡さない。値として扱い、Append は元の lineage を変更しない。
type ObservationLineage struct {
	entries []IssueObservationRef
}

// NewObservationLineage は最初の観測から lineage を作る。
func NewObservationLineage(initial IssueObservationRef) (ObservationLineage, error) {
	if err := validateVersionedRef("issueObservation", initial.Schema, initial.Digest, issueObservationSchemaPrefix); err != nil {
		return ObservationLineage{}, err
	}
	return ObservationLineage{entries: []IssueObservationRef{initial}}, nil
}

// Append は新しい観測を追記した lineage を返す。直近と同じ観測は履歴を増やさない。
func (l ObservationLineage) Append(ref IssueObservationRef) (ObservationLineage, error) {
	if err := validateVersionedRef("issueObservation", ref.Schema, ref.Digest, issueObservationSchemaPrefix); err != nil {
		return ObservationLineage{}, err
	}
	if len(l.entries) == 0 {
		return ObservationLineage{}, protocolErr(ProtocolFieldMissing, "issueObservation",
			"初期観測を持たない lineage へ追記できない")
	}
	if l.entries[len(l.entries)-1] == ref {
		return l, nil
	}
	entries := make([]IssueObservationRef, len(l.entries), len(l.entries)+1)
	copy(entries, l.entries)
	return ObservationLineage{entries: append(entries, ref)}, nil
}

// Entries は観測履歴の複製を返す。
func (l ObservationLineage) Entries() []IssueObservationRef {
	return append([]IssueObservationRef(nil), l.entries...)
}

// Latest は直近の観測を返す。lineage が空の場合 ok は false になる。
func (l ObservationLineage) Latest() (IssueObservationRef, bool) {
	if len(l.entries) == 0 {
		return IssueObservationRef{}, false
	}
	return l.entries[len(l.entries)-1], true
}

// Len は観測件数を返す。
func (l ObservationLineage) Len() int { return len(l.entries) }
