package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ClaimCheckpointSchemaV1Alpha1 は draft Pull Request body に固定する
// claim checkpoint payload の schema である。
const ClaimCheckpointSchemaV1Alpha1 = "kudo.claim-checkpoint/v1alpha1"

// ClaimCheckpoint は Issue Worker が作った live context identity と、Controller が
// deployment configuration から解決した policy identity を一つの durable record に束縛する。
// canonical Issue payload や policy payload 自体は保持しない。
type ClaimCheckpoint struct {
	Schema           string
	Context          ClaimContext
	ExecutionPolicy  ExecutionPolicyRef
	EscalationPolicy EscalationPolicyRef
}

type ClaimCheckpointRef struct {
	Schema string
	Digest Digest
}

func (r ClaimCheckpointRef) Valid() bool {
	return validSchemaIdentity(r.Schema, claimCheckpointSchemaPrefix) && r.Digest.Valid()
}

type checkpointRefEnvelope struct {
	Schema string `json:"schema"`
	Digest Digest `json:"digest"`
}

type claimContextEnvelope struct {
	Compiler        string                `json:"compiler"`
	Observation     checkpointRefEnvelope `json:"issueObservation"`
	BodyDigest      Digest                `json:"bodyDigest"`
	TaskContext     checkpointRefEnvelope `json:"taskContext"`
	ContextManifest checkpointRefEnvelope `json:"contextManifest"`
	BaseSHA         string                `json:"baseSha"`
}

type claimCheckpointEnvelope struct {
	Schema           string                `json:"schema"`
	Context          claimContextEnvelope  `json:"claimContext"`
	ExecutionPolicy  checkpointRefEnvelope `json:"executionPolicy"`
	EscalationPolicy checkpointRefEnvelope `json:"escalationPolicy"`
}

// EncodeClaimCheckpoint は checkpoint を検証し、field 順が固定された JSON payload を返す。
// JSON は GitHub machine block から標準 library だけで strict に復元するために使い、
// Task Context 系の canonical YAML schema を置き換えるものではない。
func EncodeClaimCheckpoint(checkpoint ClaimCheckpoint) (ClaimCheckpointRef, ArtifactPayload, error) {
	if err := validateClaimCheckpoint(checkpoint); err != nil {
		return ClaimCheckpointRef{}, ArtifactPayload{}, err
	}
	data, err := json.Marshal(claimCheckpointToEnvelope(checkpoint))
	if err != nil {
		return ClaimCheckpointRef{}, ArtifactPayload{}, protocolErr(ProtocolFieldInvalid, "", "claim checkpoint を encode できない: %v", err)
	}
	payload := newArtifactPayload(
		ArtifactKindClaimCheckpoint,
		ClaimCheckpointSchemaV1Alpha1,
		MediaTypeJSON,
		data,
	)
	return ClaimCheckpointRef{Schema: ClaimCheckpointSchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

func validateClaimCheckpoint(checkpoint ClaimCheckpoint) error {
	if checkpoint.Schema != ClaimCheckpointSchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"claim checkpoint schema は %q でなければならない: %q",
			ClaimCheckpointSchemaV1Alpha1, checkpoint.Schema)
	}
	if err := ValidateClaimContext(checkpoint.Context); err != nil {
		return err
	}
	if !checkpoint.ExecutionPolicy.Valid() {
		return protocolErr(ProtocolFieldInvalid, "executionPolicy", "Execution Policy ref が不正")
	}
	if !checkpoint.EscalationPolicy.Valid() {
		return protocolErr(ProtocolFieldInvalid, "escalationPolicy", "Escalation Policy ref が不正")
	}
	return nil
}

