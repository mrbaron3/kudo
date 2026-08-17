package contract

import (
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var testSelf = repositoryRef{Owner: "mrbaron3", Name: "kudo"}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s を読めない: %v", name, err)
	}
	return string(b)
}

func fixtureNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", dir))
	if err != nil {
		t.Fatalf("testdata/%s を読めない: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			// invalidExpectations の key と一致させるため slash 区切りで組み立てる
			// （filepath.Join は Windows で `invalid\x.md` になる）
			names = append(names, path.Join(dir, e.Name()))
		}
	}
	if len(names) == 0 {
		t.Fatalf("testdata/%s に fixture が無い", dir)
	}
	return names
}

func TestParseValidFixtures(t *testing.T) {
	for _, name := range fixtureNames(t, "valid") {
		t.Run(name, func(t *testing.T) {
			task, errs := parse(readFixture(t, name), testSelf)
			if len(errs) > 0 {
				t.Fatalf("valid fixture でエラー: %v", errs)
			}
			if task == nil {
				t.Fatal("task が nil")
			}
			if got := len(task.Contract.AcceptanceCriteriaIDs); got == 0 {
				t.Fatal("acceptanceCriteriaIds が空")
			}
			if len(task.AcceptanceCriteria) != len(task.Contract.AcceptanceCriteriaIDs) {
				t.Fatalf("criterion 数 %d と ID 数 %d が一致しない",
					len(task.AcceptanceCriteria), len(task.Contract.AcceptanceCriteriaIDs))
			}
		})
	}
}

func TestParseMinimalTyped(t *testing.T) {
	task, errs := parse(readFixture(t, "valid/minimal.md"), testSelf)
	if len(errs) > 0 {
		t.Fatalf("エラー: %v", errs)
	}
	want := parsedContract{
		Schema:                "kudo.issue/v1alpha1",
		Kind:                  KindTask,
		Readiness:             ReadinessReady,
		Parent:                nil,
		DependsOn:             []IssueRef{},
		AcceptanceCriteriaIDs: []string{"AC-1"},
		AuthorityRefs:         []AuthorityRef{},
	}
	if !reflect.DeepEqual(task.Contract, want) {
		t.Fatalf("Contract = %+v, want %+v", task.Contract, want)
	}
	if !task.Contract.isExecutableReadiness() {
		t.Fatal("readiness: ready が実行可能と判定されない")
	}
	if len(task.Sections) != len(requiredSections) {
		t.Fatalf("section 数 = %d, want %d", len(task.Sections), len(requiredSections))
	}
}

func TestParseFullTyped(t *testing.T) {
	task, errs := parse(readFixture(t, "valid/full.md"), testSelf)
	if len(errs) > 0 {
		t.Fatalf("エラー: %v", errs)
	}
	issue1 := IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 1}
	want := parsedContract{
		Schema:    "kudo.issue/v1alpha1",
		Kind:      KindTask,
		Readiness: ReadinessReady,
		Parent:    &issue1,
		DependsOn: []IssueRef{
			{Owner: "mrbaron3", Repository: "kudo", Number: 31},
			{Owner: "mrbaron3", Repository: "kudo", Number: 9},
		},
		AcceptanceCriteriaIDs: []string{"AC-1", "AC-2", "AC-3"},
		AuthorityRefs: []AuthorityRef{
			{Path: "AGENTS.md"},
			{Path: "docs/contracts/issue-contract-v1alpha1.md"},
			{Path: ".github/ISSUE_TEMPLATE/kudo-task.md"},
			{Issue: &issue1},
		},
	}
	if !reflect.DeepEqual(task.Contract, want) {
		t.Fatalf("Contract = %+v, want %+v", task.Contract, want)
	}

	wantTitles := append(append([]string{}, requiredSections...), sectionAdvisoryHints)
	var gotTitles []string
	for _, s := range task.Sections {
		gotTitles = append(gotTitles, s.Title)
	}
	if !reflect.DeepEqual(gotTitles, wantTitles) {
		t.Fatalf("section 順序 = %v, want %v", gotTitles, wantTitles)
	}

	var ids []string
	for _, c := range task.AcceptanceCriteria {
		ids = append(ids, c.ID)
	}
	if !reflect.DeepEqual(ids, []string{"AC-1", "AC-2", "AC-3"}) {
		t.Fatalf("criterion ID = %v", ids)
	}

	// fence 内の `##` を heading として拾っていないことを確認する
	// （4 backtick fence は内側の 3 backtick で閉じない）
	if sec, ok := task.section(sectionVerification); !ok || !strings.Contains(sec.Content, "## Fake Heading") {
		t.Fatal("fence 内の内容が Verification section に保持されていない")
	}
	// indent された fence と tilde fence の内側の行頭 `##` も heading として拾わない
	sec, ok := task.section(sectionConstraints)
	if !ok {
		t.Fatal("Constraints section が無い")
	}
	for _, want := range []string{"## Fenced Fake", "## Tilde Fake"} {
		if !strings.Contains(sec.Content, want) {
			t.Fatalf("fence 内の %q が Constraints section に保持されていない", want)
		}
	}
}

