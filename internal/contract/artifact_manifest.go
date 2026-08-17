package contract

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ArtifactManifestSchemaV1Alpha1 は review へ渡す artifact 一覧の schema である。
const ArtifactManifestSchemaV1Alpha1 = "kudo.artifact-manifest/v1alpha1"

// ArtifactEntry は review が参照する 1 artifact を logical name で引けるようにする。
// Length と Digest は content-addressed store の write-once bytes を指す。
type ArtifactEntry struct {
	Name      string
	MediaType string
	Length    int64
	Digest    Digest
}

// ArtifactManifest は Review Request が指す immutable な artifact 集合である。
// bytes が変われば新しい digest と新しい manifest になり、以前の approval は再利用できない。
type ArtifactManifest struct {
	Schema  string
	Entries []ArtifactEntry
}

// ArtifactManifestRef は manifest schema と canonical artifact digest の組である。
type ArtifactManifestRef struct {
	Schema string
	Digest Digest
}

// NewArtifactEntry は payload から length、media type、digest を導出する。
// producer に自己申告させると bytes と manifest が食い違ったまま review へ渡るため、
// payload を持つ artifact はこの経路で entry 化する。
func NewArtifactEntry(name string, payload ArtifactPayload) (ArtifactEntry, error) {
	if !validArtifactName(name) {
		return ArtifactEntry{}, fmt.Errorf("artifact の logical name が不正: %q", name)
	}
	if err := payload.Validate(); err != nil {
		return ArtifactEntry{}, err
	}
	return ArtifactEntry{
		Name:      name,
		MediaType: payload.MediaType,
		Length:    int64(len(payload.Data)),
		Digest:    payload.Digest,
	}, nil
}

// EncodeArtifactManifest は manifest を検証し、canonical payload と ref を返す。
func EncodeArtifactManifest(manifest ArtifactManifest) (ArtifactManifestRef, ArtifactPayload, error) {
	if err := validateArtifactManifest(manifest); err != nil {
		return ArtifactManifestRef{}, ArtifactPayload{}, err
	}
	payload := newArtifactPayload(
		ArtifactKindArtifactManifest,
		ArtifactManifestSchemaV1Alpha1,
		MediaTypeYAML,
		encodeArtifactManifest(manifest),
	)
	return ArtifactManifestRef{Schema: ArtifactManifestSchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

func validateArtifactManifest(manifest ArtifactManifest) error {
	if manifest.Schema != ArtifactManifestSchemaV1Alpha1 {
		return fmt.Errorf("artifact manifest schema は %q でなければならない: %q",
			ArtifactManifestSchemaV1Alpha1, manifest.Schema)
	}
	if len(manifest.Entries) == 0 {
		return fmt.Errorf("artifact manifest が空")
	}
	seen := map[string]bool{}
	for i, entry := range manifest.Entries {
		if !validArtifactName(entry.Name) {
			return fmt.Errorf("entries[%d].name が不正: %q", i, entry.Name)
		}
		if seen[entry.Name] {
			return fmt.Errorf("entries[%d].name が重複: %s", i, entry.Name)
		}
		seen[entry.Name] = true
		if !validCanonicalLine(entry.MediaType) {
			return fmt.Errorf("entries[%d].mediaType が不正: %q", i, entry.MediaType)
		}
		if entry.Length < 0 {
			return fmt.Errorf("entries[%d].length が負", i)
		}
		if !entry.Digest.Valid() {
			return fmt.Errorf("entries[%d].digest が不正: %q", i, entry.Digest)
		}
	}
	return nil
}

// validArtifactName は logical name として許可する形を返す。manifest は name で
// 引く table であり、大文字小文字や空白の揺れで別 entry を作らせない。
//
// 文字種に加えて relative path として正規形であることを課す。この name は Issue Worker が
// 作り Review Worker が読む値であり、review 側は immutable snapshot を disposable checkout へ
// 展開する。name を展開先の名前に使う実装は自然に出てくるため、`..` や空 segment を
// 含む形を manifest の入口で拒否する。
func validArtifactName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '/', c == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	if path.Clean(name) != name {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// encodeArtifactManifest は entry を logical name 順に並べて encode する。
// manifest は name で引く table であり、producer が列挙した順序は identity ではない。
func encodeArtifactManifest(manifest ArtifactManifest) []byte {
	entries := append([]ArtifactEntry(nil), manifest.Entries...)
	slices.SortFunc(entries, func(a, b ArtifactEntry) int { return strings.Compare(a.Name, b.Name) })

	var b strings.Builder
	writeYAMLString(&b, 0, "schema", manifest.Schema)
	b.WriteString("entries:\n")
	for _, entry := range entries {
		b.WriteString("  - name: ")
		b.WriteString(yamlString(entry.Name))
		b.WriteByte('\n')
		writeYAMLString(&b, 4, "mediaType", entry.MediaType)
		// length は implicit type に依存させないため、他の scalar と同じく quote する
		writeYAMLString(&b, 4, "length", strconv.FormatInt(entry.Length, 10))
		writeYAMLString(&b, 4, "digest", string(entry.Digest))
	}
	return []byte(b.String())
}

// ReadArtifactManifestArtifact は ref/payload を照合して保存 bytes を返す。
func ReadArtifactManifestArtifact(ref ArtifactManifestRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, artifactManifestSchemaPrefix) {
		return nil, fmt.Errorf("ArtifactManifestRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindArtifactManifest, ref.Schema, ref.Digest, payload)
}
