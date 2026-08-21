package contract

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

var compilerTestIssue = IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 10}

func requireCompiled(t *testing.T, body string) *CompiledIssue {
	t.Helper()
	compiled, errs := Compile(body, compilerTestIssue)
	if len(errs) > 0 {
		t.Fatalf("compile error: %v", errs)
	}
	if compiled == nil {
		t.Fatal("compiled issue が nil")
	}
	return compiled
}

func requireGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	want := []byte(readFixture(t, "canonical/"+name))
	if !bytes.Equal(got, want) {
		t.Fatalf("%s が golden と不一致\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

func sampleContextManifest(compiled *CompiledIssue) ContextManifest {
	return ContextManifest{
		Schema:      ContextManifestSchemaV1Alpha1,
		TaskContext: compiled.TaskContextRef,
		BaseSHA:     "0123456789abcdef0123456789abcdef01234567",
		Parent:      cloneIssueRef(compiled.ClaimRequirements.Parent),
		Dependencies: []DependencyCompletion{
			{Issue: compiled.ClaimRequirements.DependsOn[0], CompletionDigest: SHA256([]byte("dependency-31"))},
			{Issue: compiled.ClaimRequirements.DependsOn[1], CompletionDigest: SHA256([]byte("dependency-9"))},
		},
		AuthorityRefs: []AuthorityContent{
			{Ref: compiled.ClaimRequirements.AuthorityRefs[0], ContentDigest: SHA256([]byte("AGENTS.md"))},
			{Ref: compiled.ClaimRequirements.AuthorityRefs[1], ContentDigest: SHA256([]byte("issue-contract"))},
			{Ref: compiled.ClaimRequirements.AuthorityRefs[2], ContentDigest: SHA256([]byte("template"))},
			{Ref: compiled.ClaimRequirements.AuthorityRefs[3], ContentDigest: SHA256([]byte("parent-body"))},
		},
	}
}

func sampleExecutionPolicy() ExecutionPolicy {
	return ExecutionPolicy{
		Schema: ExecutionPolicySchemaV1Alpha1,
		IssueWorker: WorkerExecutionPolicy{
			Provider:        "codex",
			Model:           "gpt-5.5",
			Adapter:         "codex-cli",
			AdapterVersion:  "1.2.3",
			ToolPermissions: []string{"repository:write", "github:write"},
			Timeout:         45 * time.Minute,
		},
		ReviewWorker: WorkerExecutionPolicy{
			Provider:        "claude",
			Model:           "claude-opus",
			Adapter:         "claude-cli",
			AdapterVersion:  "2.0.0",
			ToolPermissions: []string{"repository:read"},
			Timeout:         30 * time.Minute,
		},
	}
}

func TestCompileCanonicalGolden(t *testing.T) {
	body := readFixture(t, "valid/full.md")
	compiled := requireCompiled(t, body)

	if !bytes.Equal(compiled.RawBodyPayload.Data, []byte(body)) {
		t.Fatal("raw body payload が入力 bytes と一致しない")
	}
	if compiled.RawBodyPayload.Digest != SHA256([]byte(body)) {
		t.Fatal("raw body digest が exact bytes に bind されていない")
	}
	requireGolden(t, compiled.ObservationPayload.Data, "issue-observation.yaml")
	requireGolden(t, compiled.TaskContextPayload.Data, "task-context.yaml")

	manifestRef, manifestPayload, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatalf("manifest encode error: %v", err)
	}
	requireGolden(t, manifestPayload.Data, "context-manifest.yaml")
	if manifestRef.Digest != manifestPayload.Digest {
		t.Fatal("ContextManifestRef が payload digest と一致しない")
	}

	policyRef, policyPayload, err := EncodeExecutionPolicy(sampleExecutionPolicy())
	if err != nil {
		t.Fatalf("policy encode error: %v", err)
	}
	requireGolden(t, policyPayload.Data, "execution-policy.yaml")
	if policyRef.Digest != policyPayload.Digest {
		t.Fatal("ExecutionPolicyRef が payload digest と一致しない")
	}

	for name, data := range map[string][]byte{
		"observation":  compiled.ObservationPayload.Data,
		"task context": compiled.TaskContextPayload.Data,
		"manifest":     manifestPayload.Data,
		"policy":       policyPayload.Data,
	} {
		if bytes.Contains(data, []byte{'\r'}) {
			t.Fatalf("%s に CR が含まれる", name)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Fatalf("%s が LF で終わらない", name)
		}
	}
}

func TestCompilerCanBeSelectedByPinnedVersion(t *testing.T) {
	compiler, err := CompilerForVersion(IssueCompilerVersionV1Alpha1)
	if err != nil {
		t.Fatal(err)
	}
	compiled, errs := compiler.Compile(readFixture(t, "valid/minimal.md"), compilerTestIssue)
	if len(errs) > 0 {
		t.Fatalf("pinned compilerのcompile error: %v", errs)
	}
	if compiled.CompilerVersion != IssueCompilerVersionV1Alpha1 {
		t.Fatalf("compiler version = %q", compiled.CompilerVersion)
	}

	if _, err := CompilerForVersion("kudo.issue-compiler/v9"); err == nil {
		t.Fatal("未対応compiler versionを受理した")
	}
}

func TestCompileDeterministic(t *testing.T) {
	body := readFixture(t, "valid/full.md")
	first := requireCompiled(t, body)
	second := requireCompiled(t, body)

	if !reflect.DeepEqual(first, second) {
		t.Fatal("同じ raw body、Issue identity、Compiler version の compile 結果が一致しない")
	}
	if first.CompilerVersion != IssueCompilerVersionV1Alpha1 {
		t.Fatalf("compiler version = %q", first.CompilerVersion)
	}
}

func TestCompileObservationAndTaskContextSeparation(t *testing.T) {
	body := readFixture(t, "valid/minimal.md")
	base := requireCompiled(t, body)
	baseManifestRef, _, err := EncodeContextManifest(base.ClaimRequirements, ContextManifest{
		Schema:      ContextManifestSchemaV1Alpha1,
		TaskContext: base.TaskContextRef,
		BaseSHA:     "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]string{
		"CRLF": strings.ReplaceAll(body, "\n", "\r\n"),
		"template comment": strings.Replace(
			body,
			"## Outcome\n\n",
			"## Outcome\n\n<!-- compiler から除外する template comment -->\n\n",
			1,
		),
	}

	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			got := requireCompiled(t, variant)
			if got.ObservationRef == base.ObservationRef {
				t.Fatal("exact body 差分で IssueObservationRef が変わらない")
			}
			if got.TaskContextRef != base.TaskContextRef {
				t.Fatalf("非意味的差分で TaskContextRef が変化: got %v, want %v", got.TaskContextRef, base.TaskContextRef)
			}
			if !bytes.Equal(got.TaskContextPayload.Data, base.TaskContextPayload.Data) {
				t.Fatal("非意味的差分で canonical Task Context bytes が変化")
			}
			if !reflect.DeepEqual(got.ClaimRequirements, base.ClaimRequirements) {
				t.Fatal("非意味的差分で ClaimRequirements が変化")
			}
			manifestRef, _, err := EncodeContextManifest(got.ClaimRequirements, ContextManifest{
				Schema:      ContextManifestSchemaV1Alpha1,
				TaskContext: got.TaskContextRef,
				BaseSHA:     "0123456789abcdef0123456789abcdef01234567",
			})
			if err != nil {
				t.Fatal(err)
			}
			if manifestRef != baseManifestRef {
				t.Fatal("Issue Observationだけの差分で ContextManifestRef が変化")
			}
		})
	}

	context := string(base.TaskContextPayload.Data)
	if strings.Contains(context, "<!-- compiler") {
		t.Fatal("Task Context に template comment が残った")
	}
}

