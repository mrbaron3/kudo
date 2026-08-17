package contract

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// IssueCompilerVersionV1Alpha1 は parser result から Task Context を構築する
	// compiler algorithm の version である。
	IssueCompilerVersionV1Alpha1 = "kudo.issue-compiler/v1alpha1"
	// IssueObservationSchemaV1Alpha1 は exact GitHub observation の schema である。
	IssueObservationSchemaV1Alpha1 = "kudo.issue-observation/v1alpha1"
	// TaskContextSchemaV1Alpha1 は AI 実行入力の schema である。
	TaskContextSchemaV1Alpha1 = "kudo.task-context/v1alpha1"
)

// IssueObservation は verified Issue identity と exact body digest を結び付ける。
// Raw body bytes 自体は RawBodyPayload に保存し、この値へ複製しない。
type IssueObservation struct {
	Schema     string
	Issue      IssueRef
	BodyDigest Digest
}

// IssueObservationRef は observation schema と canonical artifact digest の組である。
type IssueObservationRef struct {
	Schema string
	Digest Digest
}

// TaskCriterion は compiler が解釈済みの Acceptance Criterion である。
type TaskCriterion struct {
	ID   string
	Body string
}

// TaskContext は model session へ渡す versioned な Task Issue 表現である。
// H2 title や Contract Markdown を consumer が再解釈しなくて済むよう、固定 field へ投影する。
type TaskContext struct {
	Schema                      string
	Compiler                    string
	Issue                       IssueRef
	ContractSchema              string
	Kind                        Kind
	Readiness                   Readiness
	Parent                      *IssueRef
	DependsOn                   []IssueRef
	AcceptanceCriteriaIDs       []string
	AuthorityRefs               []AuthorityRef
	Outcome                     string
	Scope                       string
	Deliverables                string
	AcceptanceCriteriaPreamble  string
	AcceptanceCriteria          []TaskCriterion
	VerificationAndEvidence     string
	ConstraintsAndInvariants    string
	DecisionAuthority           string
	StopAndEscalationConditions string
	AdvisoryHints               *string
}

// TaskContextRef は Task Context schema と canonical artifact digest の組である。
type TaskContextRef struct {
	Schema string
	Digest Digest
}

// ClaimRequirements は claim に必要な stable projection だけを保持する。
// Controller/Worker は parser の Task、Contract、section title を再解釈しない。
type ClaimRequirements struct {
	Readiness     Readiness
	Parent        *IssueRef
	DependsOn     []IssueRef
	AuthorityRefs []AuthorityRef
}

// CompiledIssue は一回の pure compile で得られる versioned input と payload を束ねる。
type CompiledIssue struct {
	CompilerVersion    string
	Observation        IssueObservation
	ObservationRef     IssueObservationRef
	RawBodyPayload     ArtifactPayload
	ObservationPayload ArtifactPayload
	TaskContext        TaskContext
	TaskContextRef     TaskContextRef
	TaskContextPayload ArtifactPayload
	ClaimRequirements  ClaimRequirements
}

// Compiler は Issue Contract と Task Context の特定 version の組合せを表す。
// zero value も v1alpha1 compiler として使用できる。
type Compiler struct{}

// NewCompiler は現在の v1alpha1 compiler を返す。
func NewCompiler() Compiler { return Compiler{} }

// Version は compiler algorithm の version を返す。
func (Compiler) Version() string { return IssueCompilerVersionV1Alpha1 }

// Compile は raw GitHub body と検証済み Issue identity から実行入力を構築する。
func Compile(body string, issue IssueRef) (*CompiledIssue, []ValidationError) {
	return NewCompiler().Compile(body, issue)
}

