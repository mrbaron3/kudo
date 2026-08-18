package contract

import (
	"fmt"
	"strings"
	"time"
)

// 正本は docs/contracts/operation-protocol-v1alpha1.md である。

const (
	// WorkerOperationSchemaV1Alpha1 は Controller が Issue Worker へ渡す
	// run-once Operation envelope の schema である。
	WorkerOperationSchemaV1Alpha1 = "kudo.worker-operation/v1alpha1"
	// WorkerResultSchemaV1Alpha1 は Operation の terminal Result の schema である。
	WorkerResultSchemaV1Alpha1 = "kudo.worker-result/v1alpha1"
)

// OperationKind は Issue Worker が実行する run-once 作業の種類である。
type OperationKind string

const (
	OperationClaim                OperationKind = "claim"
	OperationAuthorTests          OperationKind = "author_tests"
	OperationReviseTests          OperationKind = "revise_tests"
	OperationImplement            OperationKind = "implement"
	OperationRepairImplementation OperationKind = "repair_implementation"
	// OperationPublishHead は固定済み head を branch へ compare-and-push し、draft PR を
	// 冪等に ensure/update する。RED 固定後と GREEN/refactor 後の両方で使い、全 review
	// round を PR head へ繋留する。draft の publish は review approve を gate にしない。
	OperationPublishHead OperationKind = "publish_head"
	// OperationFinalizePullRequest は final approve に bind された head に対して required
	// PR body を確定し、draft を解除する。ready 化だけが final approve を gate とする。
	OperationFinalizePullRequest OperationKind = "finalize_pull_request"
)

// operationKindRule は kind ごとの field 要件を固定する。
type operationKindRule struct {
	// resolvedContext は Issue Observation、Context Manifest、head SHA を必須にする。
	// claim はこれらを作る Operation なので開始時点では持てず、Result も head を返さない。
	resolvedContext bool
	// priorArtifacts は Required input が先行 artifact を名指しする kind で true。
	priorArtifacts bool
	// preservesHead は承認済み head を進めず観測するだけの kind で true。
	preservesHead bool
	// pullRequestRef は成功が作成した Pull Request の canonical reference を
	// 残さなければならない kind で true。非空判定だけでは PR identity を復元できず、
	// retry 時の既存 PR 照合も durable handoff も成立しない。
	pullRequestRef bool
}

// operationKindRules は kind ごとの field 要件を固定する。succeeded が残さなければ
// ならない output は logical name の集合として requiredOperationOutputs が持つため、
// ここには「output を残すか」という真偽値を重複させない。
var operationKindRules = map[OperationKind]operationKindRule{
	OperationClaim:                {},
	OperationAuthorTests:          {resolvedContext: true},
	OperationReviseTests:          {resolvedContext: true, priorArtifacts: true},
	OperationImplement:            {resolvedContext: true, priorArtifacts: true},
	OperationRepairImplementation: {resolvedContext: true, priorArtifacts: true},
	OperationPublishHead:          {resolvedContext: true, priorArtifacts: true, preservesHead: true, pullRequestRef: true},
	OperationFinalizePullRequest:  {resolvedContext: true, priorArtifacts: true, preservesHead: true, pullRequestRef: true},
}

// WorkerOperation は Controller が queue へ記録する run-once Operation である。
//
// Observation は audit lineage であり content identity へ寄与しない。exact body だけが
// 変わった再観測で Operation identity が変わると、HTML comment の編集や retry のたびに
// 別 Operation になり、既存 approval と重複判定が壊れる。semantic input は
// ContextManifest に閉じており、validator はその ref を opaque な組として扱う。
//
// base SHA を持たないのは意図的である。base は claim が Context Manifest へ pin する
// 解決結果であり、envelope へ複製すると、validator が opaque に扱う manifest ref と
// cross-check できない identity を二重に抱える。repository を Issue reference から
// 導出するのと同じ理由で、同じ値の出所は一つに保つ。
//
// OperationID は logical Operation の stable identity であり、content identity である
// digest とも execution attempt とも別である。CreatedAt は queue 時刻であり、
// caller が clock 境界から明示的に渡す。
type WorkerOperation struct {
	Schema      string
	OperationID string
	RunID       string
	Kind        OperationKind
	Issue       IssueRef

	// Observation と ContextManifest は claim では nil にする。claim はこれらを作る
	// Operation であり、空文字や直前 Run の値から推測してはならない。
	Observation     *IssueObservationRef
	ContextManifest *ContextManifestRef
	ExecutionPolicy ExecutionPolicyRef

	HeadSHA        string
	InputArtifacts []Digest
	PolicyRefs     []string

	CausationID string
	CreatedAt   time.Time
}

