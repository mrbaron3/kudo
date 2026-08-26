package contract

import (
	"bytes"
	"errors"
	"testing"
)

func TestClaimCheckpointCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	issue := IssueRef{Owner: "Acme", Repository: "Widgets", Number: 17}
	checkpoint := sampleClaimCheckpoint(t, issue)
	ref, payload, err := EncodeClaimCheckpoint(checkpoint)
	if err != nil {
		t.Fatalf("EncodeClaimCheckpoint() error = %v", err)
	}
	if ref.Schema != ClaimCheckpointSchemaV1Alpha1 || ref.Digest != payload.Digest {
		t.Fatalf("ref = %#v, payload digest = %s", ref, payload.Digest)
	}
	if payload.Kind != ArtifactKindClaimCheckpoint || payload.MediaType != MediaTypeJSON {
		t.Fatalf("payload metadata = %#v", payload)
	}
	if !bytes.Equal(payload.Data, []byte(`{"schema":"kudo.claim-checkpoint/v1alpha1","claimContext":{"compiler":"kudo.issue-compiler/v1alpha1","issueObservation":{"schema":"kudo.issue-observation/v1alpha1","digest":"sha256:1cd5921d3e4cf4dd3ff511fc56930e17143de917c18d6427a1aafc20ea1e4115"},"bodyDigest":"sha256:7016e77dc72f05f3dfe13a0b1dd3369d44ec7b30a4a7ac4b7b425d394222d085","taskContext":{"schema":"kudo.task-context/v1alpha1","digest":"sha256:0ebb429fa86d481c2630fac53db1c91cffed5d4d41d1021c179444eb67e7ee0b"},"contextManifest":{"schema":"kudo.context-manifest/v1alpha1","digest":"sha256:05b3abf2579a5eb66403cd78be557fd860633a1fe2103c7642030defe32c657f"},"baseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"executionPolicy":{"schema":"kudo.execution-policy/v1alpha1","digest":"sha256:a402c4336ebfd78227dea06341f394c60ab50d13d841cedf38df329afe4a304e"},"escalationPolicy":{"schema":"kudo.escalation-policy/v1alpha1","digest":"sha256:ac4b504daa20a3fde09e97f725b1b037dc7ef946b34504f1cc0a2ac558dd8087"}}`)) {
		t.Fatalf("canonical payload mismatch:\n%s", payload.Data)
	}

	decoded, err := ReadClaimCheckpointArtifact(ref, payload)
	if err != nil {
		t.Fatalf("ReadClaimCheckpointArtifact() error = %v", err)
	}
	if decoded != checkpoint {
		t.Fatalf("decoded = %#v, want %#v", decoded, checkpoint)
	}
}

func TestClaimCheckpointRejectsTamperingAndAmbiguity(t *testing.T) {
	t.Parallel()

	checkpoint := sampleClaimCheckpoint(t, IssueRef{Owner: "acme", Repository: "widgets", Number: 17})
	ref, payload, err := EncodeClaimCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}

	tampered := payload
	tampered.Data = append([]byte(nil), payload.Data...)
	tampered.Data[len(tampered.Data)-2] ^= 1
	if _, err := ReadClaimCheckpointArtifact(ref, tampered); err == nil {
		t.Fatal("tampered payload was accepted")
	}

	duplicate := payload
	duplicate.Data = bytes.Replace(payload.Data, []byte(`"schema":"kudo.claim-checkpoint/v1alpha1"`), []byte(`"schema":"kudo.claim-checkpoint/v1alpha1","schema":"kudo.claim-checkpoint/v1alpha1"`), 1)
	duplicate.Digest = SHA256(duplicate.Data)
	duplicateRef := ClaimCheckpointRef{Schema: ref.Schema, Digest: duplicate.Digest}
	if _, err := ReadClaimCheckpointArtifact(duplicateRef, duplicate); !errors.Is(err, ProtocolFieldDuplicate) {
		t.Fatalf("duplicate JSON field error = %v", err)
	}

	unknown := payload
	unknown.Data = bytes.Replace(payload.Data, []byte(`,"claimContext"`), []byte(`,"future":true,"claimContext"`), 1)
	unknown.Digest = SHA256(unknown.Data)
	unknownRef := ClaimCheckpointRef{Schema: ref.Schema, Digest: unknown.Digest}
	if _, err := ReadClaimCheckpointArtifact(unknownRef, unknown); err == nil {
		t.Fatal("unknown checkpoint field was accepted")
	}
}

func sampleClaimCheckpoint(t *testing.T, issue IssueRef) ClaimCheckpoint {
	t.Helper()
	body := "checkpoint body"
	observation := IssueObservation{
		Schema:     IssueObservationSchemaV1Alpha1,
		Issue:      issue.canonical(),
		BodyDigest: SHA256([]byte(body)),
	}
	context := ClaimContext{
		Compiler:        IssueCompilerVersionV1Alpha1,
		Observation:     IssueObservationRef{Schema: IssueObservationSchemaV1Alpha1, Digest: SHA256(encodeIssueObservation(observation))},
		BodyDigest:      observation.BodyDigest,
		TaskContext:     TaskContextRef{Schema: TaskContextSchemaV1Alpha1, Digest: SHA256([]byte("task"))},
		ContextManifest: ContextManifestRef{Schema: ContextManifestSchemaV1Alpha1, Digest: SHA256([]byte("manifest"))},
		BaseSHA:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	return ClaimCheckpoint{
		Schema:           ClaimCheckpointSchemaV1Alpha1,
		Context:          context,
		ExecutionPolicy:  ExecutionPolicyRef{Schema: ExecutionPolicySchemaV1Alpha1, Digest: SHA256([]byte("execution"))},
		EscalationPolicy: EscalationPolicyRef{Schema: EscalationPolicySchemaV1Alpha1, Digest: SHA256([]byte("escalation"))},
	}
}
