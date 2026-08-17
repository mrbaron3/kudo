package contract

import (
	"strconv"
	"strings"
)

// yamlString は任意の UTF-8 string を YAML double-quoted scalar にする。
// すべての string scalar を quote することで implicit type に依存しない。
func yamlString(value string) string {
	return strconv.Quote(value)
}

func writeYAMLString(b *strings.Builder, indent int, key, value string) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(yamlString(value))
	b.WriteByte('\n')
}

func writeYAMLNull(b *strings.Builder, indent int, key string) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(key)
	b.WriteString(": null\n")
}

func writeYAMLStringList(b *strings.Builder, indent int, key string, values []string) {
	prefix := strings.Repeat(" ", indent)
	b.WriteString(prefix)
	b.WriteString(key)
	if len(values) == 0 {
		b.WriteString(": []\n")
		return
	}
	b.WriteString(":\n")
	for _, value := range values {
		b.WriteString(prefix)
		b.WriteString("  - ")
		b.WriteString(yamlString(value))
		b.WriteByte('\n')
	}
}

func validIssueRef(ref IssueRef) bool {
	return validOwner(ref.Owner) && validRepoName(ref.Repository) && ref.Number > 0
}

func validSchemaIdentity(schema, prefix string) bool {
	version, ok := strings.CutPrefix(schema, prefix)
	if !ok || version == "" {
		return false
	}
	for i := range len(version) {
		c := version[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func canonicalMarkdown(content string) string {
	lines := splitLines(content)
	var scanner lineScanner
	kept := make([]bodyLine, 0, len(lines))
	for _, line := range lines {
		if scanner.step(line) == classComment {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[0].text) == "" {
		kept = kept[1:]
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1].text) == "" {
		kept = kept[:len(kept)-1]
	}
	return joinLines(kept)
}

func acceptanceCriteriaPreamble(content string) string {
	lines := splitLines(content)
	var scanner lineScanner
	for i, line := range lines {
		if scanner.step(line) == classText && strings.HasPrefix(line.text, "### ") {
			return canonicalMarkdown(joinLines(lines[:i]))
		}
	}
	return canonicalMarkdown(content)
}

func cloneIssueRef(ref *IssueRef) *IssueRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func cloneIssueRefs(refs []IssueRef) []IssueRef {
	if refs == nil {
		return nil
	}
	cloned := make([]IssueRef, len(refs))
	copy(cloned, refs)
	return cloned
}

func cloneAuthorityRefs(refs []AuthorityRef) []AuthorityRef {
	cloned := make([]AuthorityRef, len(refs))
	for i, ref := range refs {
		cloned[i] = AuthorityRef{Path: ref.Path, Issue: cloneIssueRef(ref.Issue)}
	}
	return cloned
}