// ValidateWorkerOperation は envelope 単体の整合性を検証する。
// ContextManifest と ExecutionPolicy は schema と digest の組としてのみ検証し、
// 参照先の canonical YAML は parse しない。
func ValidateWorkerOperation(op WorkerOperation) error {
	if op.Schema != WorkerOperationSchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"worker operation schema は %q でなければならない: %q", WorkerOperationSchemaV1Alpha1, op.Schema)
	}
	rule, ok := operationKindRules[op.Kind]
	if !ok {
		return protocolErr(ProtocolKindUnknown, "kind", "operation kind が不正: %q", op.Kind)
	}
	for _, field := range []struct{ name, value string }{
		{"operationId", op.OperationID},
		{"runId", op.RunID},
		{"causationId", op.CausationID},
	} {
		if !validProtocolID(field.value) {
			return protocolErr(ProtocolFieldInvalid, field.name, "identifier が不正: %q", field.value)
		}
	}
	if !validIssueRef(op.Issue) {
		return protocolErr(ProtocolFieldInvalid, "issue", "Issue reference が不正")
	}
	if err := validateVersionedRef("executionPolicy", op.ExecutionPolicy.Schema, op.ExecutionPolicy.Digest, executionPolicySchemaPrefix); err != nil {
		return err
	}
	if op.CreatedAt.IsZero() {
		return protocolErr(ProtocolFieldMissing, "createdAt", "作成時刻が空")
	}
	if err := validateOperationContext(op, rule); err != nil {
		return err
	}
	if err := validateDigestSet("inputArtifacts", op.InputArtifacts); err != nil {
		return err
	}
	if rule.priorArtifacts && len(op.InputArtifacts) == 0 {
		return protocolErr(ProtocolKindConstraint, "inputArtifacts",
			"kind %q は先行 artifact の参照を必須とする", op.Kind)
	}
	return validatePolicyRefs(op.PolicyRefs)
}

func validateOperationContext(op WorkerOperation, rule operationKindRule) error {
	if !rule.resolvedContext {
		switch {
		case op.Observation != nil:
			return protocolErr(ProtocolKindConstraint, "issueObservation", "kind %q はまだ Issue Observation を持てない", op.Kind)
		case op.ContextManifest != nil:
			return protocolErr(ProtocolKindConstraint, "contextManifest", "kind %q はまだ Context Manifest を持てない", op.Kind)
		case op.HeadSHA != "":
			return protocolErr(ProtocolKindConstraint, "headSha", "kind %q はまだ head を持てない", op.Kind)
		case len(op.InputArtifacts) > 0:
			return protocolErr(ProtocolKindConstraint, "inputArtifacts", "kind %q はまだ input artifact を持てない", op.Kind)
		}
		return nil
	}

	if op.Observation == nil {
		return protocolErr(ProtocolKindConstraint, "issueObservation", "kind %q は Issue Observation を省略できない", op.Kind)
	}
	if err := validateVersionedRef("issueObservation", op.Observation.Schema, op.Observation.Digest, issueObservationSchemaPrefix); err != nil {
		return err
	}
	if op.ContextManifest == nil {
		return protocolErr(ProtocolKindConstraint, "contextManifest", "kind %q は Context Manifest を省略できない", op.Kind)
	}
	if err := validateVersionedRef("contextManifest", op.ContextManifest.Schema, op.ContextManifest.Digest, contextManifestSchemaPrefix); err != nil {
		return err
	}
	if !validGitSHA(op.HeadSHA) {
		return protocolErr(ProtocolFieldInvalid, "headSha", "commit SHA が不正: %q", op.HeadSHA)
	}
	return nil
}

