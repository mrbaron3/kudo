package contract

import (
	"errors"
	"strings"
	"testing"
)

// protocol identifier は canonical bytes と database key だけでなく、Run workspace の
// path segment としても使われうる。`..` のような値を protocol 層が通すと、拒否が
// filesystem 層まで遅れる（あるいは遅れた先で拒否されない）。信頼境界で弾く。
func TestProtocolIDRejectsPathShapes(t *testing.T) {
	rejected := []string{"", ".", "..", "...", ".hidden", "-leading", "_leading", "a b", "a/b", "a\\b"}
	for _, value := range rejected {
		if validProtocolID(value) {
			t.Errorf("protocol ID %q を受理した", value)
		}
	}
	for _, value := range []string{"a", "op-01", "run_01", "01KUDOEXAMPLE", "attempt.1", "F-1"} {
		if !validProtocolID(value) {
			t.Errorf("正当な protocol ID %q を拒否した", value)
		}
	}

	// envelope 境界でも同じ規則が効く
	op := sampleWorkerOperation(t)
	op.RunID = ".."
	if err := ValidateWorkerOperation(op); err == nil {
		t.Fatal("runId = \"..\" の Operation を受理した")
	}
}

// artifact の logical name は Issue Worker が作り Review Worker が読む値であり、
// review 側は immutable snapshot を disposable checkout へ展開する。name を展開先の
// 名前に使う実装は自然に出てくるため、path traversal 形状を manifest の入口で弾く。
func TestArtifactNameRejectsPathTraversalShapes(t *testing.T) {
	rejected := []string{
		"", ".", "..", "/leading", "a/../../etc/passwd", "a/..", "a/", "a//b", "a/./b", "-leading", "A", "a b",
	}
	for _, name := range rejected {
		if validArtifactName(name) {
			t.Errorf("artifact name %q を受理した", name)
		}
	}
	for _, name := range []string{"task-context", "evidence/red.txt", "a", "test_plan.v2"} {
		if !validArtifactName(name) {
			t.Errorf("正当な artifact name %q を拒否した", name)
		}
	}

	manifest := sampleArtifactManifest(t)
	manifest.Entries[0].Name = "a/../../etc/passwd"
	if _, _, err := EncodeArtifactManifest(manifest); err == nil {
		t.Fatal("traversal 形状の name を持つ manifest を受理した")
	}
}

// repository-relative path は canonical bytes と PostgreSQL text の両方へ載る。
// path 形状だけを見て文字種を見ないと、NUL や改行を含む値が protocol 層を通り、
// 拒否が保存段階まで遅れる。同じ理由で外部 reference は既に単一行を要求している。
func TestAuthorityPathRejectsNonCanonicalText(t *testing.T) {
	rejected := []string{"docs/a\x00b.md", "docs/a\nb.md", "docs/a\tb.md", "docs/a\x7fb.md", " ", "docs/a\xffb.md"}
	for _, p := range rejected {
		if validAuthorityPath(p) {
			t.Errorf("authority path %q を受理した", p)
		}
	}
	for _, p := range []string{"docs/workflow.md", "AGENTS.md", "docs/contracts/issue-contract-v1alpha1.md"} {
		if !validAuthorityPath(p) {
			t.Errorf("正当な authority path %q を拒否した", p)
		}
	}

	op := sampleWorkerOperation(t)
	op.PolicyRefs = []string{"docs/a\x00b.md"}
	if err := ValidateWorkerOperation(op); err == nil {
		t.Fatal("control character を含む policyRef を受理した")
	}

	req := sampleReviewRequest(t)
	req.PolicyRefs = []string{"docs/a\nb.md"}
	if err := ValidateReviewRequest(req); err == nil {
		t.Fatal("改行を含む policyRef を受理した")
	}
}

func TestIssueContractAuthorityPathRejectsOversizeBeforeArtifactEncoding(t *testing.T) {
	path := strings.Repeat("a", MaxCanonicalLineBytes+1)
	body := strings.Replace(readFixture(t, "valid/full.md"), "  - AGENTS.md", "  - "+path, 1)
	_, errs := Compile(body, compilerTestIssue)
	if len(errs) != 1 || errs[0].Code != CodeRefInvalid || errs[0].Field != "authorityRefs" {
		t.Fatalf("上限超過の authority path を Issue body の位置で拒否していない: %+v", errs)
	}

	op := sampleWorkerOperation(t)
	op.PolicyRefs = []string{path}
	if err := ValidateWorkerOperation(op); !errors.Is(err, ProtocolFieldTooLong) {
		t.Fatalf("protocol の policyRef 上限超過が %q へ分類されない: %v", ProtocolFieldTooLong, err)
	}
}
