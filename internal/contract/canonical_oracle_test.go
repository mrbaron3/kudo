package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// canonical YAML encoder は golden fixture と自分自身でしか比較されないため、
// 「fixture と一致する」ことは「YAML として正しい」ことを意味しない。
// 独立実装（Ruby psych）を差分オラクルにして、encoder の出力が実際に YAML として
// parse でき、string scalar が escape 前の値へ戻ることを検証する。
//
// KUDO_YAML_ORACLE=required を設定すると parser 不在時に skip せず失敗する。CI で設定する。

// yamlOracleCandidates は YAML を JSON へ変換する独立実装の候補である。
// 1 つの runtime へ依存しないよう複数を順に試す。
var yamlOracleCandidates = []struct {
	name string
	args []string
}{
	{"ruby", []string{"-e", `require "yaml"; require "json"; print JSON.generate(YAML.safe_load(STDIN.read))`}},
	{"python3", []string{"-c", `import sys,yaml,json; print(json.dumps(yaml.safe_load(sys.stdin.read())),end="")`}},
}

// lookupYAMLOracle は利用できる oracle を返す。probe まで行い、runtime はあるが
// YAML library が無い候補を除外する。
func lookupYAMLOracle() (string, []string, bool) {
	for _, candidate := range yamlOracleCandidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		probe := exec.Command(path, candidate.args...)
		probe.Stdin = strings.NewReader("probe: \"ok\"\n")
		if err := probe.Run(); err == nil {
			return path, candidate.args, true
		}
	}
	return "", nil, false
}

// yamlOracle は canonical YAML bytes を独立実装で parse し、JSON へ変換して返す。
func yamlOracle(t *testing.T, data []byte) []byte {
	t.Helper()
	path, args, ok := lookupYAMLOracle()
	if !ok {
		if os.Getenv("KUDO_YAML_ORACLE") == "required" {
			t.Fatal("KUDO_YAML_ORACLE=required だが YAML oracle (ruby / python3+PyYAML) が無い")
		}
		t.Skip("YAML oracle (ruby / python3+PyYAML) が無いため skip")
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("canonical bytes が YAML として parse できない: %v\n%s\n--- input ---\n%s", err, stderr.String(), data)
	}
	return stdout.Bytes()
}