func TestParseTemplateDraftNotExecutable(t *testing.T) {
	task, errs := parse(readFixture(t, "valid/template-draft.md"), testSelf)
	if len(errs) > 0 {
		t.Fatalf("エラー: %v", errs)
	}
	if task.Contract.Readiness != ReadinessDraft {
		t.Fatalf("readiness = %s, want draft", task.Contract.Readiness)
	}
	if task.Contract.isExecutableReadiness() {
		t.Fatal("draft が実行可能と判定された")
	}
}

// expectedError は invalid fixture の期待エラーを code・field・行番号まで固定する。
type expectedError struct {
	Code  Code
	Field string
	Line  int
}

// invalidExpectations は fixture ごとの期待エラーを返却順（行番号昇順、位置なしは先頭）
// で固定する。順序と位置の固定は AC-3（決定性）と deliverable（field/section 位置を
// 保持する validation result）の検証を兼ねる。
var invalidExpectations = map[string][]expectedError{
	"invalid/ac-criterion-duplicate.md": {{CodeACCriterionDuplicate, "", 40}},
	"invalid/ac-criterion-order.md":     {{CodeACCriterionOutOfOrder, "", 35}},
	"invalid/ac-id-duplicate.md":        {{CodeACIDDuplicate, "acceptanceCriteriaIds", 11}},
	"invalid/ac-ids-empty.md":           {{CodeACIDsEmpty, "acceptanceCriteriaIds", 9}},
	"invalid/ac-mismatch.md": {
		{CodeACCriterionMissing, "acceptanceCriteriaIds", 33},
		{CodeACCriterionUnlisted, "", 41},
	},
	"invalid/advisory-not-last.md": {
		{CodeSectionOutOfOrder, "", 52},
		{CodeSectionOutOfOrder, "", 56},
	},
	// 16: 行頭側の混在、30: 行末側の混在、43: 可視内容が inline code span だけの混在、
	// 48: comment 内の backtick が comment 外の backtick と対になる形
	"invalid/comment-ambiguous.md": {
		{CodeCommentAmbiguous, "", 16},
		{CodeCommentAmbiguous, "", 30},
		{CodeCommentAmbiguous, "", 43},
		{CodeCommentAmbiguous, "", 48},
	},
	"invalid/contract-block-duplicate.md": {{CodeContractBlockDuplicate, "", 14}},
	"invalid/contract-block-missing.md": {
		{CodeSectionEmpty, "", 1},
		{CodeContractBlockMissing, "", 1},
	},
	"invalid/contract-extra-content.md": {{CodeContractExtraContent, "", 14}},
	"invalid/contract-fence-info.md":    {{CodeContractBlockMissing, "", 3}},
	"invalid/enum-invalid.md": {
		{CodeEnumInvalid, "schema", 4},
		{CodeEnumInvalid, "kind", 5},
		{CodeEnumInvalid, "readiness", 6},
	},
	"invalid/fence-unclosed.md": {
		{CodeSectionMissing, "", 0},
		{CodeSectionMissing, "", 0},
		{CodeSectionMissing, "", 0},
		{CodeFenceUnclosed, "", 42},
	},
	"invalid/field-missing.md": {{CodeFieldMissing, "dependsOn", 3}},
	"invalid/field-type.md": {
		{CodeFieldType, "schema", 4},
		{CodeFieldType, "dependsOn", 9},
		{CodeFieldType, "acceptanceCriteriaIds", 10},
	},
	"invalid/indented-fence.md": {
		{CodeIndentedMarker, "", 44},
		{CodeIndentedMarker, "", 46},
	},
	"invalid/indented-marker.md": {{CodeIndentedMarker, "", 56}},
	"invalid/preamble.md":        {{CodePreambleContent, "", 1}},
	"invalid/ref-cross-repo.md": {
		{CodeRefCrossRepository, "parent", 7},
		{CodeRefCrossRepository, "dependsOn", 9},
	},
	"invalid/ref-duplicate.md": {
		{CodeRefDuplicate, "dependsOn", 10},
		{CodeRefDuplicate, "authorityRefs", 15},
		{CodeRefDuplicate, "authorityRefs", 17},
	},
	"invalid/ref-invalid.md": {
		{CodeRefInvalid, "parent", 7},
		{CodeRefInvalid, "dependsOn", 9},
		{CodeRefInvalid, "dependsOn", 10},
		{CodeRefInvalid, "dependsOn", 11},
		{CodeRefInvalid, "authorityRefs", 15},
		{CodeRefInvalid, "authorityRefs", 16},
	},
	"invalid/section-duplicate.md":  {{CodeSectionDuplicate, "", 32}},
	"invalid/section-empty.md":      {{CodeSectionEmpty, "", 14}},
	"invalid/section-missing.md":    {{CodeSectionMissing, "", 0}},
	"invalid/section-order.md":      {{CodeSectionOutOfOrder, "", 22}},
	"invalid/section-unknown.md":    {{CodeSectionUnknown, "", 56}},
	"invalid/yaml-duplicate-key.md": {{CodeYAMLDuplicateKey, "readiness", 7}},
	"invalid/yaml-syntax-more.md": {
		{CodeFieldMissing, "readiness", 3},
		{CodeYAMLSyntax, "", 6},
		{CodeYAMLSyntax, "dependsOn", 8},
		{CodeYAMLSyntax, "", 9},
	},
	"invalid/yaml-syntax.md": {
		{CodeFieldMissing, "kind", 3},
		{CodeYAMLSyntax, "", 5},
	},
	"invalid/yaml-unknown-field.md": {{CodeYAMLUnknownField, "priority", 12}},
}

