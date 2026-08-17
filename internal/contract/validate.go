package contract

import (
	"path"
	"strconv"
	"strings"
)

// Contract block で許可する field 名。順序は文書内の宣言順に依存しない。
var contractFields = map[string]bool{
	"schema":                true,
	"kind":                  true,
	"readiness":             true,
	"parent":                true,
	"dependsOn":             true,
	"acceptanceCriteriaIds": true,
	"authorityRefs":         true,
}

// IssueContractSchemaV1Alpha1 は Issue Contract の schema identity である。
// Task Context その他の artifact schema とは独立して version 管理する。
const IssueContractSchemaV1Alpha1 = "kudo.issue/v1alpha1"

const schemaV1Alpha1 = IssueContractSchemaV1Alpha1

// parseIssueRef は github://owner/repository/issues/number 形式の reference を解釈する。
func parseIssueRef(s string) (IssueRef, bool) {
	rest, ok := strings.CutPrefix(s, "github://")
	if !ok {
		return IssueRef{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[2] != "issues" {
		return IssueRef{}, false
	}
	owner, repo, num := parts[0], parts[1], parts[3]
	if !validOwner(owner) || !validRepoName(repo) {
		return IssueRef{}, false
	}
	if len(num) == 0 || num[0] == '0' {
		return IssueRef{}, false
	}
	// strconv.Atoi は符号（+31 等）を許容するため、digit だけを明示的に許可し
	// canonical でない表現を排除する
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return IssueRef{}, false
		}
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return IssueRef{}, false
	}
	// GitHub は owner / repository を case-insensitive に扱う。表記の case 差分が
	// 別 identity として下流の digest まで伝播しないよう、parse 境界で正規化する。
	return IssueRef{Owner: owner, Repository: repo, Number: n}.canonical(), true
}

