package contract

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestContextManifestDifferenceMatrix(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	base := sampleContextManifest(compiled)
	baseRef, _, err := EncodeContextManifest(base)
	if err != nil {
		t.Fatal(err)
	}

	variants := map[string]ContextManifest{}

	changedBase := base
	changedBase.BaseSHA = "1123456789abcdef0123456789abcdef01234567"
	variants["base SHA"] = changedBase

	changedDependency := base
	changedDependency.Dependencies = append([]DependencyCompletion(nil), base.Dependencies...)
	changedDependency.Dependencies[0].CompletionDigest = SHA256([]byte("changed completion"))
	variants["dependency completion"] = changedDependency

	changedAuthority := base
	changedAuthority.AuthorityRefs = append([]AuthorityContent(nil), base.AuthorityRefs...)
	changedAuthority.AuthorityRefs[0].ContentDigest = SHA256([]byte("changed authority"))
	variants["authority content"] = changedAuthority

	changedParent := base
	changedParent.Parent = &IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 2}
	variants["parent identity"] = changedParent

	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			got, payload, err := EncodeContextManifest(variant)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseRef {
				t.Fatal("resolved input の変更で ContextManifestRef が変わらない")
			}
			if variant.TaskContext != compiled.TaskContextRef {
				t.Fatal("manifest input の変更で TaskContextRef が変化")
			}
			if strings.Contains(string(payload.Data), "issueObservation") || strings.Contains(string(payload.Data), "bodyDigest") {
				t.Fatal("Context Manifest が Issue Observation/bodyDigest を含む")
			}
		})
	}
}

