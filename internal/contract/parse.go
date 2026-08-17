package contract

import "sort"

// H2 section の canonical な title。この順序でそれぞれ 1 回だけ現れる。
const (
	sectionContract           = "Contract"
	sectionOutcome            = "Outcome"
	sectionScope              = "Scope"
	sectionDeliverables       = "Deliverables"
	sectionAcceptanceCriteria = "Acceptance Criteria"
	sectionVerification       = "Verification and Evidence"
	sectionConstraints        = "Constraints and Invariants"
	sectionDecisionAuthority  = "Decision Authority"
	sectionStopConditions     = "Stop and Escalation Conditions"
	sectionAdvisoryHints      = "Advisory Hints"
)

var requiredSections = []string{
	sectionContract,
	sectionOutcome,
	sectionScope,
	sectionDeliverables,
	sectionAcceptanceCriteria,
	sectionVerification,
	sectionConstraints,
	sectionDecisionAuthority,
	sectionStopConditions,
}

// sectionOrder は canonical な並び順を表す。Advisory Hints は任意だが最後に置く。
var sectionOrder = func() map[string]int {
	m := make(map[string]int, len(requiredSections)+1)
	for i, t := range requiredSections {
		m[t] = i
	}
	m[sectionAdvisoryHints] = len(requiredSections)
	return m
}()

// parse は GitHub Issue 本文を kudo.issue/v1alpha1 として strict に解釈する。
//
// self には Issue が属する repository の identity を渡す。本文には repository を
// 自己申告させないため、この値は GitHub API または検証済み event envelope を正とする。
//
// 返り値は、検証エラーが 1 件でもあれば task が nil になり、エラーが無ければ
// 完全に typed な Task が返る。エラーは行番号の昇順（位置を持たないものは先頭）で
// 安定しており、同じ入力は常に同じ結果を返す。
func parse(body string, self repositoryRef) (*parsedTask, []ValidationError) {
	if self.Owner == "" || self.Name == "" {
		return nil, []ValidationError{{
			Code:    CodeRepositoryRefInvalid,
			Message: "repository identity（owner / name）を明示的に渡す",
		}}
	}

	var errs []ValidationError
	lines := splitLines(body)

	preamble, sections, scanErrs := scanSections(lines)
	errs = append(errs, scanErrs...)

	// 最初の H2 より前には空行と comment 以外を置かない
	for _, l := range contentLines(preamble) {
		errs = append(errs, ValidationError{
			Code:    CodePreambleContent,
			Line:    l.num,
			Message: "本文は `## Contract` から開始する",
		})
	}

	// H2 section の集合・重複・順序
	firstSeen := map[string]*rawSection{}
	counted := map[string]bool{}
	prevOrder := -1
	for i := range sections {
		sec := &sections[i]
		order, known := sectionOrder[sec.title]
		if !known {
			errs = append(errs, ValidationError{
				Code:    CodeSectionUnknown,
				Line:    sec.line,
				Section: sec.title,
				Message: "未知の section `" + sec.title + "` は許可しない",
			})
			continue
		}
		if firstSeen[sec.title] != nil {
			errs = append(errs, ValidationError{
				Code:    CodeSectionDuplicate,
				Line:    sec.line,
				Section: sec.title,
				Message: "section `" + sec.title + "` が重複している",
			})
			continue
		}
		firstSeen[sec.title] = sec
		if order <= prevOrder {
			errs = append(errs, ValidationError{
				Code:    CodeSectionOutOfOrder,
				Line:    sec.line,
				Section: sec.title,
				Message: "section `" + sec.title + "` の位置が規定の順序と一致しない",
			})
		} else {
			prevOrder = order
		}
		// required section は空にできない。任意の Advisory Hints は空を許可する
		if !counted[sec.title] {
			counted[sec.title] = true
			if order < len(requiredSections) && len(contentLines(sec.lines)) == 0 {
				errs = append(errs, ValidationError{
					Code:    CodeSectionEmpty,
					Line:    sec.line,
					Section: sec.title,
					Message: "required section `" + sec.title + "` に内容が無い",
				})
			}
		}
	}
	for _, title := range requiredSections {
		if firstSeen[title] == nil {
			errs = append(errs, ValidationError{
				Code:    CodeSectionMissing,
				Section: title,
				Message: "required section `" + title + "` が無い",
			})
		}
	}

	// Contract block の strict parse と semantic validation
	var contract parsedContract
	var contractOK bool
	if sec := firstSeen[sectionContract]; sec != nil {
		block, blockLine, found, blockErrs := extractContractBlock(*sec)
		errs = append(errs, blockErrs...)
		if found {
			entries, yamlErrs := parseYAMLBlock(block)
			errs = append(errs, yamlErrs...)
			var buildErrs []ValidationError
			contract, buildErrs = buildContract(entries, blockLine, self)
			errs = append(errs, buildErrs...)
			contractOK = len(yamlErrs) == 0 && len(buildErrs) == 0
		}
	}

	// Acceptance Criteria の対応検証
	var criteria []rawCriterion
	if sec := firstSeen[sectionAcceptanceCriteria]; sec != nil {
		criteria = scanCriteria(sec.lines)
		if contractOK {
			errs = append(errs, validateCriteria(contract.AcceptanceCriteriaIDs, criteria, sec.line)...)
		}
	}

	// 位置を持つエラーを行番号順に並べる。同一行内の順序は検出順を保つ
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })

	if len(errs) > 0 {
		return nil, errs
	}

	task := &parsedTask{Contract: contract}
	for _, sec := range sections {
		task.Sections = append(task.Sections, parsedSection{
			Title:   sec.title,
			Line:    sec.line,
			Content: joinLines(sec.lines),
		})
	}
	for _, cr := range criteria {
		task.AcceptanceCriteria = append(task.AcceptanceCriteria, parsedCriterion{
			ID:   cr.id,
			Line: cr.line,
			Body: joinLines(cr.lines),
		})
	}
	return task, nil
}