func TestCompileSemanticDifferenceMatrix(t *testing.T) {
	body := readFixture(t, "valid/minimal.md")
	base := requireCompiled(t, body)
	tests := []struct {
		name         string
		body         string
		claimChanges bool
	}{
		{"readiness", strings.Replace(body, "readiness: ready", "readiness: draft", 1), true},
		{"parent", strings.Replace(body, "parent: null", "parent: github://mrbaron3/kudo/issues/1", 1), true},
		{"dependsOn", strings.Replace(body, "dependsOn: []", "dependsOn:\n  - github://mrbaron3/kudo/issues/9", 1), true},
		{"authorityRefs", strings.Replace(body, "authorityRefs: []", "authorityRefs:\n  - AGENTS.md", 1), true},
		{"criterion ID", strings.ReplaceAll(body, "AC-1", "AC-X"), false},
		{"outcome", strings.Replace(body, "完了後に外部から観測できる結果。", "別の外部結果。", 1), false},
		{"scope", strings.Replace(body, "- 対象\n", "- 変更後の対象\n", 1), false},
		{"deliverables", strings.Replace(body, "- 成果物\n", "- 別の成果物\n", 1), false},
		{"criterion body", strings.Replace(body, "- Then: 期待結果", "- Then: 別の期待結果", 1), false},
		{"verification", strings.Replace(body, "- `mise run check`", "- `go test ./...`", 1), false},
		{"constraints", strings.Replace(body, "- 不変条件", "- 変更後の不変条件", 1), false},
		{"decision authority", strings.Replace(body, "- このIssue内で決めてよい: 内部構成", "- このIssue内で決めてよい: file分割", 1), false},
		{"stop conditions", strings.Replace(body, "- 停止条件", "- authority矛盾で停止", 1), false},
		{"advisory hints", body + "\n\n## Advisory Hints\n\n- 非拘束の助言", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requireCompiled(t, tt.body)
			if got.TaskContextRef == base.TaskContextRef {
				t.Fatal("意味のある変更で TaskContextRef が変わらない")
			}
			claimChanged := !reflect.DeepEqual(got.ClaimRequirements, base.ClaimRequirements)
			if claimChanged != tt.claimChanges {
				t.Fatalf("ClaimRequirements changed = %v, want %v", claimChanged, tt.claimChanges)
			}
		})
	}
}

