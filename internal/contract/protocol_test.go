package contract

import "testing"

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
