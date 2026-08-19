package contract

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// 正本は docs/spec/05_design/contracts/review-protocol-v1alpha1.md である。

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
// Observation と PullRequestObservation は audit lineage と live freshness 照合のための
// exact observation であり、request identity には寄与しない。RequestID と ProducerRunID も
// identity ではないため、同じ immutable input への依頼は producer が誰であっても同じ
// digest を持つ。逆に、identity を構成する ref が一つでも違えば、同じ RequestID でも
// 別 request である。
//
// PullRequest は review が繋留される publish 済み draft PR であり identity に含まれる。
// 同じ head でも別 PR への request は別 request である。
type ReviewRequest struct {
	Schema        string
	RequestID     string
	Kind          ReviewKind
	ProducerRunID string
	Issue         IssueRef
	PullRequest   PullRequestRef

	Observation            IssueObservationRef
	PullRequestObservation PullRequestObservationRef
	HeadSHA                string
	ContextManifest        ContextManifestRef
	ExecutionPolicy        ExecutionPolicyRef
	ArtifactManifest       ArtifactManifestRef
	PolicyRefs             []string

	CreatedAt time.Time
}

// ValidateReviewRequest は request 単体の整合性を検証する。
// versioned ref は schema と digest の組としてのみ検証し、参照先の canonical YAML や
// Issue Contract を parse しない。
func ValidateReviewRequest(req ReviewRequest) error {
	if req.Schema != ReviewRequestSchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"review request schema は %q でなければならない: %q", ReviewRequestSchemaV1Alpha1, req.Schema)
	}
	if !reviewKinds[req.Kind] {
		return protocolErr(ProtocolKindUnknown, "kind", "review kind が不正: %q", req.Kind)
	}
	if !validProtocolID(req.RequestID) {
		return protocolErr(ProtocolFieldInvalid, "requestId", "identifier が不正: %q", req.RequestID)
	}
	if !validProtocolID(req.ProducerRunID) {
		return protocolErr(ProtocolFieldInvalid, "producerRunId", "identifier が不正: %q", req.ProducerRunID)
	}
	if !validIssueRef(req.Issue) {
		return protocolErr(ProtocolFieldInvalid, "issue", "Issue reference が不正")
	}
	if !validPullRequestRef(req.PullRequest) {
		return protocolErr(ProtocolFieldInvalid, "pullRequest", "Pull Request reference が不正")
	}
	// review は Task と同じ repository の PR へ繋留される。別 repository の PR を指す
	// request を通すと、Run が所有しない PR に対する approve が gate を通る。
	if !req.PullRequest.sameRepository(req.Issue) {
		return protocolErr(ProtocolFieldInvalid, "pullRequest",
			"Issue と別 repository の PR を参照している: %s", req.PullRequest.String())
	}
	if err := validateVersionedRef("issueObservation", req.Observation.Schema, req.Observation.Digest, issueObservationSchemaPrefix); err != nil {
		return err
	}
	if err := validateVersionedRef("pullRequestObservation", req.PullRequestObservation.Schema, req.PullRequestObservation.Digest, pullRequestObservationSchemaPrefix); err != nil {
		return err
	}
	if !validGitSHA(req.HeadSHA) {
		return protocolErr(ProtocolFieldInvalid, "headSha", "commit SHA が不正: %q", req.HeadSHA)
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
		return protocolErr(ProtocolFieldMissing, "policyRefs", "評価基準となる policy が明示されていない")
	}
	if err := validatePolicyRefs(req.PolicyRefs); err != nil {
		return err
	}
	if err := requireReviewPolicyRefs(req.Kind, req.PolicyRefs); err != nil {
		return err
	}
	if req.CreatedAt.IsZero() {
		return protocolErr(ProtocolFieldMissing, "createdAt", "作成時刻が空")
	}
	return nil
}