func TestExecutionPolicyDifferenceIsIsolated(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	manifestRef, _, err := EncodeContextManifest(sampleContextManifest(compiled))
	if err != nil {
		t.Fatal(err)
	}

	base := sampleExecutionPolicy()
	baseRef, _, err := EncodeExecutionPolicy(base)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.ReviewWorker.Model = "claude-sonnet"
	changedRef, _, err := EncodeExecutionPolicy(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedRef == baseRef {
		t.Fatal("policy change で ExecutionPolicyRef が変わらない")
	}

	afterManifestRef, _, err := EncodeContextManifest(sampleContextManifest(compiled))
	if err != nil {
		t.Fatal(err)
	}
	if afterManifestRef != manifestRef || compiled.TaskContextRef != sampleContextManifest(compiled).TaskContext {
		t.Fatal("Execution Policy change が TaskContextRef/ContextManifestRef に波及")
	}
}

func TestExecutionPolicyCanonicalizesPermissionSet(t *testing.T) {
	first := sampleExecutionPolicy()
	second := sampleExecutionPolicy()
	second.IssueWorker.ToolPermissions = []string{"github:write", "repository:write"}

	firstRef, firstPayload, err := EncodeExecutionPolicy(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRef, secondPayload, err := EncodeExecutionPolicy(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstRef != secondRef || !bytes.Equal(firstPayload.Data, secondPayload.Data) {
		t.Fatal("順序を持たない tool permission set の canonical bytes が一致しない")
	}
}

func TestVersionedArtifactReadPreservesBytes(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/minimal.md"))
	got, err := ReadTaskContextArtifact(compiled.TaskContextRef, compiled.TaskContextPayload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, compiled.TaskContextPayload.Data) {
		t.Fatal("read bytes が保存 payload と一致しない")
	}
	got[0] = 'X'
	if compiled.TaskContextPayload.Data[0] == 'X' {
		t.Fatal("reader が payload の mutable slice を共有した")
	}

	tampered := compiled.TaskContextPayload
	tampered.Data = append([]byte(nil), tampered.Data...)
	tampered.Data[0] = 'X'
	if _, err := ReadTaskContextArtifact(compiled.TaskContextRef, tampered); err == nil {
		t.Fatal("改変 payload を受理した")
	}

	// Reader は schema/digest と bytes を照合するだけで再 encode しない。
	// そのため、将来の非互換 Task Context schema も旧 artifact と並存できる。
	futureBytes := []byte("schema: \"kudo.task-context/v1beta1\"\nnewRepresentation: true\n")
	futurePayload := newArtifactPayload(
		ArtifactKindTaskContext,
		"kudo.task-context/v1beta1",
		MediaTypeYAML,
		futureBytes,
	)
	futureRef := TaskContextRef{Schema: futurePayload.Schema, Digest: futurePayload.Digest}
	futureGot, err := ReadTaskContextArtifact(futureRef, futurePayload)
	if err != nil {
		t.Fatalf("future schema artifact の opaque read: %v", err)
	}
	if !bytes.Equal(futureGot, futureBytes) {
		t.Fatal("future schema artifact が再 encode された")
	}

	wrongSchema := futureRef
	wrongSchema.Schema = TaskContextSchemaV1Alpha1
	if _, err := ReadTaskContextArtifact(wrongSchema, futurePayload); err == nil {
		t.Fatal("schema と digest の組を崩した ref を受理した")
	}
}

func TestArtifactSchemasAreIndependent(t *testing.T) {
	schemas := []string{
		IssueContractSchemaV1Alpha1,
		IssueObservationSchemaV1Alpha1,
		TaskContextSchemaV1Alpha1,
		ContextManifestSchemaV1Alpha1,
		ExecutionPolicySchemaV1Alpha1,
	}
	seen := map[string]bool{}
	for _, schema := range schemas {
		if schema == "" || seen[schema] {
			t.Fatalf("schema identity が空または重複: %q", schema)
		}
		seen[schema] = true
	}

	compiled := requireCompiled(t, readFixture(t, "valid/minimal.md"))
	if compiled.ObservationRef.Schema == compiled.TaskContextRef.Schema {
		t.Fatal("Issue Observation と Task Context が schema identity を共有")
	}
}

func TestContextManifestValidation(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	valid := sampleContextManifest(compiled)
	tests := map[string]func(*ContextManifest){
		"schema":              func(m *ContextManifest) { m.Schema = "kudo.context-manifest/v2" },
		"task context schema": func(m *ContextManifest) { m.TaskContext.Schema = "" },
		"task context version": func(m *ContextManifest) {
			m.TaskContext.Schema = "kudo.task-context/"
		},
		"task context digest": func(m *ContextManifest) { m.TaskContext.Digest = "sha256:nope" },
		"base SHA":            func(m *ContextManifest) { m.BaseSHA = strings.ToUpper(m.BaseSHA) },
		"parent":              func(m *ContextManifest) { m.Parent = &IssueRef{} },
		"duplicate dependency": func(m *ContextManifest) {
			m.Dependencies = append(m.Dependencies, m.Dependencies[0])
		},
		"ambiguous authority": func(m *ContextManifest) {
			m.AuthorityRefs[0].Ref.Issue = &IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 1}
		},
		"duplicate authority": func(m *ContextManifest) {
			m.AuthorityRefs = append(m.AuthorityRefs, m.AuthorityRefs[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := valid
			got.Dependencies = append([]DependencyCompletion(nil), valid.Dependencies...)
			got.AuthorityRefs = append([]AuthorityContent(nil), valid.AuthorityRefs...)
			mutate(&got)
			if _, _, err := EncodeContextManifest(got); err == nil {
				t.Fatal("invalid manifest を受理した")
			}
		})
	}
}

func TestExecutionPolicyValidation(t *testing.T) {
	valid := sampleExecutionPolicy()
	tests := map[string]func(*ExecutionPolicy){
		"schema":   func(p *ExecutionPolicy) { p.Schema = "" },
		"provider": func(p *ExecutionPolicy) { p.IssueWorker.Provider = "" },
		"timeout":  func(p *ExecutionPolicy) { p.ReviewWorker.Timeout = 0 },
		"duplicate tool": func(p *ExecutionPolicy) {
			p.IssueWorker.ToolPermissions = []string{"repository:write", "repository:write"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := valid
			mutate(&got)
			if _, _, err := EncodeExecutionPolicy(got); err == nil {
				t.Fatal("invalid policy を受理した")
			}
		})
	}
}

func TestReferenceTypesIncludeSchemaAndDigest(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	manifestRef, manifestPayload, err := EncodeContextManifest(sampleContextManifest(compiled))
	if err != nil {
		t.Fatal(err)
	}
	policyRef, policyPayload, err := EncodeExecutionPolicy(sampleExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}

	refs := []struct {
		schema string
		digest Digest
	}{
		{compiled.ObservationRef.Schema, compiled.ObservationRef.Digest},
		{compiled.TaskContextRef.Schema, compiled.TaskContextRef.Digest},
		{manifestRef.Schema, manifestRef.Digest},
		{policyRef.Schema, policyRef.Digest},
	}
	for _, ref := range refs {
		if ref.schema == "" || !ref.digest.Valid() {
			t.Fatalf("incomplete versioned ref: %+v", ref)
		}
	}

	if _, err := ReadContextManifestArtifact(manifestRef, manifestPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExecutionPolicyArtifact(policyRef, policyPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIssueObservationArtifact(compiled.ObservationRef, compiled.ObservationPayload); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactPayloadValidationRejectsAmbiguousMetadata(t *testing.T) {
	raw := newArtifactPayload(ArtifactKindRawIssueBody, "", MediaTypeMarkdown, []byte("body"))
	versioned := newArtifactPayload(
		ArtifactKindTaskContext,
		TaskContextSchemaV1Alpha1,
		MediaTypeYAML,
		[]byte("schema: test\n"),
	)
	tests := map[string]ArtifactPayload{
		"unknown kind": func() ArtifactPayload {
			got := raw
			got.Kind = "unknown"
			return got
		}(),
		"raw schema": func() ArtifactPayload {
			got := raw
			got.Schema = "unexpected"
			return got
		}(),
		"raw media type": func() ArtifactPayload {
			got := raw
			got.MediaType = MediaTypeYAML
			return got
		}(),
		"missing versioned schema": func() ArtifactPayload {
			got := versioned
			got.Schema = ""
			return got
		}(),
		"missing schema version": func() ArtifactPayload {
			got := versioned
			got.Schema = "kudo.task-context/"
			return got
		}(),
		"versioned media type": func() ArtifactPayload {
			got := versioned
			got.MediaType = MediaTypeMarkdown
			return got
		}(),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if err := payload.Validate(); err == nil {
				t.Fatal("ambiguous artifact metadata を受理した")
			}
		})
	}
}

func TestClaimRequirementsIsStableProjection(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	want := ClaimRequirements{
		Readiness: ReadinessReady,
		Parent:    &IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 1},
		DependsOn: []IssueRef{
			{Owner: "mrbaron3", Repository: "kudo", Number: 31},
			{Owner: "mrbaron3", Repository: "kudo", Number: 9},
		},
		AuthorityRefs: []AuthorityRef{
			{Path: "AGENTS.md"},
			{Path: "docs/contracts/issue-contract-v1alpha1.md"},
			{Path: ".github/ISSUE_TEMPLATE/kudo-task.md"},
			{Issue: &IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 1}},
		},
	}
	if !reflect.DeepEqual(compiled.ClaimRequirements, want) {
		t.Fatalf("ClaimRequirements = %+v, want %+v", compiled.ClaimRequirements, want)
	}
}
