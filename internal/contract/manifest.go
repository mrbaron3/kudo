package contract

import (
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
//
// claim には同じ Task Context を compile して得た ClaimRequirements を渡す。
// manifest は「Task Context から解決した closure」であり、その対応関係を呼び出し側の
// 規律に任せず encode 境界で強制する。manifest が持つのは解決結果（digest と base SHA）
// だけで、どの reference を解決するかは Task Context が決める。
func EncodeContextManifest(claim ClaimRequirements, manifest ContextManifest) (ContextManifestRef, ArtifactPayload, error) {
	if err := validateContextManifest(manifest); err != nil {
		return ContextManifestRef{}, ArtifactPayload{}, err
	}
	if err := validateManifestMatchesClaim(claim, manifest); err != nil {
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
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"context manifest schema は %q でなければならない: %q", ContextManifestSchemaV1Alpha1, manifest.Schema)
	}
	if err := validateVersionedRef("taskContext", manifest.TaskContext.Schema, manifest.TaskContext.Digest, taskContextSchemaPrefix); err != nil {
		return err
	}
	if !validGitSHA(manifest.BaseSHA) {
		return protocolErr(ProtocolFieldInvalid, "baseSha",
			"base SHA は 40 または 64 桁の lowercase hex でなければならない: %q", manifest.BaseSHA)
	}
	if manifest.Parent != nil && !validIssueRef(*manifest.Parent) {
		return protocolErr(ProtocolFieldInvalid, "parent", "parent Issue reference が不正")
	}

	seenDependencies := map[string]bool{}
	for i, dependency := range manifest.Dependencies {
		if !validIssueRef(dependency.Issue) {
			return protocolErr(ProtocolFieldInvalid, fmt.Sprintf("dependencies[%d].issue", i), "Issue reference が不正")
		}
		if !dependency.CompletionDigest.Valid() {
			return protocolErr(ProtocolFieldInvalid, fmt.Sprintf("dependencies[%d].completionDigest", i),
				"digest が不正: %q", dependency.CompletionDigest)
		}
		key := dependency.Issue.String()
		if seenDependencies[key] {
			return protocolErr(ProtocolFieldDuplicate, fmt.Sprintf("dependencies[%d].issue", i), "Issue が重複: %s", key)
		}
		seenDependencies[key] = true
	}

	seenAuthority := map[string]bool{}
	for i, authority := range manifest.AuthorityRefs {
		key, err := validateAuthorityIdentity(fmt.Sprintf("authorityRefs[%d].ref", i), authority.Ref)
		if err != nil {
			return err
		}
		if seenAuthority[key] {
			return protocolErr(ProtocolFieldDuplicate, fmt.Sprintf("authorityRefs[%d].ref", i), "authority が重複: %s", key)
		}
		seenAuthority[key] = true
		if !authority.ContentDigest.Valid() {
			return protocolErr(ProtocolFieldInvalid, fmt.Sprintf("authorityRefs[%d].contentDigest", i),
				"digest が不正: %q", authority.ContentDigest)
		}
	}
	return nil
}

// validateManifestMatchesClaim は manifest が Task Context の宣言と 1 対 1 に
// 対応することを検証する。件数・identity・順序のいずれかが食い違えば拒否する。
// 順序は authority の優先順位（Issue Contract の authorityRefs 順）を表すため、
// 集合一致では不十分である。
func validateManifestMatchesClaim(claim ClaimRequirements, manifest ContextManifest) error {
	switch {
	case claim.Parent == nil && manifest.Parent != nil:
		return protocolErr(ProtocolIdentityMismatch, "parent", "Task Context が parent を持たないのに manifest に parent がある")
	case claim.Parent != nil && manifest.Parent == nil:
		return protocolErr(ProtocolIdentityMismatch, "parent", "Task Context の parent が manifest で解決されていない")
	case claim.Parent != nil && claim.Parent.String() != manifest.Parent.String():
		return protocolErr(ProtocolIdentityMismatch, "parent", "parent が Task Context と一致しない: got %s, want %s",
			manifest.Parent.String(), claim.Parent.String())
	}

	if len(manifest.Dependencies) != len(claim.DependsOn) {
		return protocolErr(ProtocolIdentityMismatch, "dependencies",
			"件数が dependsOn と一致しない: got %d, want %d", len(manifest.Dependencies), len(claim.DependsOn))
	}
	for i, dependency := range manifest.Dependencies {
		if dependency.Issue.String() != claim.DependsOn[i].String() {
			return protocolErr(ProtocolIdentityMismatch, fmt.Sprintf("dependencies[%d]", i),
				"dependsOn の宣言順と一致しない: got %s, want %s",
				dependency.Issue.String(), claim.DependsOn[i].String())
		}
	}

	if len(manifest.AuthorityRefs) != len(claim.AuthorityRefs) {
		return protocolErr(ProtocolIdentityMismatch, "authorityRefs",
			"件数が Task Context と一致しない: got %d, want %d",
			len(manifest.AuthorityRefs), len(claim.AuthorityRefs))
	}
	for i, authority := range manifest.AuthorityRefs {
		// 重複検出と同じ identity 関数で比較し、path と Issue reference の
		// 同一性判定が 2 通りに分かれないようにする。
		got, err := validateAuthorityIdentity(fmt.Sprintf("authorityRefs[%d].ref", i), authority.Ref)
		if err != nil {
			return err
		}
		want, err := validateAuthorityIdentity(fmt.Sprintf("taskContext.authorityRefs[%d]", i), claim.AuthorityRefs[i])
		if err != nil {
			return err
		}
		if got != want {
			return protocolErr(ProtocolIdentityMismatch, fmt.Sprintf("authorityRefs[%d]", i),
				"Task Context の宣言順と一致しない: got %s, want %s", got, want)
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

// validateAuthorityIdentity は authority の重複検出と順序比較に使う identity key を返す。
// field は error に載せる protocol 上の位置であり、呼び出し側が index を含めて渡す。
func validateAuthorityIdentity(field string, ref AuthorityRef) (string, error) {
	switch {
	case ref.Path != "" && ref.Issue == nil:
		if !validProtocolAuthorityPath(ref.Path) {
			return "", protocolErr(protocolAuthorityPathCode(ref.Path), field,
				"repository-relative path が不正: %q", ref.Path)
		}
		return "path:" + ref.Path, nil
	case ref.Path == "" && ref.Issue != nil:
		if !validIssueRef(*ref.Issue) {
			return "", protocolErr(ProtocolFieldInvalid, field, "Issue reference が不正")
		}
		return "issue:" + ref.Issue.String(), nil
	default:
		return "", protocolErr(ProtocolFieldInvalid, field, "path または Issue reference のどちらか一方だけが必要")
	}
}

func encodeContextManifest(manifest ContextManifest) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", manifest.Schema)
	writeYAMLRef(&b, 0, "taskContext", manifest.TaskContext.Schema, manifest.TaskContext.Digest)
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
	if !validSchemaIdentity(ref.Schema, contextManifestSchemaPrefix) {
		return nil, protocolSchemaErr("schema", ref.Schema, "ContextManifestRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindContextManifest, ref.Schema, ref.Digest, payload)
}
