package contract

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 正本は docs/contracts/review-protocol-v1alpha1.md である。

const (
	// ReviewRequestSchemaV1Alpha1 は Review Worker への品質評価依頼の schema である。
	ReviewRequestSchemaV1Alpha1 = "kudo.review-request/v1alpha1"
	// ReviewResultSchemaV1Alpha1 は Review Worker が返す verdict の schema である。
	ReviewResultSchemaV1Alpha1 = "kudo.review-result/v1alpha1"
)

// ReviewKind は gate ごとの review 種別である。
type ReviewKind string

const (
	ReviewTestValidity        ReviewKind = "test_validity"
	ReviewFinalImplementation ReviewKind = "final_implementation"
)

var reviewKinds = map[ReviewKind]bool{
	ReviewTestValidity:        true,
	ReviewFinalImplementation: true,
}

// ReviewRequest は immutable input に対する 1 回の品質評価依頼である。
//
// Observation は audit lineage と live freshness 照合のための exact observation であり、
// request identity には寄与しない。RequestID と ProducerRunID も identity ではないため、
// 同じ immutable input への依頼は producer が誰であっても同じ digest を持つ。
// 逆に、identity を構成する ref が一つでも違えば、同じ RequestID でも別 request である。
type ReviewRequest struct {
	Schema        string
	RequestID     string
	Kind          ReviewKind
	ProducerRunID string
	Issue         IssueRef

	Observation      IssueObservationRef
	HeadSHA          string
	ContextManifest  ContextManifestRef
	ExecutionPolicy  ExecutionPolicyRef
	ArtifactManifest ArtifactManifestRef
	PolicyRefs       []string

	CreatedAt time.Time
}

// ValidateReviewRequest は request 単体の整合性を検証する。
// versioned ref は schema と digest の組としてのみ検証し、参照先の canonical YAML や
// Issue Contract を parse しない。
func ValidateReviewRequest(req ReviewRequest) error {
	if req.Schema != ReviewRequestSchemaV1Alpha1 {
		return fmt.Errorf("review request schema は %q でなければならない: %q", ReviewRequestSchemaV1Alpha1, req.Schema)
	}
	if !reviewKinds[req.Kind] {
		return fmt.Errorf("review kind が不正: %q", req.Kind)
	}
	if !validProtocolID(req.RequestID) {
		return fmt.Errorf("requestId が不正: %q", req.RequestID)
	}
	if !validProtocolID(req.ProducerRunID) {
		return fmt.Errorf("producerRunId が不正: %q", req.ProducerRunID)
	}
	if !validIssueRef(req.Issue) {
		return errors.New("issue が不正")
	}
	if err := validateVersionedRef("issueObservation", req.Observation.Schema, req.Observation.Digest, issueObservationSchemaPrefix); err != nil {
		return err
	}
	if !validGitSHA(req.HeadSHA) {
		return fmt.Errorf("headSha が不正: %q", req.HeadSHA)
	}
	if err := validateVersionedRef("contextManifest", req.ContextManifest.Schema, req.ContextManifest.Digest, contextManifestSchemaPrefix); err != nil {
		return err
	}
	if err := validateVersionedRef("executionPolicy", req.ExecutionPolicy.Schema, req.ExecutionPolicy.Digest, executionPolicySchemaPrefix); err != nil {
		return err
	}
	if err := validateVersionedRef("artifactManifest", req.ArtifactManifest.Schema, req.ArtifactManifest.Digest, artifactManifestSchemaPrefix); err != nil {
		return err
	}
	// reviewer は「明示された policy」だけで判断する。policy を省略した request は、
	// reviewer に評価基準を推測させることになるため受理しない。
	if len(req.PolicyRefs) == 0 {
		return errors.New("policyRefs が空")
	}
	if err := validatePolicyRefs(req.PolicyRefs); err != nil {
		return err
	}
	if req.CreatedAt.IsZero() {
		return errors.New("createdAt が空")
	}
	return nil
}

