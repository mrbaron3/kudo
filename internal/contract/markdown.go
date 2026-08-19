package contract

import "strings"

// 本 file は Issue Contract の検証に必要な最小限の Markdown 走査だけを実装する。
// 一般的な Markdown parser を実装範囲へ広げない（docs/spec/05_design/contracts/issue-contract-v1alpha1.md、
// Issue #9 Constraints）。認識するのは行頭の H2/H3 heading、code fence、
// 行頭で始まる HTML comment のみである。
//
// 「人と Kudo の双方が同じ契約を読む」ことを、GitHub の描画を完全に再現するのではなく
// 解釈が一意に定まる本文だけを受理することで担保する。CommonMark で描画結果が
// 文脈（list、indent、lazy continuation）に依存する書き方は、推測して受理せず
// error として拒否する。
//
//   - heading と fence marker は列 0 に置く。1〜3 space indent した marker らしき行は
//     GitHub では文脈次第で heading にも code block にもなるため拒否する
//   - fence は backtick または tilde 3 個以上で開き、同じ文字・同じ長さ以上で
//     info string を持たない列 0 の行だけが閉じる
//   - HTML comment は行全体が comment の行だけを許可する。可視内容と混在する行は拒否する。
//     inline code span も可視内容として数える
//   - inline code span と HTML comment は同一行では先に現れた側が優先される。
//     code span が先ならその内側の `<!--` は comment ではなく、`<!--` が先なら
//     その内側の backtick は code span を開かない

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

// lineClass は走査中の 1 行の分類を表す。
type lineClass int

const (
	classText lineClass = iota
	classFenceOpen
	classFenceContent
	classFenceClose
	classComment
	classCommentAmbiguous
	classIndentedMarker
)

// indentWidth は行頭の space 数を返す。
func indentWidth(text string) int {
	n := 0
	for n < len(text) && text[n] == ' ' {
		n++
	}
	return n
}

// fenceMarker は列 0 から始まる fence marker（backtick または tilde 3 個以上）を判定し、
// fence 文字・個数・info string を返す。
func fenceMarker(text string) (char byte, count int, info string, ok bool) {
	if len(text) == 0 || (text[0] != '`' && text[0] != '~') {
		return 0, 0, "", false
	}
	c := text[0]
	j := 0
	for j < len(text) && text[j] == c {
		j++
	}
	if j < 3 {
		return 0, 0, "", false
	}
	return c, j, strings.TrimSpace(text[j:]), true
}

// indentedMarker は 1〜3 space indent された heading / fence marker かを判定する。
// GitHub ではこの indent 幅の marker が文脈次第で heading にも code block にもなるため、
// 解釈を推測せず拒否する。4 space 以上は GitHub でも block marker にならないため対象外。
func indentedMarker(text string) bool {
	n := indentWidth(text)
	if n < 1 || n > 3 {
		return false
	}
	rest := text[n:]
	if _, _, _, ok := fenceMarker(rest); ok {
		return true
	}
	return strings.HasPrefix(rest, "## ") || strings.HasPrefix(rest, "### ")
}

// closingRun は from 以降で長さ runLen ちょうどの backtick run を探し、その直後の
// index を返す。見つからない場合 ok は false になる。
func closingRun(text string, from, runLen int) (end int, ok bool) {
	for k := from; k < len(text); {
		if text[k] != '`' {
			k++
			continue
		}
		m := k
		for m < len(text) && text[m] == '`' {
			m++
		}
		if m-k == runLen {
			return m, true
		}
		k = m
	}
	return 0, false
}

// inlineScan は 1 行を左から 1 度だけ走査し、行頭時点の comment 状態 inComment を
// 起点として、comment の外に可視内容があるか (visible)、行末で comment が開いたままか
// (open)、この行が comment を含むか (hasComment) を返す。
//
// inline code span と HTML comment はどちらも inline 構造で、CommonMark では
// 先に現れた側が優先される。走査を 1 本化してこの優先順位をそのまま実装する。
//
//   - backtick run が先: 同じ長さの run で閉じれば code span。内側の `<!--` は
//     comment ではない。閉じない run は literal text。どちらも可視内容として数える
//   - `<!--` が先: 対応する `-->` までが comment。内側の backtick は code span を
//     開かない。CommonMark に合わせて `<!-->` と `<!--->` は空 comment として扱う
//
// code span を先に mask してから comment を探す実装にはしない。その順序では
// (1) code span だけが可視内容の行で可視内容が空白に化けて混在を見落とし、
// (2) comment 内の backtick が comment 外の backtick と対になって閉じ `-->` ごと
// mask され、comment 状態が以降の本文へ漏れる。
func inlineScan(text string, inComment bool) (visible, open, hasComment bool) {
	var outside strings.Builder
	open = inComment
	hasComment = inComment
	for i := 0; i < len(text); {
		if open {
			j := strings.Index(text[i:], "-->")
			if j < 0 {
				break
			}
			i += j + len("-->")
			open = false
			continue
		}
		switch {
		case text[i] == '`':
			j := i
			for j < len(text) && text[j] == '`' {
				j++
			}
			end, ok := closingRun(text, j, j-i)
			if !ok {
				end = j
			}
			outside.WriteString(text[i:end])
			i = end
		case strings.HasPrefix(text[i:], "<!--"):
			hasComment = true
			rest := text[i+len("<!--"):]
			switch {
			case strings.HasPrefix(rest, ">"):
				i += len("<!-->")
			case strings.HasPrefix(rest, "->"):
				i += len("<!--->")
			default:
				open = true
				i += len("<!--")
			}
		default:
			outside.WriteByte(text[i])
			i++
		}
	}
	return strings.TrimSpace(outside.String()) != "", open, hasComment
}