// BindReviewRequestManifest は request が指す Artifact Manifest の中身を binding 境界で
// 検証する。ValidateReviewRequest は manifest を schema と digest の組としてしか見ないため、
// 「reviewer が読めない manifest を参照した request」はそこでは止まらない。
//
// manifest を再 encode して request の ref と照合したうえで必須 entry を検証する。name の
// 充足だけを見て ref を照合しないと、必須 entry を揃えた別の manifest を渡して gate を
// 通せてしまい、検証した manifest と reviewer が実際に読む manifest がずれる。
// Context Manifest は request にも明示される semantic input なので、entry の digest とも
// 一致させ、同じ request 内に相反する identity を残さない。
func BindReviewRequestManifest(req ReviewRequest, manifest ArtifactManifest) error {
	if err := ValidateReviewRequest(req); err != nil {
		return err
	}
	ref, _, err := EncodeArtifactManifest(manifest)
	if err != nil {
		return err
	}
	if ref != req.ArtifactManifest {
		return protocolErr(ProtocolIdentityMismatch, "artifactManifest",
			"request が参照していない manifest を検証しようとしている: got schema=%q digest=%s, want schema=%q digest=%s",
			ref.Schema, ref.Digest, req.ArtifactManifest.Schema, req.ArtifactManifest.Digest)
	}
	if err := requireArtifactNames("entries", fmt.Sprintf("review kind %q", req.Kind),
		requiredReviewEntries[req.Kind], artifactEntrySet(manifest.Entries)); err != nil {
		return err
	}
	for i, entry := range manifest.Entries {
		if entry.Name == string(ArtifactNameContextManifest) && entry.Digest != req.ContextManifest.Digest {
			return protocolErr(ProtocolIdentityMismatch, fmt.Sprintf("entries[%d].digest", i),
				"context-manifest entry が request の Context Manifest と一致しない: got %s, want %s",
				entry.Digest, req.ContextManifest.Digest)
		}
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
	writeYAMLString(&b, 0, "pullRequest", req.PullRequest.String())
	writeYAMLString(&b, 0, "headSha", req.HeadSHA)
	writeYAMLRef(&b, 0, "contextManifest", req.ContextManifest.Schema, req.ContextManifest.Digest)
	writeYAMLRef(&b, 0, "executionPolicy", req.ExecutionPolicy.Schema, req.ExecutionPolicy.Digest)
	writeYAMLRef(&b, 0, "artifactManifest", req.ArtifactManifest.Schema, req.ArtifactManifest.Digest)
	writeYAMLStringList(&b, 0, "policyRefs", canonicalStringSet(req.PolicyRefs))
	return []byte(b.String())
}

// requiredReviewPolicyRefs は kind の Request が policyRefs へ含めなければならない
// 標準 policy の現行 version である。policy 文書は標準 policy の省略と別 policy からの
// 推測を禁じており、path の形式検証だけでは任意の別 policy だけを積んだ request が
// provider へ渡ってしまう。語彙は artifact logical name と同じ理由で pure core に置く。
// policy の意味を変えるときは新しい versioned path を追加し、ここも同じ change で差し替える。
var requiredReviewPolicyRefs = map[ReviewKind][]string{
	ReviewTestValidity:        {"docs/spec/05_design/review-policies/test-validity-v1alpha1.md"},
	ReviewFinalImplementation: {"docs/spec/05_design/review-policies/final-implementation-v1alpha1.md"},
}

// RequiredReviewPolicyRefs は kind の Request が含めるべき標準 policy path を返す。
// 未知の kind では nil を返す。
func RequiredReviewPolicyRefs(kind ReviewKind) []string {
	return slices.Clone(requiredReviewPolicyRefs[kind])
}

// requireReviewPolicyRefs は kind の標準 policy が policyRefs に揃っているかを検証する。
// repository 固有 policy の追加は妨げない。
func requireReviewPolicyRefs(kind ReviewKind, refs []string) error {
	present := make(map[string]bool, len(refs))
	for _, ref := range refs {
		present[ref] = true
	}
	missing := make([]string, 0, len(requiredReviewPolicyRefs[kind]))
	for _, required := range requiredReviewPolicyRefs[kind] {
		if !present[required] {
			missing = append(missing, required)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return protocolErr(ProtocolKindConstraint, "policyRefs",
		"review kind %q が要求する policy ref が揃っていない: %s", kind, strings.Join(missing, ", "))
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

// ConditionalPerspective は final implementation review policy の条件付き観点の語彙である。
//
// 適用可否は review session が policy の適用条件から判断する。語彙を pure core に置くのは
// 「全観点への態度表明」という宣言の完全性を binding 境界で機械検証するためであり、
// 適用条件そのものは docs/spec/05_design/review-policies/final-implementation-v1alpha1.md が正本である。
type ConditionalPerspective string

const (
	PerspectiveUX            ConditionalPerspective = "ux"
	PerspectiveAccessibility ConditionalPerspective = "accessibility"
	PerspectiveTypeDesign    ConditionalPerspective = "type-design"
	PerspectivePerformance   ConditionalPerspective = "performance"
)

var conditionalPerspectives = map[ConditionalPerspective]bool{
	PerspectiveUX:            true,
	PerspectiveAccessibility: true,
	PerspectiveTypeDesign:    true,
	PerspectivePerformance:   true,
}

// requiredResultPerspectives は kind の Result が宣言しなければならない条件付き観点である。
// test_validity が空なのは条件付き観点を持たない kind だからであり、空は「検証しない」
// ではなく「宣言を持てない」を意味する。
var requiredResultPerspectives = map[ReviewKind][]ConditionalPerspective{
	ReviewTestValidity: {},
	ReviewFinalImplementation: {
		PerspectiveUX,
		PerspectiveAccessibility,
		PerspectiveTypeDesign,
		PerspectivePerformance,
	},
}

// 宣言漏れの kind は必須集合が nil になり、宣言なしの Result が binding を通る。
// fail-open を検出可能に留めず、宣言矛盾を持つ binary が起動しないようにする。
func init() {
	for kind := range reviewKinds {
		if _, ok := requiredReviewPolicyRefs[kind]; !ok {
			panic("review kind " + string(kind) + " の標準 policy ref が宣言されていない")
		}
		if _, ok := requiredResultPerspectives[kind]; !ok {
			panic("review kind " + string(kind) + " の必須 applicability 宣言が宣言されていない")
		}
	}
}

// PerspectiveDecision は条件付き観点 1 件への applicability 宣言である。
//
// 宣言は reviewer 自身の判断であり、handler や Controller が代筆・補完してはならない。
// 適用しない判断にも理由 code と根拠を必須にして、観点が黙って省略される経路を残さない。
type PerspectiveDecision struct {
	Perspective  ConditionalPerspective
	Applicable   bool
	Reason       string
	EvidenceRefs []Digest
}

// validReasonCode は applicability 宣言の理由 code を検証する。
// 理由は機械判定可能な lowercase kebab-case に限る。自由記述を許すと、Controller と
// audit は宣言を分類できず、記録が飾りになる。補足の自由記述は evidence 側に置く。
func validReasonCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, segment := range strings.Split(value, "-") {
		if segment == "" {
			return false
		}
		for i := range len(segment) {
			c := segment[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
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
// Perspectives は判断の一部であり、finding と同じく identity へ寄与する。
type ReviewResult struct {
	Schema        string
	RequestDigest Digest
	ReviewRunID   string
	Verdict       ReviewVerdict
	Perspectives  []PerspectiveDecision
	Findings      []ReviewFinding
	CreatedAt     time.Time
}

// ValidateReviewResult は Result 単体の整合性と、verdict と finding の対応を検証する。
// blocking finding の有無と verdict が矛盾する Result を通すと、Controller の
// gate 判定が finding を読まずに誤った方向へ進む。
func ValidateReviewResult(result ReviewResult) error {
	if result.Schema != ReviewResultSchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"review result schema は %q でなければならない: %q", ReviewResultSchemaV1Alpha1, result.Schema)
	}
	if !result.RequestDigest.Valid() {
		return protocolErr(ProtocolFieldInvalid, "requestDigest", "digest が不正: %q", result.RequestDigest)
	}
	if !validProtocolID(result.ReviewRunID) {
		return protocolErr(ProtocolFieldInvalid, "reviewRunId", "identifier が不正: %q", result.ReviewRunID)
	}
	if !reviewVerdicts[result.Verdict] {
		return protocolErr(ProtocolFieldInvalid, "verdict", "review verdict が不正: %q", result.Verdict)
	}
	if result.CreatedAt.IsZero() {
		return protocolErr(ProtocolFieldMissing, "createdAt", "作成時刻が空")
	}

	// applicability 宣言は単体の妥当性と一意性だけをここで検証する。kind に対する
	// 完全性は Result 単体では決まらないため、BindReviewResult が request と突き合わせる。
	seenPerspectives := map[ConditionalPerspective]bool{}
	for i, decision := range result.Perspectives {
		field := fmt.Sprintf("perspectives[%d]", i)
		if !conditionalPerspectives[decision.Perspective] {
			return protocolErr(ProtocolFieldInvalid, field+".perspective",
				"条件付き観点の語彙に無い: %q", decision.Perspective)
		}
		if seenPerspectives[decision.Perspective] {
			return protocolErr(ProtocolFieldDuplicate, field+".perspective",
				"applicability 宣言が重複: %s", decision.Perspective)
		}
		seenPerspectives[decision.Perspective] = true
		if !validReasonCode(decision.Reason) {
			return protocolErr(ProtocolFieldInvalid, field+".reason",
				"機械判定可能な reason code でない: %q", decision.Reason)
		}
		if len(decision.EvidenceRefs) == 0 {
			return protocolErr(ProtocolFieldMissing, field+".evidenceRefs", "宣言が根拠を持たない")
		}
		if err := validateDigestSet(field+".evidenceRefs", decision.EvidenceRefs); err != nil {
			return err
		}
	}

	// finding の identity（妥当性と一意性）はここで判定する。一意性は finding 間の関係で
	// あり単体では決まらないため、content の検証だけを validateReviewFinding へ分ける。
	seen := map[string]bool{}
	blocking := 0
	for i, finding := range result.Findings {
		if !validProtocolID(finding.ID) {
			return protocolErr(ProtocolFieldInvalid, findingField(i, "id"), "identifier が不正: %q", finding.ID)
		}
		if seen[finding.ID] {
			return protocolErr(ProtocolFieldDuplicate, findingField(i, "id"), "finding id が重複: %s", finding.ID)
		}
		seen[finding.ID] = true
		if err := validateReviewFinding(i, finding); err != nil {
			return err
		}
		if finding.Severity == SeverityBlocking {
			blocking++
		}
	}

	return validateVerdictMatchesFindings(result.Verdict, blocking)
}

// validateVerdictMatchesFindings は verdict と blocking finding の対応を検証する。
// 矛盾した Result を通すと、Controller は finding を読まずに gate を判断してしまう。
func validateVerdictMatchesFindings(verdict ReviewVerdict, blocking int) error {
	switch verdict {
	case VerdictApprove:
		if blocking > 0 {
			return protocolErr(ProtocolOutcomeConflict, "verdict", "approve は blocking finding を持てない")
		}
	case VerdictRequestChanges, VerdictNeedsHuman:
		if blocking == 0 {
			return protocolErr(ProtocolOutcomeConflict, "verdict", "%q は blocking finding を必要とする", verdict)
		}
	}
	return nil
}

func findingField(index int, name string) string {
	return fmt.Sprintf("findings[%d].%s", index, name)
}

// validateReviewFinding は finding 単体の content を検証する。
// ID の妥当性と一意性は呼び出し側が判定済みであることを前提とする。
func validateReviewFinding(index int, finding ReviewFinding) error {
	if !findingSeverities[finding.Severity] {
		return protocolErr(ProtocolFieldInvalid, findingField(index, "severity"),
			"severity が不正: %q", finding.Severity)
	}
	if !validCanonicalLine(finding.Summary, MaxCanonicalLineBytes) {
		return protocolErr(canonicalLineCode(finding.Summary, MaxCanonicalLineBytes), findingField(index, "summary"),
			"空、canonical な単一行でない、または上限 %d byte を超えている", MaxCanonicalLineBytes)
	}
	for _, body := range []struct{ name, value string }{
		{"expected", finding.Expected},
		{"observed", finding.Observed},
	} {
		if !validCanonicalText(body.value, MaxCanonicalTextBytes) {
			return protocolErr(canonicalTextCode(body.value, MaxCanonicalTextBytes), findingField(index, body.name),
				"空、canonical text でない、または上限 %d byte を超えている", MaxCanonicalTextBytes)
		}
	}
	if len(finding.EvidenceRefs) == 0 {
		return protocolErr(ProtocolFieldMissing, findingField(index, "evidenceRefs"), "finding が根拠を持たない")
	}
	return validateDigestSet(findingField(index, "evidenceRefs"), finding.EvidenceRefs)
}

// ReviewResultDigest は Result の content identity を返す。
// review Run と作成時刻を含めないため、同じ request への同じ判断は同じ digest になる。
func ReviewResultDigest(result ReviewResult) (Digest, error) {
	if err := ValidateReviewResult(result); err != nil {
		return "", err
	}
	return SHA256(encodeReviewResultIdentity(result)), nil
}

// encodeReviewResultIdentity は perspective と finding をそれぞれ perspective 名・
// finding ID の lexicographic 順へ並べ替えて encode する。並びは reviewer が列挙した
// 順序であって判断そのものではなく、model provider は同じ判断でも順序を再現しない。
// ValidateReviewResult が一意性を保証しているため、いずれの並びも決定論的である。
func encodeReviewResultIdentity(result ReviewResult) []byte {
	findings := append([]ReviewFinding(nil), result.Findings...)
	slices.SortFunc(findings, func(a, b ReviewFinding) int { return strings.Compare(a.ID, b.ID) })

	var b strings.Builder
	writeYAMLString(&b, 0, "schema", result.Schema)
	writeYAMLString(&b, 0, "requestDigest", string(result.RequestDigest))
	writeYAMLString(&b, 0, "verdict", string(result.Verdict))
	encodeReviewPerspectives(&b, result.Perspectives)
	if len(findings) == 0 {
		b.WriteString("findings: []\n")
		return []byte(b.String())
	}
	b.WriteString("findings:\n")
	for _, finding := range findings {
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

func encodeReviewPerspectives(b *strings.Builder, decisions []PerspectiveDecision) {
	if len(decisions) == 0 {
		b.WriteString("perspectives: []\n")
		return
	}
	sorted := append([]PerspectiveDecision(nil), decisions...)
	slices.SortFunc(sorted, func(a, b PerspectiveDecision) int {
		return strings.Compare(string(a.Perspective), string(b.Perspective))
	})
	b.WriteString("perspectives:\n")
	for _, decision := range sorted {
		b.WriteString("  - perspective: ")
		b.WriteString(yamlString(string(decision.Perspective)))
		b.WriteByte('\n')
		writeYAMLBool(b, 4, "applicable", decision.Applicable)
		writeYAMLString(b, 4, "reason", decision.Reason)
		writeYAMLStringList(b, 4, "evidenceRefs", canonicalDigestStrings(decision.EvidenceRefs))
	}
}

// requireResultPerspectives は kind が要求する applicability 宣言の完全性を検証する。
// 欠落を 1 件目で打ち切らず全件を error へ載せる（requireArtifactNames と同じ理由）。
func requireResultPerspectives(kind ReviewKind, decisions []PerspectiveDecision) error {
	required := requiredResultPerspectives[kind]
	if len(required) == 0 {
		if len(decisions) > 0 {
			return protocolErr(ProtocolKindConstraint, "perspectives",
				"review kind %q は条件付き観点を持たない", kind)
		}
		return nil
	}
	present := make(map[ConditionalPerspective]bool, len(decisions))
	for _, decision := range decisions {
		present[decision.Perspective] = true
	}
	missing := make([]string, 0, len(required))
	for _, perspective := range required {
		if !present[perspective] {
			missing = append(missing, string(perspective))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return protocolErr(ProtocolKindConstraint, "perspectives",
		"review kind %q が要求する applicability 宣言が揃っていない: %s", kind, strings.Join(missing, ", "))
}

// BindReviewResult は Result が参照する Review Request identity を再計算して照合する。
// Context Manifest ref、Execution Policy ref、head、artifact manifest、policy ref、
// PR ref のいずれかが変われば digest が変わり、以前の verdict は新しい入力へ再利用できない。
//
// applicability 宣言の完全性は kind に依存し Result 単体では決まらないため、ここで
// 検証する。宣言を欠いた Result は品質 verdict ではなく protocol violation として拒否する。
func BindReviewResult(req ReviewRequest, result ReviewResult) error {
	digest, err := ReviewRequestDigest(req)
	if err != nil {
		return err
	}
	if err := ValidateReviewResult(result); err != nil {
		return err
	}
	if result.RequestDigest != digest {
		return protocolErr(ProtocolIdentityMismatch, "requestDigest",
			"result が別の Review Request identity を参照している: got %s, want %s",
			result.RequestDigest, digest)
	}
	return requireResultPerspectives(req.Kind, result.Perspectives)
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
		return protocolErr(ProtocolOutcomeConflict, "", "quality verdict と attempt failure を同時に持てない")
	case o.Result != nil:
		return ValidateReviewResult(*o.Result)
	case o.Failure != nil:
		return o.Failure.Validate()
	default:
		return protocolErr(ProtocolOutcomeConflict, "", "attempt outcome が verdict も failure も持たない")
	}
}