// ReviewRequestDigest は Review Request の content identity を返す。
// request ID、producer Run、作成時刻、Issue Observation は含まない。
func ReviewRequestDigest(req ReviewRequest) (Digest, error) {
	if err := ValidateReviewRequest(req); err != nil {
		return "", err
	}
	return SHA256(encodeReviewRequestIdentity(req)), nil
}

func encodeReviewRequestIdentity(req ReviewRequest) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", req.Schema)
	writeYAMLString(&b, 0, "kind", string(req.Kind))
	writeYAMLString(&b, 0, "repository", req.Issue.repositoryURL())
	writeYAMLString(&b, 0, "issue", req.Issue.String())
	writeYAMLString(&b, 0, "headSha", req.HeadSHA)
	writeYAMLRef(&b, 0, "contextManifest", req.ContextManifest.Schema, req.ContextManifest.Digest)
	writeYAMLRef(&b, 0, "executionPolicy", req.ExecutionPolicy.Schema, req.ExecutionPolicy.Digest)
	writeYAMLRef(&b, 0, "artifactManifest", req.ArtifactManifest.Schema, req.ArtifactManifest.Digest)
	writeYAMLStringList(&b, 0, "policyRefs", canonicalStringSet(req.PolicyRefs))
	return []byte(b.String())
}

// ReviewVerdict は Review Worker が返す品質判断である。
// timeout や transport failure はここへ変換せず、AttemptFailure として記録する。
type ReviewVerdict string

const (
	VerdictApprove        ReviewVerdict = "approve"
	VerdictRequestChanges ReviewVerdict = "request_changes"
	VerdictNeedsHuman     ReviewVerdict = "needs_human"
)

var reviewVerdicts = map[ReviewVerdict]bool{
	VerdictApprove:        true,
	VerdictRequestChanges: true,
	VerdictNeedsHuman:     true,
}

// FindingSeverity は finding が gate を止めるかどうかを表す。
type FindingSeverity string

const (
	SeverityBlocking FindingSeverity = "blocking"
	SeverityAdvisory FindingSeverity = "advisory"
)

var findingSeverities = map[FindingSeverity]bool{
	SeverityBlocking: true,
	SeverityAdvisory: true,
}

// ReviewFinding は verdict の根拠 1 件である。
// expected / observed / evidence を必須にして、感想を finding として通さない。
type ReviewFinding struct {
	ID           string
	Severity     FindingSeverity
	Summary      string
	Expected     string
	Observed     string
	EvidenceRefs []Digest
}

// ReviewResult は Review Request に対する versioned な verdict である。
// ReviewRunID と CreatedAt は content identity へ含めない。
type ReviewResult struct {
	Schema        string
	RequestDigest Digest
	ReviewRunID   string
	Verdict       ReviewVerdict
	Findings      []ReviewFinding
	CreatedAt     time.Time
}

// ValidateReviewResult は Result 単体の整合性と、verdict と finding の対応を検証する。
// blocking finding の有無と verdict が矛盾する Result を通すと、Controller の
// gate 判定が finding を読まずに誤った方向へ進む。
func ValidateReviewResult(result ReviewResult) error {
	if result.Schema != ReviewResultSchemaV1Alpha1 {
		return fmt.Errorf("review result schema は %q でなければならない: %q", ReviewResultSchemaV1Alpha1, result.Schema)
	}
	if !result.RequestDigest.Valid() {
		return fmt.Errorf("requestDigest が不正: %q", result.RequestDigest)
	}
	if !validProtocolID(result.ReviewRunID) {
		return fmt.Errorf("reviewRunId が不正: %q", result.ReviewRunID)
	}
	if !reviewVerdicts[result.Verdict] {
		return fmt.Errorf("review verdict が不正: %q", result.Verdict)
	}
	if result.CreatedAt.IsZero() {
		return errors.New("createdAt が空")
	}

	seen := map[string]bool{}
	blocking := 0
	for i, finding := range result.Findings {
		if !validProtocolID(finding.ID) {
			return fmt.Errorf("findings[%d].id が不正: %q", i, finding.ID)
		}
		if seen[finding.ID] {
			return fmt.Errorf("findings[%d].id が重複: %s", i, finding.ID)
		}
		seen[finding.ID] = true
		if !findingSeverities[finding.Severity] {
			return fmt.Errorf("findings[%d].severity が不正: %q", i, finding.Severity)
		}
		if finding.Severity == SeverityBlocking {
			blocking++
		}
		if !validCanonicalLine(finding.Summary) {
			return fmt.Errorf("findings[%d].summary が不正", i)
		}
		for _, field := range []struct{ name, value string }{
			{"expected", finding.Expected},
			{"observed", finding.Observed},
		} {
			if !validCanonicalText(field.value) {
				return fmt.Errorf("findings[%d].%s が不正", i, field.name)
			}
		}
		if len(finding.EvidenceRefs) == 0 {
			return fmt.Errorf("findings[%d].evidenceRefs が空", i)
		}
		if err := validateDigestSet(fmt.Sprintf("findings[%d].evidenceRefs", i), finding.EvidenceRefs); err != nil {
			return err
		}
	}

	switch result.Verdict {
	case VerdictApprove:
		if blocking > 0 {
			return errors.New("approve は blocking finding を持てない")
		}
	case VerdictRequestChanges, VerdictNeedsHuman:
		if blocking == 0 {
			return fmt.Errorf("%q は blocking finding を必要とする", result.Verdict)
		}
	}
	return nil
}

