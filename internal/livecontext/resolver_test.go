package livecontext

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

const resolverBaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestResolverBuildsManifestFromExactAuthorityBytes(t *testing.T) {
	t.Parallel()

	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	body := taskBody("ready", "[]", []string{"AGENTS.md", "github://acme/widgets/issues/23"})
	compiled, validation := contract.Compile(body, issue)
	if len(validation) > 0 {
		t.Fatalf("Compile() errors = %v", validation)
	}
	source := &fakeSource{
		issues: map[string][]byte{
			"github://acme/widgets/issues/23": []byte("authority issue\r\n"),
		},
		contents: map[string][]byte{
			"AGENTS.md@" + resolverBaseSHA: []byte("repository authority\n"),
		},
	}

	resolved, err := NewResolver(source).Resolve(t.Context(), compiled, resolverBaseSHA)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Manifest.TaskContext != compiled.TaskContextRef || resolved.Manifest.BaseSHA != resolverBaseSHA {
		t.Fatalf("manifest = %#v", resolved.Manifest)
	}
	if len(resolved.Authorities) != 2 ||
		resolved.Authorities[0].Digest != contract.SHA256([]byte("repository authority\n")) ||
		resolved.Authorities[1].Digest != contract.SHA256([]byte("authority issue\r\n")) {
		t.Fatalf("authorities = %#v", resolved.Authorities)
	}
	if resolved.Manifest.AuthorityRefs[0].ContentDigest != resolved.Authorities[0].Digest ||
		resolved.Manifest.AuthorityRefs[1].ContentDigest != resolved.Authorities[1].Digest {
		t.Fatalf("manifest authority refs = %#v", resolved.Manifest.AuthorityRefs)
	}
}

func TestResolverClassifiesReadinessDependencyAndMissingAuthority(t *testing.T) {
	t.Parallel()

	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	tests := []struct {
		name      string
		body      string
		source    *fakeSource
		wantError error
	}{
		{name: "draft", body: taskBody("draft", "[]", nil), source: &fakeSource{}, wantError: ErrNotReady},
		{name: "dependency", body: taskBody("ready", "[github://acme/widgets/issues/16]", nil), source: &fakeSource{}, wantError: ErrWaitingDependency},
		{name: "missing authority", body: taskBody("ready", "[]", []string{"missing.md"}), source: &fakeSource{contentErr: ErrSourceNotFound}, wantError: ErrAuthorityMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, validation := contract.Compile(tt.body, issue)
			if len(validation) > 0 {
				t.Fatalf("Compile() errors = %v", validation)
			}
			_, err := NewResolver(tt.source).Resolve(t.Context(), compiled, resolverBaseSHA)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Resolve() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestReconstructUsesPinnedCompilerAndBase(t *testing.T) {
	t.Parallel()

	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	body := taskBody("ready", "[]", []string{"AGENTS.md"})
	source := &fakeSource{
		issues:   map[string][]byte{issue.String(): []byte(body)},
		contents: map[string][]byte{"AGENTS.md@" + resolverBaseSHA: []byte("rules")},
	}
	compiler, err := contract.CompilerForVersion(contract.IssueCompilerVersionV1Alpha1)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NewResolver(source).Reconstruct(t.Context(), compiler, issue, resolverBaseSHA)
	if err != nil {
		t.Fatalf("Reconstruct() error = %v", err)
	}
	if resolved.Compiled.CompilerVersion != compiler.Version() || source.lastRef != resolverBaseSHA {
		t.Fatalf("compiler = %q, ref = %q", resolved.Compiled.CompilerVersion, source.lastRef)
	}
}

func TestReconstructClaimKeepsBodyOnlyDifferenceOutOfSemanticIdentity(t *testing.T) {
	t.Parallel()

	issue := contract.IssueRef{Owner: "acme", Repository: "widgets", Number: 17}
	body := taskBody("ready", "[]", []string{"AGENTS.md"})
	source := &fakeSource{
		issues:   map[string][]byte{issue.String(): []byte(body)},
		contents: map[string][]byte{"AGENTS.md@" + resolverBaseSHA: []byte("rules")},
	}
	compiler := contract.NewCompiler()
	initial, err := NewResolver(source).Reconstruct(t.Context(), compiler, issue, resolverBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := contract.ClaimContext{
		Compiler: compiler.Version(), Observation: initial.Compiled.ObservationRef,
		BodyDigest: initial.Compiled.Observation.BodyDigest, TaskContext: initial.Compiled.TaskContextRef,
		ContextManifest: initial.ManifestRef, BaseSHA: resolverBaseSHA,
	}
	source.issues[issue.String()] = []byte(body + "\n<!-- audit-only edit -->\n")

	reconstructed, err := NewResolver(source).ReconstructClaim(t.Context(), issue, checkpoint)
	if err != nil {
		t.Fatalf("ReconstructClaim() error = %v", err)
	}
	if reconstructed.ObservationMatches || !reconstructed.SameSemanticInput() {
		t.Fatalf("reconstruction = %#v", reconstructed)
	}
}

type fakeSource struct {
	issues     map[string][]byte
	contents   map[string][]byte
	issueErr   error
	contentErr error
	lastRef    string
}

func (f *fakeSource) ReadIssue(_ context.Context, issue contract.IssueRef) ([]byte, error) {
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	return append([]byte(nil), f.issues[issue.String()]...), nil
}

func (f *fakeSource) ReadRepositoryContent(_ context.Context, _ contract.IssueRef, path, ref string) ([]byte, error) {
	f.lastRef = ref
	if f.contentErr != nil {
		return nil, f.contentErr
	}
	return append([]byte(nil), f.contents[path+"@"+ref]...), nil
}

func taskBody(readiness, dependencies string, authority []string) string {
	depends := "dependsOn: " + dependencies
	if dependencies != "[]" {
		depends = "dependsOn:\n  - " + strings.Trim(dependencies, "[]")
	}
	authorityYAML := "authorityRefs: []"
	if len(authority) > 0 {
		authorityYAML = "authorityRefs:\n"
		for _, ref := range authority {
			authorityYAML += "  - " + ref + "\n"
		}
		authorityYAML = strings.TrimSuffix(authorityYAML, "\n")
	}
	return "## Contract\n\n```yaml\n" +
		"schema: kudo.issue/v1alpha1\nkind: task\nreadiness: " + readiness + "\nparent: null\n" +
		depends + "\nacceptanceCriteriaIds:\n  - AC-1\n" + authorityYAML + "\n```\n\n" +
		"## Outcome\n\n結果。\n\n## Scope\n\n### Included\n\n- 対象\n\n### Excluded\n\n- 対象外\n\n" +
		"## Deliverables\n\n- 成果物\n\n## Acceptance Criteria\n\n### AC-1\n\n- Given: 前提\n- When: 操作\n- Then: 結果\n\n" +
		"## Verification and Evidence\n\n- test\n\n## Constraints and Invariants\n\n- 制約\n\n" +
		"## Decision Authority\n\n- 判断\n\n## Stop and Escalation Conditions\n\n- 停止\n"
}