// OperationDigest は Operation の content identity を返す。
//
// queue 時刻、operation ID、attempt、lease owner、provider session、workspace path、
// および audit lineage である Issue Observation は含まない。したがって、raw body だけが
// 変わった再観測や retry では digest が変わらない。
func OperationDigest(op WorkerOperation) (Digest, error) {
	if err := ValidateWorkerOperation(op); err != nil {
		return "", err
	}
	return SHA256(encodeOperationIdentity(op)), nil
}

// encodeOperationIdentity は validate 済み Operation の identity bytes を返す。
// 保存する envelope 全体ではなく、digest 計算のための canonical 表現である。
func encodeOperationIdentity(op WorkerOperation) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", op.Schema)
	writeYAMLString(&b, 0, "kind", string(op.Kind))
	writeYAMLString(&b, 0, "run", op.RunID)
	writeYAMLString(&b, 0, "repository", op.Issue.repositoryURL())
	writeYAMLString(&b, 0, "issue", op.Issue.String())
	if op.ContextManifest == nil {
		writeYAMLNull(&b, 0, "contextManifest")
	} else {
		writeYAMLRef(&b, 0, "contextManifest", op.ContextManifest.Schema, op.ContextManifest.Digest)
	}
	writeYAMLRef(&b, 0, "executionPolicy", op.ExecutionPolicy.Schema, op.ExecutionPolicy.Digest)
	writeYAMLOptionalString(&b, 0, "headSha", op.HeadSHA)
	writeYAMLStringList(&b, 0, "inputArtifacts", canonicalDigestStrings(op.InputArtifacts))
	writeYAMLStringList(&b, 0, "policyRefs", canonicalStringSet(op.PolicyRefs))
	writeYAMLString(&b, 0, "causation", op.CausationID)
	return []byte(b.String())
}

// OperationOutcome は Operation の terminal な実行結果である。
// approve / request_changes は Review Worker だけが返す品質 verdict であり、
// ここには存在しない。
type OperationOutcome string

const (
	OutcomeSucceeded      OperationOutcome = "succeeded"
	OutcomeStaleInput     OperationOutcome = "stale_input"
	OutcomeNeedsHuman     OperationOutcome = "needs_human"
	OutcomeFailedTerminal OperationOutcome = "failed_terminal"
)

var operationOutcomes = map[OperationOutcome]bool{
	OutcomeSucceeded:      true,
	OutcomeStaleInput:     true,
	OutcomeNeedsHuman:     true,
	OutcomeFailedTerminal: true,
}

// OperationResult は Operation の terminal Result である。
//
// HeadSHA は Operation が固定した head であり、入力 head と一致するとは限らない。
// AttemptID と CompletedAt は attempt の記録であり、content identity へ含めない。
// retry 可能な失敗は Result にせず、AttemptFailure として記録する。
// ChangedInputFields は stale_input と判定した根拠であり、CompareOperationInput が
// 返す SemanticDifference.ChangedFields をそのまま載せる。Controller が「何が変わって
// 止まったか」を Result 以外の経路から補う必要をなくすため、他の outcome では空にする。
type OperationResult struct {
	Schema             string
	OperationDigest    Digest
	AttemptID          string
	Outcome            OperationOutcome
	HeadSHA            string
	ChangedInputFields []string
	OutputArtifacts    []NamedArtifact
	ExternalRefs       []string
	CompletedAt        time.Time
}

