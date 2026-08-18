package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Digest は content bytes の SHA-256 identity を表す。
// String form は常に sha256:<lowercase hex> とする。
type Digest string

// SHA256 は bytes の content digest を返す。
func SHA256(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// Valid は digest が canonical な SHA-256 string かを返す。
func (d Digest) Valid() bool {
	s := string(d)
	if !strings.HasPrefix(s, "sha256:") || len(s) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	return err == nil && s == strings.ToLower(s)
}

// ArtifactKind は payload の論理的な用途を表す。content address そのものは Digest である。
type ArtifactKind string

const (
	ArtifactKindRawIssueBody           ArtifactKind = "raw-issue-body"
	ArtifactKindIssueObservation       ArtifactKind = "issue-observation"
	ArtifactKindTaskContext            ArtifactKind = "task-context"
	ArtifactKindContextManifest        ArtifactKind = "context-manifest"
	ArtifactKindExecutionPolicy        ArtifactKind = "execution-policy"
	ArtifactKindArtifactManifest       ArtifactKind = "artifact-manifest"
	ArtifactKindPullRequestObservation ArtifactKind = "pull-request-observation"
)

const (
	MediaTypeMarkdown = "text/markdown; charset=utf-8"
	MediaTypeYAML     = "application/yaml; charset=utf-8"
)

// ArtifactPayload は content-addressed Artifact Store へ渡す write-once payload である。
// Schema は raw Issue body のような schema-less payload では空にする。
// Data を変更した場合 Validate は失敗するため、producer は作成後に変更しない。
type ArtifactPayload struct {
	Kind      ArtifactKind
	Schema    string
	MediaType string
	Digest    Digest
	Data      []byte
}

func newArtifactPayload(kind ArtifactKind, schema, mediaType string, data []byte) ArtifactPayload {
	owned := append([]byte(nil), data...)
	return ArtifactPayload{
		Kind:      kind,
		Schema:    schema,
		MediaType: mediaType,
		Digest:    SHA256(owned),
		Data:      owned,
	}
}

// artifactKindRule は kind ごとの schema namespace と media type を固定する。
// schemaPrefix が空の kind は versioned schema を持たない。
type artifactKindRule struct {
	schemaPrefix string
	mediaType    string
}

var artifactKindRules = map[ArtifactKind]artifactKindRule{
	ArtifactKindRawIssueBody:           {mediaType: MediaTypeMarkdown},
	ArtifactKindIssueObservation:       {schemaPrefix: issueObservationSchemaPrefix, mediaType: MediaTypeYAML},
	ArtifactKindTaskContext:            {schemaPrefix: taskContextSchemaPrefix, mediaType: MediaTypeYAML},
	ArtifactKindContextManifest:        {schemaPrefix: contextManifestSchemaPrefix, mediaType: MediaTypeYAML},
	ArtifactKindExecutionPolicy:        {schemaPrefix: executionPolicySchemaPrefix, mediaType: MediaTypeYAML},
	ArtifactKindArtifactManifest:       {schemaPrefix: artifactManifestSchemaPrefix, mediaType: MediaTypeYAML},
	ArtifactKindPullRequestObservation: {schemaPrefix: pullRequestObservationSchemaPrefix, mediaType: MediaTypeYAML},
}

// Validate は metadata と content bytes の binding を検証する。
func (p ArtifactPayload) Validate() error {
	rule, ok := artifactKindRules[p.Kind]
	if !ok {
		return protocolErr(ProtocolKindUnknown, "kind", "artifact kind が不正: %q", p.Kind)
	}
	switch {
	case rule.schemaPrefix == "" && p.Schema != "":
		return protocolErr(ProtocolKindConstraint, "schema",
			"kind %q は versioned schema を持たない: %q", p.Kind, p.Schema)
	case rule.schemaPrefix != "" && !validSchemaIdentity(p.Schema, rule.schemaPrefix):
		return protocolSchemaErr("schema", p.Schema,
			"kind %q の artifact schema が不正: %q", p.Kind, p.Schema)
	}
	if p.MediaType != rule.mediaType {
		return protocolErr(ProtocolFieldInvalid, "mediaType",
			"kind %q の media type は %q でなければならない: %q", p.Kind, rule.mediaType, p.MediaType)
	}
	if !p.Digest.Valid() {
		return protocolErr(ProtocolFieldInvalid, "digest", "artifact digest が不正: %q", p.Digest)
	}
	if got := SHA256(p.Data); got != p.Digest {
		return protocolErr(ProtocolIdentityMismatch, "digest",
			"artifact digest が bytes と一致しない: got %s, want %s", got, p.Digest)
	}
	return nil
}

// readVersionedArtifact は ref と payload を bytes 単位で照合して clone を返す。
// canonical bytes を decode/re-encode しないため、active Run が参照する旧 schema の
// artifact も同じ bytes のまま読み出せる。
func readVersionedArtifact(kind ArtifactKind, schema string, digest Digest, payload ArtifactPayload) ([]byte, error) {
	if schema == "" {
		return nil, protocolErr(ProtocolFieldMissing, "schema", "artifact ref schema が空")
	}
	if digest == "" {
		return nil, protocolErr(ProtocolFieldMissing, "digest", "artifact ref digest が空")
	}
	if !digest.Valid() {
		return nil, protocolErr(ProtocolFieldInvalid, "digest", "artifact ref digest が不正: %q", digest)
	}
	if err := payload.Validate(); err != nil {
		return nil, err
	}
	if payload.Kind != kind {
		return nil, protocolErr(ProtocolIdentityMismatch, "kind",
			"artifact kind が ref と一致しない: got %q, want %q", payload.Kind, kind)
	}
	if payload.Schema != schema {
		return nil, protocolErr(ProtocolIdentityMismatch, "schema",
			"artifact schema が ref と一致しない: got %q, want %q", payload.Schema, schema)
	}
	if payload.Digest != digest {
		return nil, protocolErr(ProtocolIdentityMismatch, "digest",
			"artifact digest が ref と一致しない: got %s, want %s", payload.Digest, digest)
	}
	return append([]byte(nil), payload.Data...), nil
}