// ReviewResultDigest は Result の content identity を返す。
// review Run と作成時刻を含めないため、同じ request への同じ判断は同じ digest になる。
func ReviewResultDigest(result ReviewResult) (Digest, error) {
	if err := ValidateReviewResult(result); err != nil {
		return "", err
	}
	return SHA256(encodeReviewResultIdentity(result)), nil
}

func encodeReviewResultIdentity(result ReviewResult) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", result.Schema)
	writeYAMLString(&b, 0, "requestDigest", string(result.RequestDigest))
	writeYAMLString(&b, 0, "verdict", string(result.Verdict))
	if len(result.Findings) == 0 {
		b.WriteString("findings: []\n")
		return []byte(b.String())
	}
	b.WriteString("findings:\n")
	for _, finding := range result.Findings {
		b.WriteString("  - id: ")
		b.WriteString(yamlString(finding.ID))
		b.WriteByte('\n')
		writeYAMLString(&b, 4, "severity", string(finding.Severity))
		writeYAMLString(&b, 4, "summary", finding.Summary)
		writeYAMLString(&b, 4, "expected", finding.Expected)
		writeYAMLString(&b, 4, "observed", finding.Observed)
		writeYAMLStringList(&b, 4, "evidenceRefs", canonicalDigestStrings(finding.EvidenceRefs))
	}
	return []byte(b.String())
}

// BindReviewResult は Result が参照する Review Request identity を再計算して照合する。
// Context Manifest ref、Execution Policy ref、head、artifact manifest、policy ref の
// いずれかが変われば digest が変わり、以前の verdict は新しい入力へ再利用できない。
func BindReviewResult(req ReviewRequest, result ReviewResult) error {
	digest, err := ReviewRequestDigest(req)
	if err != nil {
		return err
	}
	if err := ValidateReviewResult(result); err != nil {
		return err
	}
	if result.RequestDigest != digest {
		return fmt.Errorf("result が別の Review Request identity を参照している: got %s, want %s",
			result.RequestDigest, digest)
	}
	return nil
}

// ReviewAttemptOutcome は 1 attempt の結末である。品質 verdict と execution failure を
// 同じ field で表現しないため、片方だけを持つ値のみ受理する。
type ReviewAttemptOutcome struct {
	Result  *ReviewResult
	Failure *AttemptFailure
}

// Validate は結末が verdict と failure のどちらか一方だけであることを保証する。
func (o ReviewAttemptOutcome) Validate() error {
	switch {
	case o.Result != nil && o.Failure != nil:
		return errors.New("quality verdict と attempt failure を同時に持てない")
	case o.Result != nil:
		return ValidateReviewResult(*o.Result)
	case o.Failure != nil:
		return o.Failure.Validate()
	default:
		return errors.New("attempt outcome が verdict も failure も持たない")
	}
}