// ValidateOperationResult は Result 単体の整合性を検証する。
func ValidateOperationResult(result OperationResult) error {
	if result.Schema != WorkerResultSchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"worker result schema は %q でなければならない: %q", WorkerResultSchemaV1Alpha1, result.Schema)
	}
	if !result.OperationDigest.Valid() {
		return protocolErr(ProtocolFieldInvalid, "operationDigest", "digest が不正: %q", result.OperationDigest)
	}
	if !validProtocolID(result.AttemptID) {
		return protocolErr(ProtocolFieldInvalid, "attemptId", "identifier が不正: %q", result.AttemptID)
	}
	if !operationOutcomes[result.Outcome] {
		return protocolErr(ProtocolFieldInvalid, "outcome", "operation outcome が不正: %q", result.Outcome)
	}
	if result.HeadSHA != "" && !validGitSHA(result.HeadSHA) {
		return protocolErr(ProtocolFieldInvalid, "headSha", "commit SHA が不正: %q", result.HeadSHA)
	}
	if result.CompletedAt.IsZero() {
		return protocolErr(ProtocolFieldMissing, "completedAt", "完了時刻が空")
	}
	if err := validateChangedInputFields(result.Outcome, result.ChangedInputFields); err != nil {
		return err
	}
	if err := validateNamedArtifacts("outputArtifacts", result.OutputArtifacts); err != nil {
		return err
	}
	return validateLineSet("externalRefs", result.ExternalRefs)
}

// validateChangedInputFields は stale_input の根拠が comparison と同じ語彙で記録されて
// いることを保証する。自由文字列を許すと、Controller は Result の値を comparison 結果と
// 突き合わせられず、根拠の記録が飾りになる。
func validateChangedInputFields(outcome OperationOutcome, fields []string) error {
	if outcome != OutcomeStaleInput {
		if len(fields) > 0 {
			return protocolErr(ProtocolOutcomeConflict, "changedInputFields",
				"outcome %q は changedInputFields を持てない", outcome)
		}
		return nil
	}
	if len(fields) == 0 {
		return protocolErr(ProtocolFieldMissing, "changedInputFields",
			"stale_input はどの field が変わったかを記録しなければならない")
	}
	seen := map[string]bool{}
	for i, field := range fields {
		name := fmt.Sprintf("changedInputFields[%d]", i)
		if !operationInputFields[field] {
			return protocolErr(ProtocolFieldInvalid, name, "semantic input field でない: %q", field)
		}
		if seen[field] {
			return protocolErr(ProtocolFieldDuplicate, name, "field が重複: %s", field)
		}
		seen[field] = true
	}
	return nil
}

// OperationResultDigest は Result の content identity を返す。
// attempt ID と完了時刻は含めないため、同じ入力から同じ結果を再生成した attempt は
// 同じ digest を持つ。
func OperationResultDigest(result OperationResult) (Digest, error) {
	if err := ValidateOperationResult(result); err != nil {
		return "", err
	}
	return SHA256(encodeOperationResultIdentity(result)), nil
}

func encodeOperationResultIdentity(result OperationResult) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", result.Schema)
	writeYAMLString(&b, 0, "operationDigest", string(result.OperationDigest))
	writeYAMLString(&b, 0, "outcome", string(result.Outcome))
	writeYAMLOptionalString(&b, 0, "headSha", result.HeadSHA)
	writeYAMLStringList(&b, 0, "changedInputFields", canonicalStringSet(result.ChangedInputFields))
	writeYAMLNamedArtifacts(&b, 0, "outputArtifacts", result.OutputArtifacts)
	writeYAMLStringList(&b, 0, "externalRefs", canonicalStringSet(result.ExternalRefs))
	return []byte(b.String())
}