func validOwner(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validRepoName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// sameRepository は GitHub の大文字小文字非区別に合わせて repository identity を比較する。
func sameRepository(r IssueRef, self repositoryRef) bool {
	return strings.EqualFold(r.Owner, self.Owner) && strings.EqualFold(r.Repository, self.Name)
}

// validAuthorityPath は repository 内 relative path として許可される形かを検証する。
func validAuthorityPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// buildContract は yamlEntry 列を typed parsedContract へ変換し、semantic rule を検証する。
// blockLine は fenced block の開始行で、field 欠落エラーの位置に使う。
func buildContract(entries []yamlEntry, blockLine int, self repositoryRef) (parsedContract, []ValidationError) {
	var c parsedContract
	var errs []ValidationError

	addErr := func(code Code, line int, field, msg string) {
		errs = append(errs, ValidationError{Code: code, Line: line, Section: sectionContract, Field: field, Message: msg})
	}

	// 各 key の最初の出現だけを採用する（重複は yaml 層が報告済み）
	byKey := map[string]yamlEntry{}
	for _, e := range entries {
		if !contractFields[e.key] {
			addErr(CodeYAMLUnknownField, e.line, e.key, "未知の field `"+e.key+"` は許可しない")
			continue
		}
		if _, ok := byKey[e.key]; !ok {
			byKey[e.key] = e
		}
	}

	scalarField := func(name string) (yamlEntry, bool) {
		e, ok := byKey[name]
		if !ok {
			addErr(CodeFieldMissing, blockLine, name, "required field `"+name+"` が無い")
			return yamlEntry{}, false
		}
		if e.isSeq {
			addErr(CodeFieldType, e.line, name, "`"+name+"` は単一の値で書く")
			return yamlEntry{}, false
		}
		return e, true
	}
	seqField := func(name string) (yamlEntry, bool) {
		e, ok := byKey[name]
		if !ok {
			addErr(CodeFieldMissing, blockLine, name, "required field `"+name+"` が無い")
			return yamlEntry{}, false
		}
		if !e.isSeq {
			addErr(CodeFieldType, e.line, name, "`"+name+"` は list で書く（空なら `[]`）")
			return yamlEntry{}, false
		}
		return e, true
	}

	if e, ok := scalarField("schema"); ok {
		if e.scalar != schemaV1Alpha1 {
			addErr(CodeEnumInvalid, e.line, "schema", "schema は `"+schemaV1Alpha1+"` のみ許可する")
		} else {
			c.Schema = e.scalar
		}
	}
	if e, ok := scalarField("kind"); ok {
		if e.scalar != string(KindTask) {
			addErr(CodeEnumInvalid, e.line, "kind", "v1alpha1 で実行可能な kind は `task` のみとする")
		} else {
			c.Kind = KindTask
		}
	}
	if e, ok := scalarField("readiness"); ok {
		switch Readiness(e.scalar) {
		case ReadinessDraft, ReadinessReady, ReadinessBlocked:
			c.Readiness = Readiness(e.scalar)
		default:
			addErr(CodeEnumInvalid, e.line, "readiness", "readiness は draft / ready / blocked のいずれかとする")
		}
	}
	if e, ok := scalarField("parent"); ok {
		if e.scalar == "null" {
			c.Parent = nil
		} else if ref, ok := parseIssueRef(e.scalar); ok {
			if !sameRepository(ref, self) {
				addErr(CodeRefCrossRepository, e.line, "parent", "parent は Task と同じ repository の Issue に限定する")
			} else {
				c.Parent = &ref
			}
		} else {
			addErr(CodeRefInvalid, e.line, "parent", "parent は `github://owner/repository/issues/<number>` または `null` で書く")
		}
	}

	if e, ok := seqField("dependsOn"); ok {
		seen := map[string]bool{}
		c.DependsOn = []IssueRef{}
		for _, item := range e.items {
			ref, okRef := parseIssueRef(item.value)
			if !okRef {
				addErr(CodeRefInvalid, item.line, "dependsOn", "dependsOn の要素は `github://owner/repository/issues/<number>` で書く")
				continue
			}
			if !sameRepository(ref, self) {
				addErr(CodeRefCrossRepository, item.line, "dependsOn", "dependsOn は Task と同じ repository の Issue に限定する")
				continue
			}
			key := ref.String()
			if seen[key] {
				addErr(CodeRefDuplicate, item.line, "dependsOn", "dependsOn の reference `"+item.value+"` が重複している")
				continue
			}
			seen[key] = true
			c.DependsOn = append(c.DependsOn, ref)
		}
	}

	if e, ok := seqField("acceptanceCriteriaIds"); ok {
		if len(e.items) == 0 {
			addErr(CodeACIDsEmpty, e.line, "acceptanceCriteriaIds", "acceptanceCriteriaIds は 1 件以上を列挙する")
		}
		seen := map[string]bool{}
		c.AcceptanceCriteriaIDs = []string{}
		for _, item := range e.items {
			if seen[item.value] {
				addErr(CodeACIDDuplicate, item.line, "acceptanceCriteriaIds", "ID `"+item.value+"` が重複している")
				continue
			}
			seen[item.value] = true
			c.AcceptanceCriteriaIDs = append(c.AcceptanceCriteriaIDs, item.value)
		}
	}

	if e, ok := seqField("authorityRefs"); ok {
		seenIssue := map[string]bool{}
		seenPath := map[string]bool{}
		c.AuthorityRefs = []AuthorityRef{}
		for _, item := range e.items {
			if strings.HasPrefix(item.value, "github://") {
				ref, okRef := parseIssueRef(item.value)
				if !okRef {
					addErr(CodeRefInvalid, item.line, "authorityRefs", "GitHub Issue reference は `github://owner/repository/issues/<number>` で書く")
					continue
				}
				if !sameRepository(ref, self) {
					addErr(CodeRefCrossRepository, item.line, "authorityRefs", "GitHub Issue 形式の authorityRefs は Task と同じ repository に限定する")
					continue
				}
				key := ref.String()
				if seenIssue[key] {
					addErr(CodeRefDuplicate, item.line, "authorityRefs", "reference `"+item.value+"` が重複している")
					continue
				}
				seenIssue[key] = true
				c.AuthorityRefs = append(c.AuthorityRefs, AuthorityRef{Issue: &ref})
				continue
			}
			if !validAuthorityPath(item.value) {
				addErr(CodeRefInvalid, item.line, "authorityRefs", "authorityRefs は repository 内 relative path か `github://` Issue reference で書く")
				continue
			}
			if seenPath[item.value] {
				addErr(CodeRefDuplicate, item.line, "authorityRefs", "path `"+item.value+"` が重複している")
				continue
			}
			seenPath[item.value] = true
			c.AuthorityRefs = append(c.AuthorityRefs, AuthorityRef{Path: item.value})
		}
	}

	return c, errs
}

// validateCriteria は Contract block の acceptanceCriteriaIds と
// Acceptance Criteria section の criterion ID の一致を検証する。
// sectionLine は Acceptance Criteria heading の行番号で、section 側に対応物が
// 無いエラーの位置に使う。
func validateCriteria(ids []string, criteria []rawCriterion, sectionLine int) []ValidationError {
	var errs []ValidationError

	inSection := map[string]bool{}
	for _, cr := range criteria {
		if inSection[cr.id] {
			errs = append(errs, ValidationError{
				Code:    CodeACCriterionDuplicate,
				Line:    cr.line,
				Section: sectionAcceptanceCriteria,
				Message: "criterion `" + cr.id + "` が重複して定義されている",
			})
			continue
		}
		inSection[cr.id] = true
	}

	listed := map[string]bool{}
	for _, id := range ids {
		listed[id] = true
		if !inSection[id] {
			errs = append(errs, ValidationError{
				Code:    CodeACCriterionMissing,
				Line:    sectionLine,
				Section: sectionAcceptanceCriteria,
				Field:   "acceptanceCriteriaIds",
				Message: "acceptanceCriteriaIds の `" + id + "` に対応する criterion が section に無い",
			})
		}
	}
	for _, cr := range criteria {
		if !listed[cr.id] && inSection[cr.id] {
			errs = append(errs, ValidationError{
				Code:    CodeACCriterionUnlisted,
				Line:    cr.line,
				Section: sectionAcceptanceCriteria,
				Message: "criterion `" + cr.id + "` が acceptanceCriteriaIds に列挙されていない",
			})
			// 同じ ID の再報告を防ぐ
			listed[cr.id] = true
		}
	}

	// ID の集合が一致する場合だけ順序を検証する。集合が食い違う状態で順序差分も
	// 報告すると、原因ではなく症状を重ねて報告することになる。
	// 順序不一致を受理して Compiler 側で並べ替えると、人が読む Issue の順序と
	// AI へ渡る順序が黙って食い違うため、H2 section の順序規則と同じく拒否する。
	if len(errs) == 0 && len(ids) == len(criteria) {
		for i, cr := range criteria {
			if cr.id != ids[i] {
				errs = append(errs, ValidationError{
					Code:    CodeACCriterionOutOfOrder,
					Line:    cr.line,
					Section: sectionAcceptanceCriteria,
					Message: "criterion `" + cr.id + "` の位置が acceptanceCriteriaIds の宣言順と一致しない",
				})
				break
			}
		}
	}
	return errs
}