// Compile は external I/O を行わず、同じ入力から byte 単位で同じ artifact を返す。
func (c Compiler) Compile(body string, issue IssueRef) (*CompiledIssue, []ValidationError) {
	if !validIssueRef(issue) {
		return nil, []ValidationError{{
			Code:    CodeIssueRefInvalid,
			Message: "verified Issue identity（owner / repository / 正の number）を明示的に渡す",
		}}
	}
	if !utf8.ValidString(body) {
		return nil, []ValidationError{{
			Code:    CodeBodyEncodingInvalid,
			Message: "Issue body は UTF-8 でなければならない",
		}}
	}
	if errs := validateBodyCharacters(body); len(errs) > 0 {
		return nil, errs
	}
	// GitHub の owner / repository は case-insensitive である。caller が渡す
	// verified identity も Issue 本文の reference と同じ規則で正規化する。
	issue = issue.canonical()

	task, errs := parse(body, issue.repositoryRef())
	if len(errs) > 0 {
		return nil, errs
	}

	rawPayload := newArtifactPayload(ArtifactKindRawIssueBody, "", MediaTypeMarkdown, []byte(body))
	observation := IssueObservation{
		Schema:     IssueObservationSchemaV1Alpha1,
		Issue:      issue,
		BodyDigest: rawPayload.Digest,
	}
	observationBytes := encodeIssueObservation(observation)
	observationPayload := newArtifactPayload(
		ArtifactKindIssueObservation,
		IssueObservationSchemaV1Alpha1,
		MediaTypeYAML,
		observationBytes,
	)
	observationRef := IssueObservationRef{
		Schema: IssueObservationSchemaV1Alpha1,
		Digest: observationPayload.Digest,
	}

	context := buildTaskContext(task, issue, c.Version())
	contextBytes := encodeTaskContext(context)
	contextPayload := newArtifactPayload(
		ArtifactKindTaskContext,
		TaskContextSchemaV1Alpha1,
		MediaTypeYAML,
		contextBytes,
	)
	contextRef := TaskContextRef{Schema: TaskContextSchemaV1Alpha1, Digest: contextPayload.Digest}

	return &CompiledIssue{
		CompilerVersion:    c.Version(),
		Observation:        observation,
		ObservationRef:     observationRef,
		RawBodyPayload:     rawPayload,
		ObservationPayload: observationPayload,
		TaskContext:        context,
		TaskContextRef:     contextRef,
		TaskContextPayload: contextPayload,
		ClaimRequirements: ClaimRequirements{
			Readiness:     task.Contract.Readiness,
			Parent:        cloneIssueRef(task.Contract.Parent),
			DependsOn:     cloneIssueRefs(task.Contract.DependsOn),
			AuthorityRefs: cloneAuthorityRefs(task.Contract.AuthorityRefs),
		},
	}, nil
}

// validateBodyCharacters は canonical artifact へ載せられない control character を
// 信頼境界で拒否する。ここで通すと compile と digest 計算は成功し、PostgreSQL の
// text / jsonb へ保存する段階で初めて失敗するため、失敗境界を入力側へ寄せる。
//
// LF と TAB は本文の構造として許可する。CRLF は行分割で `\r` を落とすため許可し、
// 単独の CR は canonical bytes へ残ってしまうため拒否する。
func validateBodyCharacters(body string) []ValidationError {
	line := 1
	for i, r := range body {
		switch {
		case r == '\n':
			line++
			continue
		case r == '\t':
			continue
		case r == '\r':
			if i+1 < len(body) && body[i+1] == '\n' {
				continue
			}
		case r >= 0x20 && r != 0x7f:
			continue
		}
		return []ValidationError{{
			Code:    CodeBodyControlCharacter,
			Line:    line,
			Message: fmt.Sprintf("Issue body に control character U+%04X は使えない", r),
		}}
	}
	return nil
}

func buildTaskContext(task *parsedTask, issue IssueRef, compilerVersion string) TaskContext {
	// required section の存在は parse が CodeSectionMissing として保証し、
	// Compile は 1 件でも ValidationError があれば buildTaskContext を呼ばない
	// （parse.go の requiredSections 検証を参照）。ここへ到達するのは parser 側の
	// 不変条件が壊れた場合だけであり、誤った Task Context を生成して下流へ流すより
	// 即座に停止する方が安全なため assertion として panic する。
	section := func(title string) string {
		value, ok := task.section(title)
		if !ok {
			panic("validated Task is missing section " + title)
		}
		return canonicalMarkdown(value.Content)
	}

	criteria := make([]TaskCriterion, len(task.AcceptanceCriteria))
	for i, criterion := range task.AcceptanceCriteria {
		criteria[i] = TaskCriterion{ID: criterion.ID, Body: canonicalMarkdown(criterion.Body)}
	}
	acceptanceSection, ok := task.section(sectionAcceptanceCriteria)
	if !ok {
		panic("validated Task is missing Acceptance Criteria")
	}

	var advisory *string
	if value, ok := task.section(sectionAdvisoryHints); ok {
		canonical := canonicalMarkdown(value.Content)
		advisory = &canonical
	}

	return TaskContext{
		Schema:                      TaskContextSchemaV1Alpha1,
		Compiler:                    compilerVersion,
		Issue:                       issue,
		ContractSchema:              task.Contract.Schema,
		Kind:                        task.Contract.Kind,
		Readiness:                   task.Contract.Readiness,
		Parent:                      cloneIssueRef(task.Contract.Parent),
		DependsOn:                   cloneIssueRefs(task.Contract.DependsOn),
		AcceptanceCriteriaIDs:       append([]string(nil), task.Contract.AcceptanceCriteriaIDs...),
		AuthorityRefs:               cloneAuthorityRefs(task.Contract.AuthorityRefs),
		Outcome:                     section(sectionOutcome),
		Scope:                       section(sectionScope),
		Deliverables:                section(sectionDeliverables),
		AcceptanceCriteriaPreamble:  acceptanceCriteriaPreamble(acceptanceSection.Content),
		AcceptanceCriteria:          criteria,
		VerificationAndEvidence:     section(sectionVerification),
		ConstraintsAndInvariants:    section(sectionConstraints),
		DecisionAuthority:           section(sectionDecisionAuthority),
		StopAndEscalationConditions: section(sectionStopConditions),
		AdvisoryHints:               advisory,
	}
}

