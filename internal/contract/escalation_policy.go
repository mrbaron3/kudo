package contract

import (
	"strconv"
	"strings"
)

// EscalationPolicySchemaV1Alpha1 は claim 時に Run へ固定する escalation policy schema である。
const EscalationPolicySchemaV1Alpha1 = "kudo.escalation-policy/v1alpha1"

// review round 上限の許容範囲は protocol core が固定する。値そのものは deployment 判断だが、
// 0 は「最初の request_changes で必ず escalate」、過大な値は事実上の無制限であり、
// どちらも gate としての意味を失わせるため範囲は configurable にしない。
const (
	MinReviewRounds = 1
	MaxReviewRounds = 10
)

// attempt retry 上限も review round と同じ bounded な deployment budget として扱う。
// AttemptRetries は初回実行を含めず、失敗後に作成できる追加 Attempt 数を表す。
const (
	DefaultAttemptRetries = 3
	MinAttemptRetries     = 1
	MaxAttemptRetries     = 10
)

// ReviewRoundLimits は review gate ごとに自動修正 loop を続ける round 数の上限である。
//
// gate ごとに独立した値を持つ。test validity と final implementation は失敗理由も
// 修正 Operation も異なる独立した収束過程であり、通算にすると片方の gate が荒れただけで
// もう片方の予算を失う。
type ReviewRoundLimits struct {
	TestValidity        int
	FinalImplementation int
}

// EscalationPolicy は Controller が自動継続をやめて人間へ渡すまでの予算を Run へ固定する。
//
// Execution Policy と分けているのは、Execution Policy ref が Run の semantic input identity
// の一部であり、変化が既存 Operation / review を stale にするためである。retry / round 上限は
// Controller の自動継続判断だけに使い、Worker / reviewer の判断入力にはしない。値を変えただけで
// 進行中 Run を supersede させてはならない。詳細は
// docs/adr/0003-review-round-limit.md を参照する。
type EscalationPolicy struct {
	Schema         string
	AttemptRetries int
	ReviewRounds   ReviewRoundLimits
}

// EscalationPolicyRef は policy schema と canonical artifact digest の組である。
type EscalationPolicyRef struct {
	Schema string
	Digest Digest
}

// EncodeEscalationPolicy は policy を検証し、canonical payload と ref を返す。
func EncodeEscalationPolicy(policy EscalationPolicy) (EscalationPolicyRef, ArtifactPayload, error) {
	if err := validateEscalationPolicy(policy); err != nil {
		return EscalationPolicyRef{}, ArtifactPayload{}, err
	}
	payload := newArtifactPayload(
		ArtifactKindEscalationPolicy,
		EscalationPolicySchemaV1Alpha1,
		MediaTypeYAML,
		encodeEscalationPolicy(policy),
	)
	return EscalationPolicyRef{Schema: EscalationPolicySchemaV1Alpha1, Digest: payload.Digest}, payload, nil
}

func validateEscalationPolicy(policy EscalationPolicy) error {
	if policy.Schema != EscalationPolicySchemaV1Alpha1 {
		return protocolErr(ProtocolSchemaUnknown, "schema",
			"escalation policy schema は %q でなければならない: %q", EscalationPolicySchemaV1Alpha1, policy.Schema)
	}
	if policy.AttemptRetries < MinAttemptRetries || policy.AttemptRetries > MaxAttemptRetries {
		return protocolErr(ProtocolFieldInvalid, "attemptRetries",
			"attempt retry 上限は %d 以上 %d 以下でなければならない: %d",
			MinAttemptRetries, MaxAttemptRetries, policy.AttemptRetries)
	}
	limits := []struct {
		field string
		value int
	}{
		{"reviewRounds.testValidity", policy.ReviewRounds.TestValidity},
		{"reviewRounds.finalImplementation", policy.ReviewRounds.FinalImplementation},
	}
	for _, limit := range limits {
		if limit.value < MinReviewRounds || limit.value > MaxReviewRounds {
			return protocolErr(ProtocolFieldInvalid, limit.field,
				"review round 上限は %d 以上 %d 以下でなければならない: %d",
				MinReviewRounds, MaxReviewRounds, limit.value)
		}
	}
	return nil
}

func encodeEscalationPolicy(policy EscalationPolicy) []byte {
	var b strings.Builder
	writeYAMLString(&b, 0, "schema", policy.Schema)
	writeYAMLString(&b, 0, "attemptRetries", strconv.Itoa(policy.AttemptRetries))
	b.WriteString("reviewRounds:\n")
	// 整数は Artifact Manifest の length と同じく decimal string として encode する。
	// implicit int を使うと、YAML 実装ごとの数値表現の差が canonical bytes へ漏れる。
	writeYAMLString(&b, 2, "testValidity", strconv.Itoa(policy.ReviewRounds.TestValidity))
	writeYAMLString(&b, 2, "finalImplementation", strconv.Itoa(policy.ReviewRounds.FinalImplementation))
	return []byte(b.String())
}

// ReadEscalationPolicyArtifact は ref/payload を照合して保存 bytes を返す。
func ReadEscalationPolicyArtifact(ref EscalationPolicyRef, payload ArtifactPayload) ([]byte, error) {
	if !validSchemaIdentity(ref.Schema, escalationPolicySchemaPrefix) {
		return nil, protocolSchemaErr("schema", ref.Schema, "EscalationPolicyRef schema が不正: %q", ref.Schema)
	}
	return readVersionedArtifact(ArtifactKindEscalationPolicy, ref.Schema, ref.Digest, payload)
}
