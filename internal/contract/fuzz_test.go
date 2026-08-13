package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzParse は任意の入力に対して panic せず、同じ入力へ常に同じ結果を返し、
// task とエラーが同時に返らないことを検証する。
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"valid", "invalid"} {
		entries, err := os.ReadDir(filepath.Join("testdata", dir))
		if err != nil {
			f.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join("testdata", dir, e.Name()))
			if err != nil {
				f.Fatal(err)
			}
			f.Add(string(b))
		}
	}
	f.Add("")
	f.Add("## Contract\n```yaml\nschema: kudo.issue/v1alpha1\n```")
	f.Add("## Contract\n<!--\n## Outcome\n-->\n```yaml\n```\n")

	self := repositoryRef{Owner: "mrbaron3", Name: "kudo"}
	issue := IssueRef{Owner: self.Owner, Repository: self.Name, Number: 10}
	f.Fuzz(func(t *testing.T, body string) {
		task1, errs1 := parse(body, self)
		task2, errs2 := parse(body, self)
		if !reflect.DeepEqual(task1, task2) || !reflect.DeepEqual(errs1, errs2) {
			t.Fatal("同じ入力で結果が一致しない")
		}
		if task1 != nil && len(errs1) > 0 {
			t.Fatal("task とエラーが同時に返った")
		}
		if task1 == nil && len(errs1) == 0 {
			t.Fatal("task もエラーも返らなかった")
		}

		compiled1, compileErrs1 := Compile(body, issue)
		compiled2, compileErrs2 := Compile(body, issue)
		if !reflect.DeepEqual(compiled1, compiled2) || !reflect.DeepEqual(compileErrs1, compileErrs2) {
			t.Fatal("compiler が同じ入力へ同じ結果を返さない")
		}
		if compiled1 != nil && len(compileErrs1) > 0 {
			t.Fatal("compiled issue と validation error が同時に返った")
		}
		if compiled1 == nil && len(compileErrs1) == 0 {
			t.Fatal("compiled issue も validation error も返らなかった")
		}
	})
}
