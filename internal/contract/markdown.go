package contract

import "strings"

// 本 file は Issue Contract の検証に必要な最小限の Markdown 走査だけを実装する。
// 一般的な Markdown parser を実装範囲へ広げない（docs/contracts/issue-contract-v1alpha1.md、
// Issue #9 Constraints）。認識するのは行頭の H2/H3 heading、行頭の code fence、
// 行頭で始まる HTML comment のみである。

// bodyLine は正規化済みの 1 行を表す。GitHub API の body は CRLF を含むため、
// 行分割時に行末の \r だけを取り除く。それ以外の空白は保持する。
type bodyLine struct {
	num  int // 1 始まり
	text string
}

func splitLines(body string) []bodyLine {
	raw := strings.Split(body, "\n")
	lines := make([]bodyLine, len(raw))
	for i, t := range raw {
		lines[i] = bodyLine{num: i + 1, text: strings.TrimSuffix(t, "\r")}
	}
	return lines
}

// lineScanner は code fence と HTML comment の内側で heading を誤認しないための
// 走査状態を持つ。
type lineScanner struct {
	inFence     bool
	fenceLine   int
	inComment   bool
	commentLine int
}

// step は 1 行を読み、状態を更新する。返り値は「この行が fence / comment の
// 内側（開始・終了行を含む）かどうか」である。
func (s *lineScanner) step(l bodyLine) (inFence, inComment bool) {
	trimmed := strings.TrimSpace(l.text)

	if s.inComment {
		if strings.Contains(trimmed, "-->") {
			s.inComment = false
		}
		return false, true
	}
	if s.inFence {
		if strings.HasPrefix(l.text, "```") {
			s.inFence = false
		}
		return true, false
	}
	if strings.HasPrefix(l.text, "```") {
		s.inFence = true
		s.fenceLine = l.num
		return true, false
	}
	if strings.HasPrefix(trimmed, "<!--") {
		if !strings.Contains(trimmed, "-->") {
			s.inComment = true
			s.commentLine = l.num
		}
		return false, true
	}
	return false, false
}

// rawSection は本文から切り出した H2 section を表す。
type rawSection struct {
	title string
	line  int
	lines []bodyLine
}

// scanSections は本文を H2 section へ分割する。fence または comment の内側の
// `## ` は heading として扱わない。fence が閉じないまま本文が終わった場合は
// CodeFenceUnclosed を報告する。
func scanSections(lines []bodyLine) (preamble []bodyLine, sections []rawSection, errs []ValidationError) {
	var sc lineScanner
	current := -1
	for _, l := range lines {
		inFence, inComment := sc.step(l)
		if !inFence && !inComment && strings.HasPrefix(l.text, "## ") {
			title := strings.TrimRight(l.text[len("## "):], " \t")
			sections = append(sections, rawSection{title: title, line: l.num})
			current = len(sections) - 1
			continue
		}
		if current < 0 {
			preamble = append(preamble, l)
		} else {
			sections[current].lines = append(sections[current].lines, l)
		}
	}
	if sc.inFence {
		errs = append(errs, ValidationError{
			Code:    CodeFenceUnclosed,
			Line:    sc.fenceLine,
			Message: "code fence が閉じていない",
		})
	}
	return preamble, sections, errs
}

// contentLines は fence 外の HTML comment を除いた実質的な内容行を返す。
// fence の内側はすべて内容として扱う。
func contentLines(lines []bodyLine) []bodyLine {
	var sc lineScanner
	var out []bodyLine
	for _, l := range lines {
		inFence, inComment := sc.step(l)
		if inComment {
			continue
		}
		if !inFence && strings.TrimSpace(l.text) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// joinLines は section 本文を改行区切りで復元する。
func joinLines(lines []bodyLine) string {
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n")
}

// rawCriterion は Acceptance Criteria section 内の H3 criterion を表す。
type rawCriterion struct {
	id    string
	line  int
	lines []bodyLine
}

// scanCriteria は Acceptance Criteria section の本文を H3 単位へ分割する。
// 最初の H3 より前の行は criterion に属さない前置きとして無視する
// （契約との対応検証は criterion ID の集合だけを対象にする）。
func scanCriteria(lines []bodyLine) []rawCriterion {
	var sc lineScanner
	var out []rawCriterion
	current := -1
	for _, l := range lines {
		inFence, inComment := sc.step(l)
		if !inFence && !inComment && strings.HasPrefix(l.text, "### ") {
			id := strings.TrimRight(l.text[len("### "):], " \t")
			out = append(out, rawCriterion{id: id, line: l.num})
			current = len(out) - 1
			continue
		}
		if current >= 0 {
			out[current].lines = append(out[current].lines, l)
		}
	}
	return out
}

// extractContractBlock は Contract section から唯一の ```yaml fenced block を取り出す。
// block 以外に実質的な内容がある場合、block が無い・複数ある場合はエラーを返す。
// found は「唯一の yaml block を特定できたか」を表し、block が空でも true になる。
// blockLine は開始 fence の行番号である。
func extractContractBlock(sec rawSection) (block []bodyLine, blockLine int, found bool, errs []ValidationError) {
	type fence struct {
		info  string
		line  int
		lines []bodyLine
	}
	var fences []fence
	var outside []bodyLine

	var sc lineScanner
	openIdx := -1
	for _, l := range sec.lines {
		wasInFence := sc.inFence
		inFence, inComment := sc.step(l)
		switch {
		case inFence && !wasInFence:
			// 開始 fence 行
			fences = append(fences, fence{info: strings.TrimSpace(strings.TrimPrefix(l.text, "```")), line: l.num})
			openIdx = len(fences) - 1
		case wasInFence && !sc.inFence:
			// 終了 fence 行
			openIdx = -1
		case inFence && openIdx >= 0:
			fences[openIdx].lines = append(fences[openIdx].lines, l)
		case inComment:
			// fence 外の comment は Contract section で許可する（template が使用する）
		default:
			outside = append(outside, l)
		}
	}

	for _, l := range outside {
		if strings.TrimSpace(l.text) != "" {
			errs = append(errs, ValidationError{
				Code:    CodeContractExtraContent,
				Line:    l.num,
				Section: sectionContract,
				Message: "Contract section には YAML fenced block 以外の内容を置かない",
			})
		}
	}

	switch len(fences) {
	case 0:
		errs = append(errs, ValidationError{
			Code:    CodeContractBlockMissing,
			Line:    sec.line,
			Section: sectionContract,
			Message: "Contract section に ```yaml fenced block が無い",
		})
		return nil, 0, false, errs
	case 1:
		// fall through
	default:
		errs = append(errs, ValidationError{
			Code:    CodeContractBlockDuplicate,
			Line:    fences[1].line,
			Section: sectionContract,
			Message: "Contract section の fenced block は 1 つだけ置く",
		})
		return nil, 0, false, errs
	}

	if fences[0].info != "yaml" {
		errs = append(errs, ValidationError{
			Code:    CodeContractBlockMissing,
			Line:    fences[0].line,
			Section: sectionContract,
			Message: "Contract block の fence info は `yaml` を指定する",
		})
		return nil, 0, false, errs
	}
	return fences[0].lines, fences[0].line, true, errs
}
