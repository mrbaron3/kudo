package contract

import "fmt"

// Code は validation error の機械可読な分類を表す。値は安定した識別子であり、
// 変更は versioned contract の変更として扱う。
type Code string

const (
	// 入力全体
	CodeRepositoryRefInvalid Code = "repository_ref_invalid"
	CodePreambleContent      Code = "preamble_content"

	// H2 section 構造
	CodeSectionMissing    Code = "section_missing"
	CodeSectionDuplicate  Code = "section_duplicate"
	CodeSectionUnknown    Code = "section_unknown"
	CodeSectionOutOfOrder Code = "section_out_of_order"
	CodeSectionEmpty      Code = "section_empty"
	CodeFenceUnclosed     Code = "fence_unclosed"
	CodeCommentAmbiguous  Code = "comment_ambiguous"

	// Contract section の fenced block
	CodeContractBlockMissing   Code = "contract_block_missing"
	CodeContractBlockDuplicate Code = "contract_block_duplicate"
	CodeContractExtraContent   Code = "contract_extra_content"

	// Contract YAML block
	CodeYAMLSyntax       Code = "yaml_syntax"
	CodeYAMLDuplicateKey Code = "yaml_duplicate_key"
	CodeYAMLUnknownField Code = "yaml_unknown_field"
	CodeFieldMissing     Code = "field_missing"
	CodeFieldType        Code = "field_type"
	CodeEnumInvalid      Code = "enum_invalid"

	// reference
	CodeRefInvalid         Code = "ref_invalid"
	CodeRefDuplicate       Code = "ref_duplicate"
	CodeRefCrossRepository Code = "ref_cross_repository"

	// Acceptance Criteria の対応
	CodeACIDsEmpty           Code = "acceptance_criteria_ids_empty"
	CodeACIDDuplicate        Code = "acceptance_criteria_id_duplicate"
	CodeACCriterionMissing   Code = "acceptance_criterion_missing"
	CodeACCriterionUnlisted  Code = "acceptance_criterion_unlisted"
	CodeACCriterionDuplicate Code = "acceptance_criterion_duplicate"
)

// ValidationError は claim rejection の原因 1 件を構造化して表す。
// transport failure は本 package の範囲外であり、ここへ混在させない。
type ValidationError struct {
	Code    Code
	Line    int    // 1 始まりの行番号。特定できない場合は 0
	Section string // 関連する H2 section title。全体に関わる場合は空
	Field   string // 関連する Contract block field。無関係な場合は空
	Message string
}

// Error は人間向けの説明を返す。機械判定には Code を使う。
func (e ValidationError) Error() string {
	pos := ""
	if e.Line > 0 {
		pos = fmt.Sprintf(" (line %d)", e.Line)
	}
	scope := ""
	if e.Section != "" {
		scope = fmt.Sprintf(" [%s]", e.Section)
	}
	if e.Field != "" {
		scope += fmt.Sprintf(" field=%s", e.Field)
	}
	return fmt.Sprintf("%s%s%s: %s", e.Code, scope, pos, e.Message)
}
