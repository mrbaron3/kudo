package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	ArtifactKindRawIssueBody     ArtifactKind = "raw-issue-body"
	ArtifactKindIssueObservation ArtifactKind = "issue-observation"
	ArtifactKindTaskContext      ArtifactKind = "task-context"
	ArtifactKindContextManifest  ArtifactKind = "context-manifest"
	ArtifactKindExecutionPolicy  ArtifactKind = "execution-policy"
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

// Validate は metadata と content bytes の binding を検証する。
func (p ArtifactPayload) Validate() error {
	switch p.Kind {
	case ArtifactKindRawIssueBody:
		if p.Schema != "" {
			return errors.New("raw Issue body artifact に schema は指定しない")
		}
		if p.MediaType != MediaTypeMarkdown {
			return fmt.Errorf("raw Issue body media type が不正: %q", p.MediaType)
		}
	case ArtifactKindIssueObservation:
		if !validSchemaIdentity(p.Schema, "kudo.issue-observation/") {
			return fmt.Errorf("Issue Observation artifact schema が不正: %q", p.Schema)
		}
		if p.MediaType != MediaTypeYAML {
			return fmt.Errorf("Issue Observation media type が不正: %q", p.MediaType)
		}
	case ArtifactKindTaskContext:
		if !validSchemaIdentity(p.Schema, "kudo.task-context/") {
			return fmt.Errorf("Task Context artifact schema が不正: %q", p.Schema)
		}
		if p.MediaType != MediaTypeYAML {
			return fmt.Errorf("Task Context media type が不正: %q", p.MediaType)
		}
	case ArtifactKindContextManifest:
		if !validSchemaIdentity(p.Schema, "kudo.context-manifest/") {
			return fmt.Errorf("Context Manifest artifact schema が不正: %q", p.Schema)
		}
		if p.MediaType != MediaTypeYAML {
			return fmt.Errorf("Context Manifest media type が不正: %q", p.MediaType)
		}
	case ArtifactKindExecutionPolicy:
		if !validSchemaIdentity(p.Schema, "kudo.execution-policy/") {
			return fmt.Errorf("Execution Policy artifact schema が不正: %q", p.Schema)
		}
		if p.MediaType != MediaTypeYAML {
			return fmt.Errorf("Execution Policy media type が不正: %q", p.MediaType)
		}
	default:
		return fmt.Errorf("artifact kind が不正: %q", p.Kind)
	}
	if !p.Digest.Valid() {
		return fmt.Errorf("artifact digest が不正: %q", p.Digest)
	}
	if got := SHA256(p.Data); got != p.Digest {
		return fmt.Errorf("artifact digest mismatch: got %s, want %s", got, p.Digest)
	}
	return nil
}

// readVersionedArtifact は ref と payload を bytes 単位で照合して clone を返す。
// canonical bytes を decode/re-encode しないため、active Run が参照する旧 schema の
// artifact も同じ bytes のまま読み出せる。
func readVersionedArtifact(kind ArtifactKind, schema string, digest Digest, payload ArtifactPayload) ([]byte, error) {
	if schema == "" {
		return nil, errors.New("artifact ref schema が空")
	}
	if !digest.Valid() {
		return nil, fmt.Errorf("artifact ref digest が不正: %q", digest)
	}
	if err := payload.Validate(); err != nil {
		return nil, err
	}
	if payload.Kind != kind {
		return nil, fmt.Errorf("artifact kind mismatch: got %q, want %q", payload.Kind, kind)
	}
	if payload.Schema != schema {
		return nil, fmt.Errorf("artifact schema mismatch: got %q, want %q", payload.Schema, schema)
	}
	if payload.Digest != digest {
		return nil, fmt.Errorf("artifact ref mismatch: got %s, want %s", payload.Digest, digest)
	}
	return append([]byte(nil), payload.Data...), nil
}