// BindOperationResult は Result が参照する Operation identity を再計算して照合する。
//
// digest は Context Manifest ref、Execution Policy ref、head、input artifact、
// policy ref を含む semantic identity 全体を覆うため、いずれかが変わった Operation へ
// 以前の Result を bind できない。Issue Observation だけの差分は identity ではないため
// binding を壊さない。
func BindOperationResult(op WorkerOperation, result OperationResult) error {
	digest, err := OperationDigest(op)
	if err != nil {
		return err
	}
	if err := ValidateOperationResult(result); err != nil {
		return err
	}
	if result.OperationDigest != digest {
		return protocolErr(ProtocolIdentityMismatch, "operationDigest",
			"result が別の Operation identity を参照している: got %s, want %s",
			result.OperationDigest, digest)
	}

	rule := operationKindRules[op.Kind]
	switch {
	case !rule.resolvedContext && result.HeadSHA != "":
		return protocolErr(ProtocolKindConstraint, "headSha", "kind %q の Result は head を固定しない", op.Kind)
	case rule.resolvedContext && result.Outcome == OutcomeSucceeded && result.HeadSHA == "":
		return protocolErr(ProtocolKindConstraint, "headSha",
			"kind %q の succeeded Result は head を固定しなければならない", op.Kind)
	}
	if result.Outcome != OutcomeSucceeded {
		return nil
	}
	return bindSucceededOutput(op, result, rule)
}

// bindSucceededOutput は kind ごとの成功条件を binding 境界で検証する。
//
// terminal success はそのまま次 gate の前提になるため、kind が要求する logical name を
// 欠いた Result を通すと、後続 Operation と review は存在しない artifact を入力に選ぶ。
// 非空判定では足りない。protocol 文書は kind ごとに「何を」残すかまで定めており、
// test plan の代わりに任意の 1 件を置いた Result も非空判定なら通ってしまう。
func bindSucceededOutput(op WorkerOperation, result OperationResult, rule operationKindRule) error {
	if err := requireArtifactNames("outputArtifacts", fmt.Sprintf("operation kind %q", op.Kind),
		requiredOperationOutputs[op.Kind], namedArtifactSet(result.OutputArtifacts)); err != nil {
		return err
	}
	if rule.pullRequestRef && !containsPullRequestRef(result.ExternalRefs, op.Issue) {
		return protocolErr(ProtocolKindConstraint, "externalRefs",
			"kind %q の succeeded Result は同じ repository の canonical な PR reference を残さなければならない: %v",
			op.Kind, result.ExternalRefs)
	}
	// head を進めない kind が別 head を報告できると、review していない head に対する
	// 外部 mutation が gate を通る。
	if rule.preservesHead && result.HeadSHA != op.HeadSHA {
		return protocolErr(ProtocolIdentityMismatch, "headSha",
			"kind %q の Result head は承認済み head と一致しなければならない: got %s, want %s",
			op.Kind, result.HeadSHA, op.HeadSHA)
	}
	return nil
}

// containsPullRequestRef は Operation と同じ repository の PR reference が
// external ref に含まれるかを返す。別 repository の PR を成功の根拠にできると、
// Run が触っていない repository の PR が gate を通る。
func containsPullRequestRef(refs []string, issue IssueRef) bool {
	for _, ref := range refs {
		pr, ok := ParsePullRequestRef(ref)
		if ok && pr.sameRepository(issue) {
			return true
		}
	}
	return false
}

// OperationAttemptOutcome は 1 attempt の結末である。Review 側の ReviewAttemptOutcome と
// 同じく、terminal Result と execution failure を同時に持つ attempt を型で禁止する。
// 両方を持てると、失敗した attempt が terminal Result として commit されうる。
type OperationAttemptOutcome struct {
	Result  *OperationResult
	Failure *AttemptFailure
}

// Validate は結末が Result と failure のどちらか一方だけであることを保証する。
func (o OperationAttemptOutcome) Validate() error {
	switch {
	case o.Result != nil && o.Failure != nil:
		return protocolErr(ProtocolOutcomeConflict, "", "terminal Result と attempt failure を同時に持てない")
	case o.Result != nil:
		return ValidateOperationResult(*o.Result)
	case o.Failure != nil:
		return o.Failure.Validate()
	default:
		return protocolErr(ProtocolOutcomeConflict, "", "attempt outcome が Result も failure も持たない")
	}
}
