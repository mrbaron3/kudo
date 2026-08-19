package contract

import "strings"

// 正本は docs/spec/05_design/contracts/review-protocol-v1alpha1.md である。

// PullRequestObservationSchemaV1Alpha1 は publish 済み Pull Request の
// exact observation の schema である。
const PullRequestObservationSchemaV1Alpha1 = "kudo.pull-request-observation/v1alpha1"

// PullRequestState は観測時点の PR の状態である。GitHub の open / closed / merged に対応する。
type PullRequestState string

const (
	PullRequestOpen   PullRequestState = "open"
	PullRequestClosed PullRequestState = "closed"
	PullRequestMerged PullRequestState = "merged"
)

var pullRequestStates = map[PullRequestState]bool{
	PullRequestOpen:   true,
	PullRequestClosed: true,
	PullRequestMerged: true,
}

// PullRequestObservationRef は versioned な PR observation への参照である。
type PullRequestObservationRef struct {
	Schema string
	Digest Digest
}

// PullRequestObservation は live Pull Request の exact observation である。
//
// Issue Observation と同じ audit lineage であり、Review Request identity へ寄与しない。
// review の live freshness 照合は、この観測ではなく live PR の再取得と request の
// headSha / PR ref の一致で行う。保存済み観測だけを現在の PR として扱ってはならない。
//
// 観測時刻は canonical content に含めない。同じ状態の再観測が別 identity になると、
// 意味のない差分で lineage が伸びる。時刻は Artifact Store の metadata が持つ。
type PullRequestObservation struct {
	Schema      string
	PullRequest PullRequestRef
	State       PullRequestState
	Draft       bool
	HeadSHA     string
	BaseRef     string
	BodyDigest  Digest
}

func validatePullRequestObservation(obs PullRequestObservation) error {
	if obs.Schema != PullRequestObservationSchemaV1Alpha1 {
		return protocolSchemaErr("schema", obs.Schema,
			"pull request observation schema は %q でなければならない: %q",
			PullRequestObservationSchemaV1Alpha1, obs.Schema)
	}
	if !validPullRequestRef(obs.PullRequest) {
		return protocolErr(ProtocolFieldInvalid, "pullRequest", "Pull Request reference が不正")
	}
	if !pullRequestStates[obs.State] {
		return protocolErr(ProtocolFieldInvalid, "state", "pull request state が不正: %q", obs.State)
	}
	// GitHub は draft のまま merge できない。矛盾した観測を保存できると、finalize gate は
	// 「ready 済みか」を観測から判定できなくなる。
	if obs.Draft && obs.State == PullRequestMerged {
		return protocolErr(ProtocolFieldInvalid, "draft", "merged された PR は draft でありえない")
	}
	if !validGitSHA(obs.HeadSHA) {
		return protocolErr(ProtocolFieldInvalid, "headSha", "commit SHA が不正: %q", obs.HeadSHA)
	}
	if !validCanonicalLine(obs.BaseRef, MaxCanonicalLineBytes) {
		return protocolErr(canonicalLineCode(obs.BaseRef, MaxCanonicalLineBytes), "baseRef",
			"空、canonical な単一行でない、または上限 %d byte を超えている", MaxCanonicalLineBytes)
	}
	if obs.BodyDigest == "" {
		return protocolErr(ProtocolFieldMissing, "bodyDigest", "digest が空")
	}
	if !obs.BodyDigest.Valid() {
		return protocolErr(ProtocolFieldInvalid, "bodyDigest", "digest が不正: %q", obs.BodyDigest)
	}
	return nil
}

func encodePullRequestObservation(obs PullRequestObservation) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", obs.Schema)
	writeYAMLString(&b, 0, "pullRequest", obs.PullRequest.String())
	writeYAMLString(&b, 0, "state", string(obs.State))
	writeYAMLBool(&b, 0, "draft", obs.Draft)
	writeYAMLString(&b, 0, "headSha", obs.HeadSHA)
	writeYAMLString(&b, 0, "baseRef", obs.BaseRef)
	writeYAMLString(&b, 0, "bodyDigest", string(obs.BodyDigest))
	return []byte(b.String())
}

// EncodePullRequestObservation は observation を canonical YAML へ encode し、
// versioned ref と write-once payload を返す。同じ観測内容は常に同じ digest を持つ。
func EncodePullRequestObservation(obs PullRequestObservation) (PullRequestObservationRef, ArtifactPayload, error) {
	if err := validatePullRequestObservation(obs); err != nil {
		return PullRequestObservationRef{}, ArtifactPayload{}, err
	}
	payload := newArtifactPayload(
		ArtifactKindPullRequestObservation,
		PullRequestObservationSchemaV1Alpha1,
		MediaTypeYAML,
		encodePullRequestObservation(obs),
	)
	return PullRequestObservationRef{Schema: PullRequestObservationSchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

// ReadPullRequestObservationArtifact は ref と payload の binding を検証して bytes を返す。
func ReadPullRequestObservationArtifact(ref PullRequestObservationRef, payload ArtifactPayload) ([]byte, error) {
	return readVersionedArtifact(ArtifactKindPullRequestObservation, ref.Schema, ref.Digest, payload)
}
