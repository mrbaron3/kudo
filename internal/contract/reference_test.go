package contract

import "testing"

func TestParseIssueRefCanonicalizesValidReference(t *testing.T) {
	got, ok := ParseIssueRef("github://MrBaron3/Kudo/issues/13")
	if !ok {
		t.Fatal("valid Issue reference を拒否した")
	}
	want := IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 13}
	if got != want {
		t.Fatalf("Issue reference = %#v, want %#v", got, want)
	}
}

func TestParseIssueRefRejectsNonCanonicalIdentityParts(t *testing.T) {
	for name, raw := range map[string]string{
		"owner underscore":  "github://my_org/kudo/issues/1",
		"owner edge hyphen": "github://-owner/kudo/issues/1",
		"owner port":        "github://owner:8080/kudo/issues/1",
		"repository dot":    "github://owner/./issues/1",
		"leading zero":      "github://owner/kudo/issues/01",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseIssueRef(raw); ok {
				t.Fatalf("non-canonical Issue reference %q を受理した", raw)
			}
		})
	}
}