func claimCheckpointToEnvelope(checkpoint ClaimCheckpoint) claimCheckpointEnvelope {
	return claimCheckpointEnvelope{
		Schema: checkpoint.Schema,
		Context: claimContextEnvelope{
			Compiler:        checkpoint.Context.Compiler,
			Observation:     checkpointRefEnvelope{Schema: checkpoint.Context.Observation.Schema, Digest: checkpoint.Context.Observation.Digest},
			BodyDigest:      checkpoint.Context.BodyDigest,
			TaskContext:     checkpointRefEnvelope{Schema: checkpoint.Context.TaskContext.Schema, Digest: checkpoint.Context.TaskContext.Digest},
			ContextManifest: checkpointRefEnvelope{Schema: checkpoint.Context.ContextManifest.Schema, Digest: checkpoint.Context.ContextManifest.Digest},
			BaseSHA:         checkpoint.Context.BaseSHA,
		},
		ExecutionPolicy:  checkpointRefEnvelope{Schema: checkpoint.ExecutionPolicy.Schema, Digest: checkpoint.ExecutionPolicy.Digest},
		EscalationPolicy: checkpointRefEnvelope{Schema: checkpoint.EscalationPolicy.Schema, Digest: checkpoint.EscalationPolicy.Digest},
	}
}

// ReadClaimCheckpointArtifact は machine block から得た payload の ref、metadata、bytes を
// 照合し、unknown/duplicate field を受理せず typed checkpoint を復元する。
func ReadClaimCheckpointArtifact(ref ClaimCheckpointRef, payload ArtifactPayload) (ClaimCheckpoint, error) {
	if !validSchemaIdentity(ref.Schema, claimCheckpointSchemaPrefix) {
		return ClaimCheckpoint{}, protocolSchemaErr("schema", ref.Schema, "ClaimCheckpointRef schema が不正: %q", ref.Schema)
	}
	data, err := readVersionedArtifact(ArtifactKindClaimCheckpoint, ref.Schema, ref.Digest, payload)
	if err != nil {
		return ClaimCheckpoint{}, err
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return ClaimCheckpoint{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope claimCheckpointEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return ClaimCheckpoint{}, protocolErr(ProtocolFieldInvalid, "", "claim checkpoint JSON が不正: %v", err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return ClaimCheckpoint{}, protocolErr(ProtocolFieldInvalid, "", "claim checkpoint JSON の後に余分な値がある")
	}
	checkpoint := ClaimCheckpoint{
		Schema: envelope.Schema,
		Context: ClaimContext{
			Compiler:        envelope.Context.Compiler,
			Observation:     IssueObservationRef{Schema: envelope.Context.Observation.Schema, Digest: envelope.Context.Observation.Digest},
			BodyDigest:      envelope.Context.BodyDigest,
			TaskContext:     TaskContextRef{Schema: envelope.Context.TaskContext.Schema, Digest: envelope.Context.TaskContext.Digest},
			ContextManifest: ContextManifestRef{Schema: envelope.Context.ContextManifest.Schema, Digest: envelope.Context.ContextManifest.Digest},
			BaseSHA:         envelope.Context.BaseSHA,
		},
		ExecutionPolicy:  ExecutionPolicyRef{Schema: envelope.ExecutionPolicy.Schema, Digest: envelope.ExecutionPolicy.Digest},
		EscalationPolicy: EscalationPolicyRef{Schema: envelope.EscalationPolicy.Schema, Digest: envelope.EscalationPolicy.Digest},
	}
	if err := validateClaimCheckpoint(checkpoint); err != nil {
		return ClaimCheckpoint{}, err
	}
	return checkpoint, nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("JSON object key が文字列でない")
				}
				if _, exists := seen[key]; exists {
					return protocolErr(ProtocolFieldDuplicate, key, "claim checkpoint JSON field が重複")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	if err := walk(); err != nil {
		if _, ok := ProtocolViolation(err); ok {
			return err
		}
		return protocolErr(ProtocolFieldInvalid, "", "claim checkpoint JSON が不正: %v", err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return protocolErr(ProtocolFieldInvalid, "", "claim checkpoint JSON の後に余分な値がある")
	}
	return nil
}
