package contract

import (
	"bytes"
	"errors"
	"testing"
)

func sampleEscalationPolicy() EscalationPolicy {
	return EscalationPolicy{
		Schema: EscalationPolicySchemaV1Alpha1,
		ReviewRounds: ReviewRoundLimits{
			TestValidity:        3,
			FinalImplementation: 3,
		},
	}
}

// Escalation Policy は Controller が Run へ固定する gate 予算であり、同じ値からは
// 常に同じ content identity を得なければならない。digest が揺れると、escalation
// comment が引用する「この Run に固定された上限」の根拠が確定しない。
func TestEscalationPolicyCanonicalIdentity(t *testing.T) {
	first, firstPayload, err := EncodeEscalationPolicy(sampleEscalationPolicy())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, secondPayload, err := EncodeEscalationPolicy(sampleEscalationPolicy())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if first != second || !bytes.Equal(firstPayload.Data, secondPayload.Data) {
		t.Fatalf("同じ policy が別 identity になった: %+v / %+v", first, second)
	}
	if first.Schema != EscalationPolicySchemaV1Alpha1 {
		t.Fatalf("ref schema = %q, want %q", first.Schema, EscalationPolicySchemaV1Alpha1)
	}
	if err := firstPayload.Validate(); err != nil {
		t.Fatalf("payload の自己検証: %v", err)
	}

	changed := sampleEscalationPolicy()
	changed.ReviewRounds.FinalImplementation = 4
	changedRef, _, err := EncodeEscalationPolicy(changed)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if changedRef == first {
		t.Fatal("上限を変えても ref が変わらない")
	}
}

// Escalation Policy と Execution Policy は別の schema identity を持たなければならない。
// 同一視できると、gate 予算の変更が Execution Policy ref の変更として観測され、
// InputIdentity 経由で進行中 Run を supersede しうる。
func TestEscalationPolicySchemaIsIndependent(t *testing.T) {
	if EscalationPolicySchemaV1Alpha1 == ExecutionPolicySchemaV1Alpha1 {
		t.Fatal("Escalation Policy が Execution Policy と schema identity を共有")
	}
	ref, payload, err := EncodeEscalationPolicy(sampleEscalationPolicy())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if payload.Kind != ArtifactKindEscalationPolicy {
		t.Fatalf("artifact kind = %q, want %q", payload.Kind, ArtifactKindEscalationPolicy)
	}
	if _, err := ReadExecutionPolicyArtifact(ExecutionPolicyRef(ref), payload); err == nil {
		t.Fatal("Execution Policy reader が Escalation Policy artifact を受理した")
	}
}

// 範囲は deployment 判断ではなく protocol core が固定する。0 は「常に即 escalate」、
// 過大な値は事実上の無制限であり、どちらも gate の意味を失わせる。
func TestEscalationPolicyValidation(t *testing.T) {
	tests := map[string]struct {
		mutate func(*EscalationPolicy)
		code   ProtocolCode
	}{
		"schema":      {func(p *EscalationPolicy) { p.Schema = "kudo.escalation-policy/v2" }, ProtocolSchemaUnknown},
		"schema 空":    {func(p *EscalationPolicy) { p.Schema = "" }, ProtocolSchemaUnknown},
		"test 上限 0":   {func(p *EscalationPolicy) { p.ReviewRounds.TestValidity = 0 }, ProtocolFieldInvalid},
		"test 上限 負":   {func(p *EscalationPolicy) { p.ReviewRounds.TestValidity = -1 }, ProtocolFieldInvalid},
		"final 上限 0":  {func(p *EscalationPolicy) { p.ReviewRounds.FinalImplementation = 0 }, ProtocolFieldInvalid},
		"test 上限 超過":  {func(p *EscalationPolicy) { p.ReviewRounds.TestValidity = MaxReviewRounds + 1 }, ProtocolFieldInvalid},
		"final 上限 超過": {func(p *EscalationPolicy) { p.ReviewRounds.FinalImplementation = MaxReviewRounds + 1 }, ProtocolFieldInvalid},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := sampleEscalationPolicy()
			test.mutate(&got)
			_, _, err := EncodeEscalationPolicy(got)
			if err == nil {
				t.Fatal("invalid policy を受理した")
			}
			if !errors.Is(err, test.code) {
				t.Fatalf("code = %v, want %v", err, test.code)
			}
		})
	}
}

// 上限ちょうどの値は受理する。canonical text の上限規則と同じ扱いにする。
func TestEscalationPolicyAcceptsBoundaryLimits(t *testing.T) {
	for _, rounds := range []int{MinReviewRounds, MaxReviewRounds} {
		policy := sampleEscalationPolicy()
		policy.ReviewRounds = ReviewRoundLimits{TestValidity: rounds, FinalImplementation: rounds}
		if _, _, err := EncodeEscalationPolicy(policy); err != nil {
			t.Fatalf("上限 %d を拒否した: %v", rounds, err)
		}
	}
}

func TestEscalationPolicyArtifactBinding(t *testing.T) {
	ref, payload, err := EncodeEscalationPolicy(sampleEscalationPolicy())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := ReadEscalationPolicyArtifact(ref, payload)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload.Data) {
		t.Fatal("read が保存 bytes を返さない")
	}

	tampered := payload
	tampered.Data = append([]byte(nil), payload.Data...)
	tampered.Data[0] = 'X'
	if _, err := ReadEscalationPolicyArtifact(ref, tampered); err == nil {
		t.Fatal("改変 payload を受理した")
	}

	// reader は bytes を再 encode せず ref と照合するだけなので、進行中 Run が
	// 参照する旧 schema の artifact も同じ bytes のまま読み出せる。
	futureBytes := []byte("schema: \"kudo.escalation-policy/v1beta1\"\nnewBudget: true\n")
	futurePayload := newArtifactPayload(
		ArtifactKindEscalationPolicy,
		"kudo.escalation-policy/v1beta1",
		MediaTypeYAML,
		futureBytes,
	)
	futureRef := EscalationPolicyRef{Schema: futurePayload.Schema, Digest: futurePayload.Digest}
	futureGot, err := ReadEscalationPolicyArtifact(futureRef, futurePayload)
	if err != nil {
		t.Fatalf("future schema artifact の opaque read: %v", err)
	}
	if !bytes.Equal(futureGot, futureBytes) {
		t.Fatal("future schema artifact が再 encode された")
	}

	wrongSchema := futureRef
	wrongSchema.Schema = EscalationPolicySchemaV1Alpha1
	if _, err := ReadEscalationPolicyArtifact(wrongSchema, futurePayload); err == nil {
		t.Fatal("schema と digest の組を崩した ref を受理した")
	}
}