// lineScanner は code fence と HTML comment の内側で heading を誤認しないための
// 走査状態を持つ。
type lineScanner struct {
	inFence   bool
	fenceLine int
	fenceChar byte
	fenceLen  int
	fenceInfo string
	inComment bool
}

// step は 1 行を読んで状態を更新し、その行の分類を返す。
func (s *lineScanner) step(l bodyLine) lineClass {
	if s.inComment {
		visible, open, _ := inlineScan(l.text, true)
		s.inComment = open
		if visible {
			return classCommentAmbiguous
		}
		return classComment
	}
	if s.inFence {
		// 閉じ fence は開き fence と同じ文字・同じ長さ以上で、info string を持たない
		if c, n, info, ok := fenceMarker(l.text); ok && c == s.fenceChar && n >= s.fenceLen && info == "" {
			s.inFence = false
			return classFenceClose
		}
		return classFenceContent
	}
	if c, n, info, ok := fenceMarker(l.text); ok {
		s.inFence = true
		s.fenceLine = l.num
		s.fenceChar = c
		s.fenceLen = n
		s.fenceInfo = info
		return classFenceOpen
	}
	if indentedMarker(l.text) {
		return classIndentedMarker
	}

	visible, open, hasComment := inlineScan(l.text, false)
	if !hasComment {
		return classText
	}
	if visible {
		// 可視内容と混在する行は拒否する。GitHub では列 0 以外の `<!--` は
		// HTML block を開始しないため、comment 状態へは遷移させない
		return classCommentAmbiguous
	}
	if indentWidth(l.text) > 0 {
		// indent された comment は文脈次第で code block になる
		return classIndentedMarker
	}
	s.inComment = open
	return classComment
}

// rawSection は本文から切り出した H2 section を表す。
type rawSection struct {
	title string
	line  int
	lines []bodyLine
}

// scanSections は本文を H2 section へ分割する。fence または comment の内側の
// `## ` は heading として扱わない。fence が閉じないまま本文が終わった場合と、
// comment と可視内容が同一行に混在する場合はエラーを報告する。
func scanSections(lines []bodyLine) (preamble []bodyLine, sections []rawSection, errs []ValidationError) {
	var sc lineScanner
	current := -1
	for _, l := range lines {
		class := sc.step(l)
		// 違反行も section の内容として保持する。誤った section_empty を重ねず、
		// 原因行を指す error だけを返すため
		switch class {
		case classCommentAmbiguous:
			errs = append(errs, ValidationError{
				Code:    CodeCommentAmbiguous,
				Line:    l.num,
				Message: "HTML comment と内容を同一行に混在させない",
			})
		case classIndentedMarker:
			errs = append(errs, ValidationError{
				Code:    CodeIndentedMarker,
				Line:    l.num,
				Message: "heading、code fence、HTML comment は列 0 から書く（indent すると GitHub の解釈が文脈で変わる）",
			})
		}
		if class == classText && strings.HasPrefix(l.text, "## ") {
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
		switch sc.step(l) {
		case classComment:
			continue
		case classText:
			if strings.TrimSpace(l.text) == "" {
				continue
			}
		}
		// 混在行と indent marker 行は、それ自体が error だが可視内容は持つため
		// section の内容として数える（誤った section_empty を重ねない）
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
		class := sc.step(l)
		if class == classText && strings.HasPrefix(l.text, "### ") {
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
	for _, l := range sec.lines {
		switch sc.step(l) {
		case classFenceOpen:
			fences = append(fences, fence{info: sc.fenceInfo, line: l.num})
		case classFenceContent:
			fences[len(fences)-1].lines = append(fences[len(fences)-1].lines, l)
		case classFenceClose:
			// fence の終了。次の open まで何もしない
		case classComment, classCommentAmbiguous, classIndentedMarker:
			// fence 外の comment は Contract section で許可する（template が使用する）。
			// 混在行と indent marker 行は scanSections が報告済み
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
