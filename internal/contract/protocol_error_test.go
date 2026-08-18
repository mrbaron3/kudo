package contract

import (
	"errors"
	"strings"
	"testing"
)

// TestProtocolErrorsAreClassifiableWithoutStringMatching は #45 AC-1 を固定する。
// Controller は失敗理由を error 文字列ではなく code で分岐する。
func TestProtocolErrorsAreClassifiableWithoutStringMatching(t *testing.T) {
	op := sampleWorkerOperation(t)

	unknownKind := op
	unknownKind.Kind = OperationKind("author_test")
	kindErr := ValidateWorkerOperation(unknownKind)
	if kindErr == nil {
		t.Fatal("unknown kind が受理された")
	}

	mismatched := sampleOperationResult(t, op)
	mismatched.OperationDigest = SHA256([]byte("別 Operation の identity"))
	digestErr := BindOperationResult(op, mismatched)
	if digestErr == nil {
		t.Fatal("digest 不一致 Result が受理された")
	}

	if !errors.Is(kindErr, ProtocolKindUnknown) {
		t.Fatalf("unknown kind が %q へ分類されない: %v", ProtocolKindUnknown, kindErr)
	}
	if !errors.Is(digestErr, ProtocolIdentityMismatch) {
		t.Fatalf("digest 不一致が %q へ分類されない: %v", ProtocolIdentityMismatch, digestErr)
	}

	// 二つが同じ code へ潰れていないことを、両方向で確かめる。片方向だけでは
	// すべての error が両 code に一致する実装を通してしまう。
	if errors.Is(kindErr, ProtocolIdentityMismatch) {
		t.Fatal("unknown kind が digest 不一致としても分類された")
	}
	if errors.Is(digestErr, ProtocolKindUnknown) {
		t.Fatal("digest 不一致が unknown kind としても分類された")
	}

	// どちらも retry 可能な transport failure ではなく、immutable な入力に対する
	// permanent な contract 違反である。
	for name, err := range map[string]error{"unknown kind": kindErr, "digest 不一致": digestErr} {
		violation, ok := ProtocolViolation(err)
		if !ok {
			t.Fatalf("%s が protocol violation として認識されない: %v", name, err)
		}
		if violation.Field == "" {
			t.Fatalf("%s がどの field の違反かを持たない", name)
		}
	}
}

// TestProtocolViolationRejectsTransportFailure は validation error と transport failure が
// 同じ値空間へ入らないことを固定する。retry 判定できない error を retry 可能側へ倒すと、
// contract 違反を無限に retry しうるため、既定は fail-closed である。
func TestProtocolViolationRejectsTransportFailure(t *testing.T) {
	transport := AttemptFailure{
		Class:     FailureTimeout,
		AttemptID: "attempt-01",
		Evidence:  "provider が 600 秒で応答しなかった",
	}
	if err := transport.Validate(); err != nil {
		t.Fatalf("transport failure record が不正: %v", err)
	}
	if _, ok := ProtocolViolation(errors.New("transport error")); ok {
		t.Fatal("素の error が protocol violation として認識された")
	}
}

// TestCanonicalTextRejectsOversizedEvidence は #45 AC-2 を固定する。
func TestCanonicalTextRejectsOversizedEvidence(t *testing.T) {
	failure := AttemptFailure{
		Class:     FailureProviderCrash,
		AttemptID: "attempt-01",
		Evidence:  strings.Repeat("e", MaxCanonicalTextBytes+1),
	}
	err := failure.Validate()
	if err == nil {
		t.Fatalf("上限 %d を超える evidence が受理された", MaxCanonicalTextBytes)
	}
	if !errors.Is(err, ProtocolFieldTooLong) {
		t.Fatalf("上限超過が %q へ分類されない: %v", ProtocolFieldTooLong, err)
	}

	// 上限ちょうどは受理する。境界を排他側へ寄せると、規定値と実装が 1 byte ずれる。
	failure.Evidence = strings.Repeat("e", MaxCanonicalTextBytes)
	if err := failure.Validate(); err != nil {
		t.Fatalf("上限ちょうどの evidence が拒否された: %v", err)
	}
}