func TestCanonicalNullAndEmptyList(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/minimal.md"))
	if compiled.TaskContext.DependsOn == nil || compiled.TaskContext.AuthorityRefs == nil {
		t.Fatal("TaskContext の明示的な空 list が nil になった")
	}
	if compiled.ClaimRequirements.DependsOn == nil || compiled.ClaimRequirements.AuthorityRefs == nil {
		t.Fatal("ClaimRequirements の明示的な空 list が nil になった")
	}
	context := string(compiled.TaskContextPayload.Data)
	for _, want := range []string{
		"  parent: null\n",
		"  dependsOn: []\n",
		"  authorityRefs: []\n",
		"advisoryHints: null\n",
	} {
		if !strings.Contains(context, want) {
			t.Fatalf("Task Context に %q が無い", want)
		}
	}

	manifest := ContextManifest{
		Schema:      ContextManifestSchemaV1Alpha1,
		TaskContext: compiled.TaskContextRef,
		BaseSHA:     "0123456789abcdef0123456789abcdef01234567",
	}
	_, payload, err := EncodeContextManifest(compiled.ClaimRequirements, manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"parent: null\n", "dependencies: []\n", "authorityRefs: []\n"} {
		if !strings.Contains(string(payload.Data), want) {
			t.Fatalf("Context Manifest に %q が無い", want)
		}
	}
}

