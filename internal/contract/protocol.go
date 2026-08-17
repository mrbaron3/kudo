package contract

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// 本 file は Worker Operation protocol と Implementation–Review protocol が共有する
// identity 素材を扱う。正本は docs/contracts/operation-protocol-v1alpha1.md と
// docs/contracts/review-protocol-v1alpha1.md である。

// versioned ref の schema namespace。ref は schema と digest の組で比較し、
// digest だけから schema を推測しない。
const (
	issueObservationSchemaPrefix = "kudo.issue-observation/"
	taskContextSchemaPrefix      = "kudo.task-context/"
	contextManifestSchemaPrefix  = "kudo.context-manifest/"
	executionPolicySchemaPrefix  = "kudo.execution-policy/"
	artifactManifestSchemaPrefix = "kudo.artifact-manifest/"
)

// validProtocolID は Operation、Run、attempt、finding 等の identifier を検証する。
// canonical bytes と database key の両方へ載るため、空白・control character・
// 長大な値を受け付けない。
func validProtocolID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// validCanonicalText は canonical YAML と PostgreSQL text へそのまま載せられる本文かを返す。
// LF と TAB だけを構造として許可する。単独 CR を含む他の control character は、
// Issue body と同じ理由（保存段階まで失敗を遅らせない）で信頼境界で拒否する。
func validCanonicalText(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r == '\n', r == '\t':
		case r < 0x20, r == 0x7f:
			return false
		}
	}
	return true
}

// validCanonicalLine は改行と TAB を含まない単一行の値を検証する。
func validCanonicalLine(value string) bool {
	return validCanonicalText(value) && !strings.ContainsAny(value, "\n\t")
}

func validateVersionedRef(name, schema string, digest Digest, prefix string) error {
	if !validSchemaIdentity(schema, prefix) {
		return fmt.Errorf("%s の schema が不正: %q", name, schema)
	}
	if !digest.Valid() {
		return fmt.Errorf("%s の digest が不正: %q", name, digest)
	}
	return nil
}

// digest list と policy ref list は順序を持たない集合として扱う。producer が
// 解決した順序は identity ではないため canonical 順へ揃え、同じ値の重複は
// 片方を黙って落とさずに拒否する。

func canonicalDigestStrings(digests []Digest) []string {
	values := make([]string, len(digests))
	for i, digest := range digests {
		values[i] = string(digest)
	}
	slices.Sort(values)
	return values
}

func canonicalStringSet(values []string) []string {
	sorted := append([]string(nil), values...)
	slices.Sort(sorted)
	return sorted
}

func validateDigestSet(name string, digests []Digest) error {
	seen := map[Digest]bool{}
	for i, digest := range digests {
		if !digest.Valid() {
			return fmt.Errorf("%s[%d] が不正な digest: %q", name, i, digest)
		}
		if seen[digest] {
			return fmt.Errorf("%s[%d] が重複: %s", name, i, digest)
		}
		seen[digest] = true
	}
	return nil
}

// validatePolicyRefs は Operation / Review へ明示された policy document を検証する。
// authority content と違い、policy ref は repository-relative path だけを許可する。
func validatePolicyRefs(refs []string) error {
	seen := map[string]bool{}
	for i, ref := range refs {
		if !validAuthorityPath(ref) {
			return fmt.Errorf("policyRefs[%d] が repository-relative path でない: %q", i, ref)
		}
		if seen[ref] {
			return fmt.Errorf("policyRefs[%d] が重複: %s", i, ref)
		}
		seen[ref] = true
	}
	return nil
}

func validateLineSet(name string, values []string) error {
	seen := map[string]bool{}
	for i, value := range values {
		if !validCanonicalLine(value) {
			return fmt.Errorf("%s[%d] が不正: %q", name, i, value)
		}
		if seen[value] {
			return fmt.Errorf("%s[%d] が重複: %s", name, i, value)
		}
		seen[value] = true
	}
	return nil
}