// TestEveryProtocolValidatorReturnsClassifiableError は、protocol validator が返す拒否を
// 一つ残らず分類可能に保つ。
//
// 個別の AC を確かめるだけでは、変換し忘れた経路が素の error を返し続けても green のままに
// なる。Controller から見ると「分類できない error が混じりうる」＝結局どこかで文字列一致に
// 頼ることになり、分類できるという保証が錯覚になる。validator を追加したときに、この test が
// 新しい拒否経路を素通ししないよう、入口ごとに代表的な invalid 入力を列挙する。
func TestEveryProtocolValidatorReturnsClassifiableError(t *testing.T) {
	op := sampleWorkerOperation(t)
	claimOp := sampleClaimOperation(t)
	req := sampleReviewRequest(t)

	badDigest := Digest("sha256:not-a-digest")

	cases := map[string]func() error{
		"operation/schema": func() error {
			bad := op
			bad.Schema = "kudo.worker-operation/v0"
			return ValidateWorkerOperation(bad)
		},
		"operation/identifier": func() error {
			bad := op
			bad.OperationID = ".hidden"
			return ValidateWorkerOperation(bad)
		},
		"operation/kind-constraint": func() error {
			bad := claimOp
			bad.HeadSHA = sampleHeadSHA
			return ValidateWorkerOperation(bad)
		},
		"operation/input-artifact-duplicate": func() error {
			bad := op
			dup := SHA256([]byte("same"))
			bad.InputArtifacts = []Digest{dup, dup}
			return ValidateWorkerOperation(bad)
		},
		"operation/policy-ref": func() error {
			bad := op
			bad.PolicyRefs = []string{"../escape.md"}
			return ValidateWorkerOperation(bad)
		},
		"result/outcome": func() error {
			bad := sampleOperationResult(t, op)
			bad.Outcome = OperationOutcome("approve")
			return ValidateOperationResult(bad)
		},
		"result/changed-input-fields": func() error {
			bad := sampleOperationResult(t, op)
			bad.ChangedInputFields = []string{"headSha"}
			return ValidateOperationResult(bad)
		},
		"result/binding": func() error {
			bad := sampleOperationResult(t, op)
			bad.OutputArtifacts = nil
			return BindOperationResult(op, bad)
		},
		"attempt/outcome-conflict": func() error {
			result := sampleOperationResult(t, op)
			return OperationAttemptOutcome{
				Result:  &result,
				Failure: &AttemptFailure{Class: FailureTimeout, AttemptID: "attempt-01", Evidence: "timeout"},
			}.Validate()
		},
		"attempt/empty": func() error { return OperationAttemptOutcome{}.Validate() },
		"failure/class": func() error {
			return AttemptFailure{Class: "unknown", AttemptID: "attempt-01", Evidence: "x"}.Validate()
		},
		"review-request/kind": func() error {
			bad := req
			bad.Kind = ReviewKind("style")
			return ValidateReviewRequest(bad)
		},
		"review-request/policy-missing": func() error {
			bad := req
			bad.PolicyRefs = nil
			return ValidateReviewRequest(bad)
		},
		"review-result/verdict-conflict": func() error {
			bad := sampleReviewResult(t, req)
			bad.Verdict = VerdictRequestChanges
			for i := range bad.Findings {
				bad.Findings[i].Severity = SeverityAdvisory
			}
			return ValidateReviewResult(bad)
		},
		"review-result/binding": func() error {
			bad := sampleReviewResult(t, req)
			bad.RequestDigest = SHA256([]byte("別 request"))
			return BindReviewResult(req, bad)
		},
		"review-attempt/empty": func() error { return ReviewAttemptOutcome{}.Validate() },
		"artifact-manifest/empty": func() error {
			_, _, err := EncodeArtifactManifest(ArtifactManifest{Schema: ArtifactManifestSchemaV1Alpha1})
			return err
		},
		"artifact-manifest/name": func() error {
			_, err := NewArtifactEntry("../escape", ArtifactPayload{})
			return err
		},
		"artifact-payload/kind": func() error {
			return ArtifactPayload{Kind: ArtifactKind("unknown")}.Validate()
		},
		"artifact-payload/digest": func() error {
			return ArtifactPayload{
				Kind: ArtifactKindRawIssueBody, MediaType: MediaTypeMarkdown, Digest: badDigest,
			}.Validate()
		},
		"execution-policy/field": func() error {
			bad := sampleExecutionPolicy()
			bad.IssueWorker.Provider = ""
			_, _, err := EncodeExecutionPolicy(bad)
			return err
		},
		"execution-policy/timeout": func() error {
			bad := sampleExecutionPolicy()
			bad.ReviewWorker.Timeout = 0
			_, _, err := EncodeExecutionPolicy(bad)
			return err
		},
		"comparison/claim-has-no-semantic-input": func() error {
			_, err := CompareOperationInput(claimOp, LatestOperationInput{})
			return err
		},
		"lineage/empty": func() error {
			_, err := ObservationLineage{}.Append(*op.Observation)
			return err
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("invalid な入力が受理された")
			}
			violation, ok := ProtocolViolation(err)
			if !ok {
				t.Fatalf("分類できない error を返している: %v", err)
			}
			if violation.Code == "" {
				t.Fatalf("code を持たない protocol error: %v", err)
			}
		})
	}
}

// TestCanonicalLineRejectsOversizedFindingAndMediaType は単一行の上限を、finding summary と
// artifact manifest の media type の両方へ適用していることを固定する。
func TestCanonicalLineRejectsOversizedFindingAndMediaType(t *testing.T) {
	req := sampleReviewRequest(t)
	result := sampleReviewResult(t, req)
	result.Findings[0].Summary = strings.Repeat("s", MaxCanonicalLineBytes+1)
	if err := ValidateReviewResult(result); !errors.Is(err, ProtocolFieldTooLong) {
		t.Fatalf("上限超過の summary が %q へ分類されない: %v", ProtocolFieldTooLong, err)
	}

	result = sampleReviewResult(t, req)
	result.Findings[0].Observed = strings.Repeat("o", MaxCanonicalTextBytes+1)
	if err := ValidateReviewResult(result); !errors.Is(err, ProtocolFieldTooLong) {
		t.Fatalf("上限超過の observed が %q へ分類されない: %v", ProtocolFieldTooLong, err)
	}

	manifest := sampleArtifactManifest(t)
	manifest.Entries[0].MediaType = strings.Repeat("m", MaxCanonicalLineBytes+1)
	if _, _, err := EncodeArtifactManifest(manifest); !errors.Is(err, ProtocolFieldTooLong) {
		t.Fatalf("上限超過の mediaType が %q へ分類されない: %v", ProtocolFieldTooLong, err)
	}
}
