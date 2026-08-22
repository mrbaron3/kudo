package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MarkerSchemaV1Alpha1       = "kudo.record-marker/v1alpha1"
	MachineBlockSchemaV1Alpha1 = "kudo.machine-block/v1alpha1"
	MaxRecordSurfaceBytes      = 64 * 1024

	markerPrefix  = "<!-- kudo-marker "
	machinePrefix = "<!-- kudo-machine "
	surfaceSuffix = " -->"
)

var (
	ErrInvalidRecordSurface  = errors.New("record surface が不正")
	ErrRecordSurfaceTooLarge = errors.New("record surface が 64KiB を超える")

	recordNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	shaPattern        = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Marker は comment/check run の冪等 identity を表す。Round と Head は
// 該当しない record では zero value にできるが、repository、Issue、kind、digest は必須である。
type Marker struct {
	Repository Repository
	Issue      int64
	Run        string
	Kind       string
	Round      int
	Head       string
	Digest     string
}

type MachineBlock struct {
	Kind      string
	MediaType string
	Digest    string
	Payload   []byte
}

type RecordSurface struct {
	Marker       Marker
	MachineBlock *MachineBlock
}

type markerEnvelope struct {
	Schema     string `json:"schema"`
	Repository string `json:"repository"`
	Issue      int64  `json:"issue"`
	Run        string `json:"run,omitempty"`
	Kind       string `json:"kind"`
	Round      int    `json:"round,omitempty"`
	Head       string `json:"head,omitempty"`
	Digest     string `json:"digest"`
}

type machineEnvelope struct {
	Schema    string `json:"schema"`
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Payload   string `json:"payload"`
}

// EncodeMarker は安定した field 順の一行 HTML comment を返す。
// digest の計算は行わず、internal/contract が渡した identity をそのまま包む。
func EncodeMarker(marker Marker) (string, error) {
	marker.Repository = marker.Repository.canonical()
	if err := validateMarker(marker); err != nil {
		return "", err
	}
	data, err := json.Marshal(markerEnvelope{
		Schema:     MarkerSchemaV1Alpha1,
		Repository: marker.Repository.String(),
		Issue:      marker.Issue,
		Run:        marker.Run,
		Kind:       marker.Kind,
		Round:      marker.Round,
		Head:       marker.Head,
		Digest:     marker.Digest,
	})
	if err != nil {
		return "", fmt.Errorf("%w: marker を encode する: %v", ErrInvalidRecordSurface, err)
	}
	return markerPrefix + string(data) + surfaceSuffix, nil
}

func ParseMarker(surface string) (Marker, error) {
	values, err := findEnvelopes(surface, markerPrefix)
	if err != nil {
		return Marker{}, err
	}
	if len(values) != 1 {
		return Marker{}, fmt.Errorf("%w: marker は1件必要（%d件）", ErrInvalidRecordSurface, len(values))
	}
	return decodeMarker(values[0])
}

func ParseMarkers(surface string) ([]Marker, error) {
	values, err := findEnvelopes(surface, markerPrefix)
	if err != nil {
		return nil, err
	}
	markers := make([]Marker, 0, len(values))
	for _, value := range values {
		marker, decodeErr := decodeMarker(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		markers = append(markers, marker)
	}
	return markers, nil
}

func EncodeMachineBlock(block MachineBlock) (string, error) {
	if err := validateMachineBlock(block); err != nil {
		return "", err
	}
	data, err := json.Marshal(machineEnvelope{
		Schema:    MachineBlockSchemaV1Alpha1,
		Kind:      block.Kind,
		MediaType: block.MediaType,
		Digest:    block.Digest,
		Payload:   base64.StdEncoding.EncodeToString(block.Payload),
	})
	if err != nil {
		return "", fmt.Errorf("%w: machine block を encode する: %v", ErrInvalidRecordSurface, err)
	}
	result := machinePrefix + string(data) + surfaceSuffix
	if len(result) > MaxRecordSurfaceBytes {
		return "", ErrRecordSurfaceTooLarge
	}
	return result, nil
}

func ParseMachineBlock(surface string) (MachineBlock, error) {
	values, err := findEnvelopes(surface, machinePrefix)
	if err != nil {
		return MachineBlock{}, err
	}
	if len(values) != 1 {
		return MachineBlock{}, fmt.Errorf("%w: machine block は1件必要（%d件）", ErrInvalidRecordSurface, len(values))
	}
	return decodeMachineBlock(values[0])
}

func RenderComment(body string, marker Marker, block *MachineBlock) (string, error) {
	if !utf8.ValidString(body) {
		return "", fmt.Errorf("%w: comment body は UTF-8 でなければならない", ErrInvalidRecordSurface)
	}
	if strings.Contains(body, markerPrefix) || strings.Contains(body, machinePrefix) {
		return "", fmt.Errorf("%w: comment body に予約済み record prefix を含められない", ErrInvalidRecordSurface)
	}
	markerText, err := EncodeMarker(marker)
	if err != nil {
		return "", err
	}
	record := markerText
	if block != nil {
		blockText, blockErr := EncodeMachineBlock(*block)
		if blockErr != nil {
			return "", blockErr
		}
		record += "\n" + blockText
	}
	if body != "" {
		record = body + "\n\n" + record
	}
	if len(record) > MaxRecordSurfaceBytes {
		return "", ErrRecordSurfaceTooLarge
	}
	return record, nil
}

// RenderCheckRunText は check run output.text に入れる machine-readable 部分を返す。
func RenderCheckRunText(marker Marker, block *MachineBlock) (string, error) {
	return RenderComment("", marker, block)
}

func ParseRecordSurface(surface string) (RecordSurface, error) {
	marker, err := ParseMarker(surface)
	if err != nil {
		return RecordSurface{}, err
	}
	blocks, err := findEnvelopes(surface, machinePrefix)
	if err != nil {
		return RecordSurface{}, err
	}
	result := RecordSurface{Marker: marker}
	if len(blocks) > 1 {
		return RecordSurface{}, fmt.Errorf("%w: machine block が複数ある", ErrInvalidRecordSurface)
	}
	if len(blocks) == 1 {
		block, decodeErr := decodeMachineBlock(blocks[0])
		if decodeErr != nil {
			return RecordSurface{}, decodeErr
		}
		result.MachineBlock = &block
	}
	return result, nil
}

func decodeMarker(data []byte) (Marker, error) {
	if err := rejectDuplicateJSONFields(data); err != nil {
		return Marker{}, err
	}
	var envelope markerEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Marker{}, fmt.Errorf("%w: marker JSON: %v", ErrInvalidRecordSurface, err)
	}
	if envelope.Schema != MarkerSchemaV1Alpha1 {
		return Marker{}, fmt.Errorf("%w: 未対応 marker schema %q", ErrInvalidRecordSurface, envelope.Schema)
	}
	repository, err := parseRepositoryRef(envelope.Repository)
	if err != nil {
		return Marker{}, err
	}
	marker := Marker{
		Repository: repository,
		Issue:      envelope.Issue,
		Run:        envelope.Run,
		Kind:       envelope.Kind,
		Round:      envelope.Round,
		Head:       envelope.Head,
		Digest:     envelope.Digest,
	}
	if err := validateMarker(marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func decodeMachineBlock(data []byte) (MachineBlock, error) {
	if err := rejectDuplicateJSONFields(data); err != nil {
		return MachineBlock{}, err
	}
	var envelope machineEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return MachineBlock{}, fmt.Errorf("%w: machine block JSON: %v", ErrInvalidRecordSurface, err)
	}
	if envelope.Schema != MachineBlockSchemaV1Alpha1 {
		return MachineBlock{}, fmt.Errorf("%w: 未対応 machine block schema %q", ErrInvalidRecordSurface, envelope.Schema)
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil {
		return MachineBlock{}, fmt.Errorf("%w: machine block payload: %v", ErrInvalidRecordSurface, err)
	}
	if base64.StdEncoding.EncodeToString(payload) != envelope.Payload {
		return MachineBlock{}, fmt.Errorf("%w: machine block payload が canonical base64 ではない", ErrInvalidRecordSurface)
	}
	block := MachineBlock{Kind: envelope.Kind, MediaType: envelope.MediaType, Digest: envelope.Digest, Payload: payload}
	if err := validateMachineBlock(block); err != nil {
		return MachineBlock{}, err
	}
	return block, nil
}

func validateMarker(marker Marker) error {
	if err := validateRepository(marker.Repository); err != nil {
		return fmt.Errorf("%w: marker repository: %v", ErrInvalidRecordSurface, err)
	}
	if marker.Issue <= 0 {
		return fmt.Errorf("%w: marker issue は正数でなければならない", ErrInvalidRecordSurface)
	}
	if marker.Run != "" && !identifierPattern.MatchString(marker.Run) {
		return fmt.Errorf("%w: marker run が不正", ErrInvalidRecordSurface)
	}
	if !recordNamePattern.MatchString(marker.Kind) {
		return fmt.Errorf("%w: marker kind が不正", ErrInvalidRecordSurface)
	}
	if marker.Round < 0 {
		return fmt.Errorf("%w: marker round は負にできない", ErrInvalidRecordSurface)
	}
	if marker.Head != "" && !shaPattern.MatchString(marker.Head) {
		return fmt.Errorf("%w: marker head が不正", ErrInvalidRecordSurface)
	}
	if !digestPattern.MatchString(marker.Digest) {
		return fmt.Errorf("%w: marker digest が不正", ErrInvalidRecordSurface)
	}
	return nil
}

func validateMachineBlock(block MachineBlock) error {
	if !recordNamePattern.MatchString(block.Kind) {
		return fmt.Errorf("%w: machine block kind が不正", ErrInvalidRecordSurface)
	}
	if block.MediaType == "" || len(block.MediaType) > 1024 {
		return fmt.Errorf("%w: machine block mediaType が不正", ErrInvalidRecordSurface)
	}
	if _, _, err := mime.ParseMediaType(block.MediaType); err != nil {
		return fmt.Errorf("%w: machine block mediaType: %v", ErrInvalidRecordSurface, err)
	}
	if !digestPattern.MatchString(block.Digest) {
		return fmt.Errorf("%w: machine block digest が不正", ErrInvalidRecordSurface)
	}
	return nil
}

func findEnvelopes(surface, prefix string) ([][]byte, error) {
	var values [][]byte
	remaining := surface
	for {
		start := strings.Index(remaining, prefix)
		if start < 0 {
			return values, nil
		}
		jsonStart := start + len(prefix)
		end := strings.Index(remaining[jsonStart:], surfaceSuffix)
		if end < 0 {
			return nil, fmt.Errorf("%w: record comment が閉じていない", ErrInvalidRecordSurface)
		}
		end += jsonStart
		values = append(values, []byte(remaining[jsonStart:end]))
		remaining = remaining[end+len(surfaceSuffix):]
	}
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: JSON object が必要", ErrInvalidRecordSurface)
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return fmt.Errorf("%w: JSON key: %v", ErrInvalidRecordSurface, keyErr)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("%w: JSON key が文字列ではない", ErrInvalidRecordSurface)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: JSON field %q が重複している", ErrInvalidRecordSurface, key)
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("%w: JSON field %q: %v", ErrInvalidRecordSurface, key, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("%w: JSON object: %v", ErrInvalidRecordSurface, err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return fmt.Errorf("%w: JSON object の後に余分な値がある", ErrInvalidRecordSurface)
	}
	return nil
}

func parseRepositoryRef(value string) (Repository, error) {
	const prefix = "github://"
	if !strings.HasPrefix(value, prefix) {
		return Repository{}, fmt.Errorf("%w: repository ref が不正", ErrInvalidRecordSurface)
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "/")
	if len(parts) != 2 {
		return Repository{}, fmt.Errorf("%w: repository ref が不正", ErrInvalidRecordSurface)
	}
	repository := Repository{Owner: parts[0], Name: parts[1]}.canonical()
	if err := validateRepository(repository); err != nil {
		return Repository{}, fmt.Errorf("%w: repository ref: %v", ErrInvalidRecordSurface, err)
	}
	return repository, nil
}