func encodeIssueObservation(observation IssueObservation) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", observation.Schema)
	writeYAMLString(&b, 0, "issue", observation.Issue.String())
	writeYAMLString(&b, 0, "bodyDigest", string(observation.BodyDigest))
	return []byte(b.String())
}

func encodeTaskContext(context TaskContext) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", context.Schema)
	writeYAMLString(&b, 0, "compiler", context.Compiler)
	writeYAMLString(&b, 0, "issue", context.Issue.String())
	b.WriteString("contract:\n")
	writeYAMLString(&b, 2, "schema", context.ContractSchema)
	writeYAMLString(&b, 2, "kind", string(context.Kind))
	writeYAMLString(&b, 2, "readiness", string(context.Readiness))
	if context.Parent == nil {
		writeYAMLNull(&b, 2, "parent")
	} else {
		writeYAMLString(&b, 2, "parent", context.Parent.String())
	}
	dependsOn := make([]string, len(context.DependsOn))
	for i, ref := range context.DependsOn {
		dependsOn[i] = ref.String()
	}
	writeYAMLStringList(&b, 2, "dependsOn", dependsOn)
	writeYAMLStringList(&b, 2, "acceptanceCriteriaIds", context.AcceptanceCriteriaIDs)
	authorityRefs := make([]string, len(context.AuthorityRefs))
	for i, ref := range context.AuthorityRefs {
		authorityRefs[i] = ref.String()
	}
	writeYAMLStringList(&b, 2, "authorityRefs", authorityRefs)
	writeYAMLString(&b, 0, "outcome", context.Outcome)
	writeYAMLString(&b, 0, "scope", context.Scope)
	writeYAMLString(&b, 0, "deliverables", context.Deliverables)
	b.WriteString("acceptanceCriteria:\n")
	writeYAMLString(&b, 2, "preamble", context.AcceptanceCriteriaPreamble)
	if len(context.AcceptanceCriteria) == 0 {
		b.WriteString("  criteria: []\n")
	} else {
		b.WriteString("  criteria:\n")
		for _, criterion := range context.AcceptanceCriteria {
			b.WriteString("    - id: ")
			b.WriteString(yamlString(criterion.ID))
			b.WriteByte('\n')
			writeYAMLString(&b, 6, "body", criterion.Body)
		}
	}
	writeYAMLString(&b, 0, "verificationAndEvidence", context.VerificationAndEvidence)
	writeYAMLString(&b, 0, "constraintsAndInvariants", context.ConstraintsAndInvariants)
	writeYAMLString(&b, 0, "decisionAuthority", context.DecisionAuthority)
	writeYAMLString(&b, 0, "stopAndEscalationConditions", context.StopAndEscalationConditions)
	if context.AdvisoryHints == nil {
		writeYAMLNull(&b, 0, "advisoryHints")
	} else {
		writeYAMLString(&b, 0, "advisoryHints", *context.AdvisoryHints)
	}
	return []byte(b.String())
}

// ReadIssueObservationArtifact は ref と payload を照合し、保存 bytes をそのまま返す。
func ReadIssueObservationArtifact(ref IssueObservationRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, issueObservationSchemaPrefix) {
		return nil, fmt.Errorf("IssueObservationRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindIssueObservation, ref.Schema, ref.Digest, payload)
}

// ReadTaskContextArtifact は ref と payload を照合し、再 encode せず保存 bytes を返す。
func ReadTaskContextArtifact(ref TaskContextRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, taskContextSchemaPrefix) {
		return nil, fmt.Errorf("TaskContextRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindTaskContext, ref.Schema, ref.Digest, payload)
}