// oracleTaskContext は oracle が復元した Task Context を表す。
// encoder と別経路で key 名・入れ子・null/空 list の区別を固定する。
type oracleTaskContext struct {
	Schema   string `json:"schema"`
	Compiler string `json:"compiler"`
	Issue    string `json:"issue"`
	Contract struct {
		Schema                string   `json:"schema"`
		Kind                  string   `json:"kind"`
		Readiness             string   `json:"readiness"`
		Parent                *string  `json:"parent"`
		DependsOn             []string `json:"dependsOn"`
		AcceptanceCriteriaIDs []string `json:"acceptanceCriteriaIds"`
		AuthorityRefs         []string `json:"authorityRefs"`
	} `json:"contract"`
	Outcome            string `json:"outcome"`
	Scope              string `json:"scope"`
	Deliverables       string `json:"deliverables"`
	AcceptanceCriteria struct {
		Preamble string `json:"preamble"`
		Criteria []struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"criteria"`
	} `json:"acceptanceCriteria"`
	VerificationAndEvidence     string  `json:"verificationAndEvidence"`
	ConstraintsAndInvariants    string  `json:"constraintsAndInvariants"`
	DecisionAuthority           string  `json:"decisionAuthority"`
	StopAndEscalationConditions string  `json:"stopAndEscalationConditions"`
	AdvisoryHints               *string `json:"advisoryHints"`
}

func decodeOracle[T any](t *testing.T, data []byte) T {
	t.Helper()
	var got T
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("oracle 出力の decode に失敗: %v\n%s", err, data)
	}
	return got
}

func TestTaskContextRoundTripsThroughExternalYAMLParser(t *testing.T) {
	// escape が必要な文字を本文へ混ぜ、encoder の quote 規則を実装非依存に検証する
	body := strings.Replace(
		readFixture(t, "valid/full.md"),
		"- 停止条件",
		"- 停止条件: \"引用\" と \\ backslash と ` code span と 絵文字 😀\n- `key: value` に見える行\n\t- tab 始まりの行",
		1,
	)
	compiled := requireCompiled(t, body)
	got := decodeOracle[oracleTaskContext](t, yamlOracle(t, compiled.TaskContextPayload.Data))
	want := compiled.TaskContext

	if got.Schema != want.Schema || got.Compiler != want.Compiler || got.Issue != want.Issue.String() {
		t.Fatalf("identity が復元されない: %+v", got)
	}
	if got.Contract.Schema != want.ContractSchema ||
		got.Contract.Kind != string(want.Kind) ||
		got.Contract.Readiness != string(want.Readiness) {
		t.Fatalf("contract scalar が復元されない: %+v", got.Contract)
	}
	if want.Parent == nil {
		if got.Contract.Parent != nil {
			t.Fatal("parent: null が null として復元されない")
		}
	} else if got.Contract.Parent == nil || *got.Contract.Parent != want.Parent.String() {
		t.Fatalf("parent = %v, want %q", got.Contract.Parent, want.Parent.String())
	}

	wantDependsOn := make([]string, len(want.DependsOn))
	for i, ref := range want.DependsOn {
		wantDependsOn[i] = ref.String()
	}
	wantAuthority := make([]string, len(want.AuthorityRefs))
	for i, ref := range want.AuthorityRefs {
		wantAuthority[i] = ref.String()
	}
	if !reflect.DeepEqual(got.Contract.DependsOn, wantDependsOn) ||
		!reflect.DeepEqual(got.Contract.AcceptanceCriteriaIDs, want.AcceptanceCriteriaIDs) ||
		!reflect.DeepEqual(got.Contract.AuthorityRefs, wantAuthority) {
		t.Fatalf("list が復元されない: %+v", got.Contract)
	}

	sections := []struct {
		name string
		got  string
		want string
	}{
		{"outcome", got.Outcome, want.Outcome},
		{"scope", got.Scope, want.Scope},
		{"deliverables", got.Deliverables, want.Deliverables},
		{"preamble", got.AcceptanceCriteria.Preamble, want.AcceptanceCriteriaPreamble},
		{"verificationAndEvidence", got.VerificationAndEvidence, want.VerificationAndEvidence},
		{"constraintsAndInvariants", got.ConstraintsAndInvariants, want.ConstraintsAndInvariants},
		{"decisionAuthority", got.DecisionAuthority, want.DecisionAuthority},
		{"stopAndEscalationConditions", got.StopAndEscalationConditions, want.StopAndEscalationConditions},
	}
	for _, section := range sections {
		if section.got != section.want {
			t.Fatalf("%s が escape 前の値へ戻らない\n got: %q\nwant: %q", section.name, section.got, section.want)
		}
	}

	if len(got.AcceptanceCriteria.Criteria) != len(want.AcceptanceCriteria) {
		t.Fatalf("criteria 件数 = %d, want %d", len(got.AcceptanceCriteria.Criteria), len(want.AcceptanceCriteria))
	}
	for i, criterion := range got.AcceptanceCriteria.Criteria {
		if criterion.ID != want.AcceptanceCriteria[i].ID || criterion.Body != want.AcceptanceCriteria[i].Body {
			t.Fatalf("criteria[%d] が復元されない: %+v", i, criterion)
		}
	}

	if want.AdvisoryHints == nil {
		if got.AdvisoryHints != nil {
			t.Fatal("advisoryHints: null が null として復元されない")
		}
	} else if got.AdvisoryHints == nil || *got.AdvisoryHints != *want.AdvisoryHints {
		t.Fatalf("advisoryHints = %v", got.AdvisoryHints)
	}
}