func TestParseInvalidFixtures(t *testing.T) {
	names := fixtureNames(t, "invalid")
	if len(names) != len(invalidExpectations) {
		t.Fatalf("fixture %d 件に対し期待値が %d 件", len(names), len(invalidExpectations))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			want, ok := invalidExpectations[name]
			if !ok {
				t.Fatalf("fixture %s の期待値が無い", name)
			}
			task, errs := parse(readFixture(t, name), testSelf)
			if task != nil {
				t.Fatal("invalid fixture で task が返った")
			}
			var got []expectedError
			for _, e := range errs {
				got = append(got, expectedError{Code: e.Code, Field: e.Field, Line: e.Line})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("errors = %v, want %v\nfull: %v", got, want, errs)
			}
			for i := 1; i < len(errs); i++ {
				if errs[i].Line < errs[i-1].Line {
					t.Fatalf("エラーが行番号順でない: %v", errs)
				}
			}
			for _, e := range errs {
				if e.Message == "" {
					t.Fatalf("message が空: %+v", e)
				}
			}
		})
	}
}

func TestParseReadinessBlocked(t *testing.T) {
	body := strings.Replace(readFixture(t, "valid/minimal.md"), "readiness: ready", "readiness: blocked", 1)
	task, errs := parse(body, testSelf)
	if len(errs) > 0 {
		t.Fatalf("エラー: %v", errs)
	}
	if task.Contract.Readiness != ReadinessBlocked {
		t.Fatalf("readiness = %s, want blocked", task.Contract.Readiness)
	}
	if task.Contract.isExecutableReadiness() {
		t.Fatal("blocked が実行可能と判定された")
	}
}

func TestParseCRLF(t *testing.T) {
	body := readFixture(t, "valid/minimal.md")
	crlf := strings.ReplaceAll(body, "\n", "\r\n")
	taskLF, errsLF := parse(body, testSelf)
	taskCRLF, errsCRLF := parse(crlf, testSelf)
	if len(errsLF) > 0 || len(errsCRLF) > 0 {
		t.Fatalf("エラー: %v / %v", errsLF, errsCRLF)
	}
	if !reflect.DeepEqual(taskLF, taskCRLF) {
		t.Fatal("CRLF と LF で parse 結果が一致しない")
	}
}

func TestParseDeterminism(t *testing.T) {
	var names []string
	names = append(names, fixtureNames(t, "valid")...)
	names = append(names, fixtureNames(t, "invalid")...)
	for _, name := range names {
		body := readFixture(t, name)
		t1, e1 := parse(body, testSelf)
		t2, e2 := parse(body, testSelf)
		if !reflect.DeepEqual(t1, t2) || !reflect.DeepEqual(e1, e2) {
			t.Fatalf("%s: 同じ入力で結果が一致しない", name)
		}
	}
}

func TestParseEmptyBody(t *testing.T) {
	task, errs := parse("", testSelf)
	if task != nil {
		t.Fatal("空 body で task が返った")
	}
	if len(errs) != len(requiredSections) {
		t.Fatalf("エラー数 = %d, want %d", len(errs), len(requiredSections))
	}
	for _, e := range errs {
		if e.Code != CodeSectionMissing {
			t.Fatalf("code = %s, want %s", e.Code, CodeSectionMissing)
		}
	}
}

func TestParseRequiresRepositoryRef(t *testing.T) {
	task, errs := parse("x", repositoryRef{})
	if task != nil || len(errs) != 1 || errs[0].Code != CodeRepositoryRefInvalid {
		t.Fatalf("task=%v errs=%v", task, errs)
	}
}

func TestValidationErrorString(t *testing.T) {
	e := ValidationError{Code: CodeEnumInvalid, Line: 5, Section: sectionContract, Field: "kind", Message: "msg"}
	s := e.Error()
	for _, part := range []string{"enum_invalid", "Contract", "kind", "line 5", "msg"} {
		if !strings.Contains(s, part) {
			t.Fatalf("%q に %q が含まれない", s, part)
		}
	}
}