// GitHub の owner/repository は case-insensitive であり、同じ Issue を指す reference が
// 表記の case 差分だけで別 identity になってはならない。parser は既に EqualFold で
// 同一 repository と判定し、重複も case を無視して拒否するため、canonical encode 側も
// 同じ同値関係へ揃える。
func TestCompileNormalizesIssueReferenceCase(t *testing.T) {
	body := readFixture(t, "valid/minimal.md")
	lower := strings.Replace(body, "parent: null", "parent: github://mrbaron3/kudo/issues/1", 1)
	mixed := strings.Replace(body, "parent: null", "parent: github://MrBaron3/Kudo/issues/1", 1)

	base := requireCompiled(t, lower)
	got := requireCompiled(t, mixed)

	if got.TaskContextRef != base.TaskContextRef {
		t.Fatalf("reference の case 差分で TaskContextRef が変化: got %v, want %v", got.TaskContextRef, base.TaskContextRef)
	}
	if !reflect.DeepEqual(got.ClaimRequirements, base.ClaimRequirements) {
		t.Fatalf("reference の case 差分で ClaimRequirements が変化: got %+v", got.ClaimRequirements)
	}
	if want := "github://mrbaron3/kudo/issues/1"; got.TaskContext.Parent.String() != want {
		t.Fatalf("parent = %q, want %q", got.TaskContext.Parent.String(), want)
	}

	// caller が渡す verified Issue identity も同じ規則で正規化する
	mixedIssue, errs := Compile(body, IssueRef{Owner: "MrBaron3", Repository: "Kudo", Number: 10})
	if len(errs) > 0 {
		t.Fatalf("compile error: %v", errs)
	}
	lowerIssue := requireCompiled(t, body)
	if mixedIssue.TaskContextRef != lowerIssue.TaskContextRef {
		t.Fatal("Issue identity の case 差分で TaskContextRef が変化")
	}
	if mixedIssue.ObservationRef != lowerIssue.ObservationRef {
		t.Fatal("Issue identity の case 差分で IssueObservationRef が変化")
	}
}

// control character は canonical artifact と PostgreSQL の text/jsonb へ格納できず、
// compile 成功後の保存で初めて失敗する。信頼境界で fail-closed に拒否する。
func TestCompileRejectsControlCharacters(t *testing.T) {
	body := readFixture(t, "valid/minimal.md")
	tests := map[string]string{
		"NUL":        strings.Replace(body, "- 停止条件", "- 停止条件\x00", 1),
		"ESC":        strings.Replace(body, "- 停止条件", "- 停止条件\x1b[31m", 1),
		"BEL":        strings.Replace(body, "- 停止条件", "- 停止条件\x07", 1),
		"DEL":        strings.Replace(body, "- 停止条件", "- 停止条件\x7f", 1),
		"lone CR":    strings.Replace(body, "- 停止条件", "- 停止条件\r混入", 1),
		"in fence":   strings.Replace(body, "kind: task", "kind: task\x00", 1),
		"in AC body": strings.Replace(body, "- Then: 期待結果", "- Then: 期待結果\x0b", 1),
	}
	for name, variant := range tests {
		t.Run(name, func(t *testing.T) {
			compiled, errs := Compile(variant, compilerTestIssue)
			if compiled != nil {
				t.Fatal("control character を含む body から artifact を生成した")
			}
			if len(errs) != 1 || errs[0].Code != CodeBodyControlCharacter {
				t.Fatalf("errors = %v, want 1 件の %s", errs, CodeBodyControlCharacter)
			}
		})
	}

	// CRLF と TAB は正当な本文として通す
	for name, variant := range map[string]string{
		"CRLF": strings.ReplaceAll(body, "\n", "\r\n"),
		"TAB":  strings.Replace(body, "- 停止条件", "- 停止条件\tの補足", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, errs := Compile(variant, compilerTestIssue); len(errs) > 0 {
				t.Fatalf("正当な body を拒否した: %v", errs)
			}
		})
	}
}

func TestCompileRejectsUnverifiedOrNonUTF8Input(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		issue IssueRef
		code  Code
	}{
		{"missing issue", readFixture(t, "valid/minimal.md"), IssueRef{}, CodeIssueRefInvalid},
		{"zero issue number", readFixture(t, "valid/minimal.md"), IssueRef{Owner: "o", Repository: "r"}, CodeIssueRefInvalid},
		{"invalid UTF-8", string([]byte{0xff}), compilerTestIssue, CodeBodyEncodingInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, errs := Compile(tt.body, tt.issue)
			if compiled != nil || len(errs) != 1 || errs[0].Code != tt.code {
				t.Fatalf("compiled = %v, errors = %v", compiled, errs)
			}
		})
	}
}