// 存在する空 Advisory Hints は空 string、欠落は null として区別されなければならない。
// 実装内の比較では両者が同じ Go zero value になりうるため oracle 側で確認する。
func TestAdvisoryHintsNullAndEmptyAreDistinctToExternalParser(t *testing.T) {
	empty := decodeOracle[oracleTaskContext](t,
		yamlOracle(t, requireCompiled(t, readFixture(t, "valid/advisory-empty.md")).TaskContextPayload.Data))
	missing := decodeOracle[oracleTaskContext](t,
		yamlOracle(t, requireCompiled(t, readFixture(t, "valid/minimal.md")).TaskContextPayload.Data))

	if missing.AdvisoryHints != nil {
		t.Fatalf("欠落 section が null にならない: %q", *missing.AdvisoryHints)
	}
	if empty.AdvisoryHints == nil {
		t.Fatal("存在する空 section が null になった")
	}
	if *empty.AdvisoryHints != "" {
		t.Fatalf("空 section = %q, want 空 string", *empty.AdvisoryHints)
	}
}

func TestManifestAndPolicyParseWithExternalYAMLParser(t *testing.T) {
	compiled := requireCompiled(t, readFixture(t, "valid/full.md"))
	_, manifestPayload, err := EncodeContextManifest(compiled.ClaimRequirements, sampleContextManifest(compiled))
	if err != nil {
		t.Fatal(err)
	}
	_, policyPayload, err := EncodeExecutionPolicy(sampleExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}

	manifest := decodeOracle[struct {
		Schema      string  `json:"schema"`
		BaseSHA     string  `json:"baseSha"`
		Parent      *string `json:"parent"`
		TaskContext struct {
			Schema string `json:"schema"`
			Digest string `json:"digest"`
		} `json:"taskContext"`
		Dependencies []struct {
			Issue            string `json:"issue"`
			CompletionDigest string `json:"completionDigest"`
		} `json:"dependencies"`
		AuthorityRefs []struct {
			Ref           string `json:"ref"`
			ContentDigest string `json:"contentDigest"`
		} `json:"authorityRefs"`
	}](t, yamlOracle(t, manifestPayload.Data))

	if manifest.Schema != ContextManifestSchemaV1Alpha1 ||
		manifest.TaskContext.Digest != string(compiled.TaskContextRef.Digest) {
		t.Fatalf("manifest が復元されない: %+v", manifest)
	}
	if len(manifest.Dependencies) != len(compiled.ClaimRequirements.DependsOn) {
		t.Fatalf("dependencies 件数 = %d", len(manifest.Dependencies))
	}
	for i, dependency := range manifest.Dependencies {
		if dependency.Issue != compiled.ClaimRequirements.DependsOn[i].String() {
			t.Fatalf("dependencies[%d] = %q", i, dependency.Issue)
		}
	}
	for i, authority := range manifest.AuthorityRefs {
		if authority.Ref != compiled.ClaimRequirements.AuthorityRefs[i].String() {
			t.Fatalf("authorityRefs[%d] = %q", i, authority.Ref)
		}
	}

	policy := decodeOracle[struct {
		Schema      string `json:"schema"`
		IssueWorker struct {
			Provider        string   `json:"provider"`
			ToolPermissions []string `json:"toolPermissions"`
			Timeout         string   `json:"timeout"`
		} `json:"issueWorker"`
	}](t, yamlOracle(t, policyPayload.Data))

	if policy.Schema != ExecutionPolicySchemaV1Alpha1 || policy.IssueWorker.Provider != "codex" {
		t.Fatalf("policy が復元されない: %+v", policy)
	}
	if !reflect.DeepEqual(policy.IssueWorker.ToolPermissions, []string{"github:write", "repository:write"}) {
		t.Fatalf("toolPermissions が lexicographic 順で復元されない: %v", policy.IssueWorker.ToolPermissions)
	}
	if policy.IssueWorker.Timeout != "45m0s" {
		t.Fatalf("timeout = %q", policy.IssueWorker.Timeout)
	}
}
