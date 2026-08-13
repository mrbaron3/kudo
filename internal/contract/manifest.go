package contract

import (
	"errors"
	"fmt"
	"strings"
)

// ContextManifestSchemaV1Alpha1 は resolved execution input manifest の schema である。
const ContextManifestSchemaV1Alpha1 = "kudo.context-manifest/v1alpha1"

// DependencyCompletion は dependency Issue と base 統合済み completion identity を結ぶ。
type DependencyCompletion struct {
	Issue            IssueRef
	CompletionDigest Digest
}

// AuthorityContent は明示された authority reference と immutable content digest を結ぶ。
type AuthorityContent struct {
	Ref           AuthorityRef
	ContentDigest Digest
}

// ContextManifest は Task Context から解決した実装入力の closure である。
// Issue Observation と raw body digest は意図的に持たない。
type ContextManifest struct {
	Schema        string
	TaskContext   TaskContextRef
	BaseSHA       string
	Parent        *IssueRef
	Dependencies  []DependencyCompletion
	AuthorityRefs []AuthorityContent
}

// ContextManifestRef は manifest schema と canonical artifact digest の組である。
type ContextManifestRef struct {
	Schema string
	Digest Digest
}

// EncodeContextManifest は manifest を検証し、canonical payload と ref を返す。
func EncodeContextManifest(manifest ContextManifest) (ContextManifestRef, ArtifactPayload, error) {
	if err := validateContextManifest(manifest); err != nil {
		return ContextManifestRef{}, ArtifactPayload{}, err
	}
	data := encodeContextManifest(manifest)
	payload := newArtifactPayload(
		ArtifactKindContextManifest,
		ContextManifestSchemaV1Alpha1,
		MediaTypeYAML,
		data,
	)
	return ContextManifestRef{Schema: ContextManifestSchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

func validateContextManifest(manifest ContextManifest) error {
	if manifest.Schema != ContextManifestSchemaV1Alpha1 {
		return fmt.Errorf("context manifest schema は %q でなければならない", ContextManifestSchemaV1Alpha1)
	}
	if !validSchemaIdentity(manifest.TaskContext.Schema, "kudo.task-context/") {
		return errors.New("TaskContextRef schema が不正")
	}
	if !manifest.TaskContext.Digest.Valid() {
		return errors.New("TaskContextRef digest が不正")
	}
	if !validGitSHA(manifest.BaseSHA) {
		return errors.New("base SHA は 40 または 64 桁の lowercase hex でなければならない")
	}
	if manifest.Parent != nil && !validIssueRef(*manifest.Parent) {
		return errors.New("parent IssueRef が不正")
	}

	seenDependencies := map[string]bool{}
	for i, dependency := range manifest.Dependencies {
		if !validIssueRef(dependency.Issue) {
			return fmt.Errorf("dependencies[%d].issue が不正", i)
		}
		if !dependency.CompletionDigest.Valid() {
			return fmt.Errorf("dependencies[%d].completionDigest が不正", i)
		}
		key := refFoldKey(dependency.Issue)
		if seenDependencies[key] {
			return fmt.Errorf("dependencies[%d].issue が重複", i)
		}
		seenDependencies[key] = true
	}

	seenAuthority := map[string]bool{}
	for i, authority := range manifest.AuthorityRefs {
		key, err := validateAuthorityIdentity(authority.Ref)
		if err != nil {
			return fmt.Errorf("authorityRefs[%d].ref: %w", i, err)
		}
		if seenAuthority[key] {
			return fmt.Errorf("authorityRefs[%d].ref が重複", i)
		}
		seenAuthority[key] = true
		if !authority.ContentDigest.Valid() {
			return fmt.Errorf("authorityRefs[%d].contentDigest が不正", i)
		}
	}
	return nil
}

func validGitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for i := range len(value) {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func validateAuthorityIdentity(ref AuthorityRef) (string, error) {
	switch {
	case ref.Path != "" && ref.Issue == nil:
		if !validAuthorityPath(ref.Path) {
			return "", errors.New("repository-relative path が不正")
		}
		return "path:" + ref.Path, nil
	case ref.Path == "" && ref.Issue != nil:
		if !validIssueRef(*ref.Issue) {
			return "", errors.New("IssueRef が不正")
		}
		return "issue:" + refFoldKey(*ref.Issue), nil
	default:
		return "", errors.New("path または IssueRef のどちらか一方だけが必要")
	}
}

func encodeContextManifest(manifest ContextManifest) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", manifest.Schema)
	b.WriteString("taskContext:\n")
	writeYAMLString(&b, 2, "schema", manifest.TaskContext.Schema)
	writeYAMLString(&b, 2, "digest", string(manifest.TaskContext.Digest))
	writeYAMLString(&b, 0, "baseSha", manifest.BaseSHA)
	if manifest.Parent == nil {
		writeYAMLNull(&b, 0, "parent")
	} else {
		writeYAMLString(&b, 0, "parent", manifest.Parent.String())
	}
	if len(manifest.Dependencies) == 0 {
		b.WriteString("dependencies: []\n")
	} else {
		b.WriteString("dependencies:\n")
		for _, dependency := range manifest.Dependencies {
			b.WriteString("  - issue: ")
			b.WriteString(yamlString(dependency.Issue.String()))
			b.WriteByte('\n')
			writeYAMLString(&b, 4, "completionDigest", string(dependency.CompletionDigest))
		}
	}
	if len(manifest.AuthorityRefs) == 0 {
		b.WriteString("authorityRefs: []\n")
	} else {
		b.WriteString("authorityRefs:\n")
		for _, authority := range manifest.AuthorityRefs {
			b.WriteString("  - ref: ")
			b.WriteString(yamlString(authority.Ref.String()))
			b.WriteByte('\n')
			writeYAMLString(&b, 4, "contentDigest", string(authority.ContentDigest))
		}
	}
	return []byte(b.String())
}

// ReadContextManifestArtifact は ref/payload を照合して保存 bytes を返す。
func ReadContextManifestArtifact(ref ContextManifestRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, "kudo.context-manifest/") {
		return nil, fmt.Errorf("ContextManifestRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindContextManifest, ref.Schema, ref.Digest, payload)
}
