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
	issueObservationSchemaPrefix       = "kudo.issue-observation/"
	taskContextSchemaPrefix            = "kudo.task-context/"
	contextManifestSchemaPrefix        = "kudo.context-manifest/"
	executionPolicySchemaPrefix        = "kudo.execution-policy/"
	escalationPolicySchemaPrefix       = "kudo.escalation-policy/"
	artifactManifestSchemaPrefix       = "kudo.artifact-manifest/"
	pullRequestObservationSchemaPrefix = "kudo.pull-request-observation/"
)

// validProtocolID は Operation、Run、attempt、finding 等の identifier を検証する。
// canonical bytes と database key の両方へ載るため、空白・control character・
// 長大な値を受け付けない。
//
// 先頭は英数字に限る。Run は workspace を持つため identifier は path segment へも
// 載りうるが、`.` や `..` は protocol 層を通ってから filesystem 層で弾かれる（あるいは
// 弾かれない）ことになる。信頼境界で拒否して、失敗を保存段階まで遅らせない。
func validProtocolID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// canonical text の byte 上限。上限は受理可否だけに影響し、canonical bytes と digest の
// 計算方法は変えない。
//
// 上限を置くのは、これらの値が model provider の出力に由来し、canonical bytes と
// PostgreSQL text の両方へそのまま載るためである。上限が無いと、単一の attempt が
// row size と digest 計算量を通じて後続の全 Operation へ影響しうる。
const (
	// MaxCanonicalTextBytes は evidence、finding の expected / observed など複数行本文の
	// 上限である。失敗した test 出力や diff hunk の抜粋を収めるには十分広く、単一 attempt が
	// 保存層を圧迫するには狭い水準として 64 KiB を採る。
	MaxCanonicalTextBytes = 64 << 10
	// MaxCanonicalLineBytes は finding summary、media type、external ref、
	// repository-relative path など単一行の値の上限である。
	MaxCanonicalLineBytes = 1 << 10
)

// validCanonicalText は canonical YAML と PostgreSQL text へそのまま載せられる本文かを返す。
// LF と TAB だけを構造として許可する。単独 CR を含む他の control character は、
// Issue body と同じ理由（保存段階まで失敗を遅らせない）で信頼境界で拒否する。
//
// maxBytes を引数に取るのは、この述語が protocol 層と Issue Contract の authority path の
// 両方から呼ばれ、妥当な上限が呼び出し側ごとに違うためである。既定値を持たせると、
// 新しい呼び出し側が上限を意識しないまま無関係な水準を継承する。
func validCanonicalText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && validCanonicalTextFormat(value)
}

func validCanonicalTextFormat(value string) bool {
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
func validCanonicalLine(value string, maxBytes int) bool {
	return len(value) <= maxBytes && validCanonicalLineFormat(value)
}

func validCanonicalLineFormat(value string) bool {
	return validCanonicalTextFormat(value) && !strings.ContainsAny(value, "\n\t")
}

// canonicalTextCode は拒否理由を上限超過とその他の形式違反へ分ける。
// 上限超過は producer 側が本文を切り詰めれば通る失敗であり、control character 混入や
// 空文字とは対処が違う。同じ code へ潰すと、Controller は両者を区別できない。
func canonicalTextCode(value string, maxBytes int) ProtocolCode {
	if validCanonicalTextFormat(value) && len(value) > maxBytes {
		return ProtocolFieldTooLong
	}
	return ProtocolFieldInvalid
}

func canonicalLineCode(value string, maxBytes int) ProtocolCode {
	if validCanonicalLineFormat(value) && len(value) > maxBytes {
		return ProtocolFieldTooLong
	}
	return ProtocolFieldInvalid
}

func validateVersionedRef(name, schema string, digest Digest, prefix string) error {
	if !validSchemaIdentity(schema, prefix) {
		return protocolSchemaErr(name, schema, "schema が不正: %q", schema)
	}
	if digest == "" {
		return protocolErr(ProtocolFieldMissing, name, "digest が空")
	}
	if !digest.Valid() {
		return protocolErr(ProtocolFieldInvalid, name, "digest が不正: %q", digest)
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
		field := fmt.Sprintf("%s[%d]", name, i)
		if !digest.Valid() {
			return protocolErr(ProtocolFieldInvalid, field, "digest が不正: %q", digest)
		}
		if seen[digest] {
			return protocolErr(ProtocolFieldDuplicate, field, "digest が重複: %s", digest)
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
		field := fmt.Sprintf("policyRefs[%d]", i)
		if !validProtocolAuthorityPath(ref) {
			return protocolErr(protocolAuthorityPathCode(ref), field,
				"repository-relative path でない: %q", ref)
		}
		if seen[ref] {
			return protocolErr(ProtocolFieldDuplicate, field, "policy ref が重複: %s", ref)
		}
		seen[ref] = true
	}
	return nil
}

func validateLineSet(name string, values []string) error {
	seen := map[string]bool{}
	for i, value := range values {
		field := fmt.Sprintf("%s[%d]", name, i)
		if !validCanonicalLine(value, MaxCanonicalLineBytes) {
			return protocolErr(canonicalLineCode(value, MaxCanonicalLineBytes), field,
				"canonical な単一行でない")
		}
		if seen[value] {
			return protocolErr(ProtocolFieldDuplicate, field, "値が重複: %s", value)
		}
		seen[value] = true
	}
	return nil
}
