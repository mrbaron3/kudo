package contract

import "strings"

// 本 file は Contract block 専用の制限付き YAML subset parser を実装する。
// 汎用 YAML library を使わないのは次の理由による（Issue #9 Advisory Hints への回答）。
//
//   - 重複 key・未知 field・行位置を仕様どおり厳密に検出する必要があり、
//     subset parser なら検出条件が実装から直接読み取れる
//   - anchor / alias / tag / flow mapping / 複数行 scalar / quoting といった
//     「同じ値の別表現」を契約から排除し、canonical 化（後続 Task）の入力を安定させる
//   - v1alpha1 の Contract block は scalar と文字列 list だけで構成され、
//     外部依存を追加するほどの文法を持たない
//
// 許可する構文は次のみとする。
//
//	key: value          # 値付き field（value は空白を含まない printable ASCII）
//	key:                # 1 件以上の item 行が続く sequence
//	  - value           # sequence item（2 space indent 固定）
//	key: []             # 空 sequence の明示
//	# comment           # 行全体の comment（template が使用する）
//	（空行）
//
// 空行と行全体 comment は sequence を終わらせない。`key:` と item 行の間、および
// item 行の間に挟んでもよく、sequence は次の field 行か block 末尾で閉じる。
// 値の quoting、行内 comment、tab、行末空白は受理しない。

// yamlEntry は Contract block の 1 field を表す。
type yamlEntry struct {
	key    string
	line   int
	isSeq  bool
	scalar string     // isSeq == false のとき有効
	items  []yamlItem // isSeq == true のとき有効。`[]` は空 slice
}

type yamlItem struct {
	value string
	line  int
}

func isYAMLKeyChar(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case !first && c >= '0' && c <= '9':
		return true
	default:
		return false
	}
}

// validScalar は値として許可する文字列かどうかを検証する。
// 空白を含む値・quoting・comment 記号を排除し、表現の揺れを作らない。
func validScalar(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '!' || c > '~' {
			return false
		}
		switch c {
		case '#', '"', '\'', '`':
			return false
		}
	}
	return true
}

func parseYAMLBlock(lines []bodyLine) ([]yamlEntry, []ValidationError) {
	var entries []yamlEntry
	var errs []ValidationError
	seen := map[string]bool{}
	seqOpen := false // 直前の entry が item 待ちの sequence か

	syntaxErr := func(l bodyLine, msg string) {
		errs = append(errs, ValidationError{
			Code:    CodeYAMLSyntax,
			Line:    l.num,
			Section: sectionContract,
			Message: msg,
		})
	}

	closeSeq := func() {
		if seqOpen {
			last := &entries[len(entries)-1]
			if len(last.items) == 0 {
				errs = append(errs, ValidationError{
					Code:    CodeYAMLSyntax,
					Line:    last.line,
					Section: sectionContract,
					Field:   last.key,
					Message: "空の sequence は `" + last.key + ": []` と書く",
				})
			}
			seqOpen = false
		}
	}

	for _, l := range lines {
		text := l.text
		trimmed := strings.TrimSpace(text)

		// 空行と行全体 comment は許可する
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.TrimRight(text, " \t") != text {
			syntaxErr(l, "行末の空白は許可しない")
			continue
		}

		// sequence item
		if strings.HasPrefix(text, "  - ") {
			if !seqOpen {
				syntaxErr(l, "sequence item の前に `key:` 行が必要")
				continue
			}
			value := text[len("  - "):]
			if !validScalar(value) {
				syntaxErr(l, "item の値は quoting・空白・comment を含まない printable ASCII で書く")
				continue
			}
			last := &entries[len(entries)-1]
			last.items = append(last.items, yamlItem{value: value, line: l.num})
			continue
		}

		// ここからは field 行のみを許可する
		if strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") {
			syntaxErr(l, "許可されない indent。field は行頭から、item は `  - ` で書く")
			continue
		}

		closeSeq()

		colon := strings.IndexByte(text, ':')
		if colon <= 0 {
			syntaxErr(l, "`key: value` または `key:` の形式で書く")
			continue
		}
		key := text[:colon]
		okKey := true
		for i := 0; i < len(key); i++ {
			if !isYAMLKeyChar(key[i], i == 0) {
				okKey = false
				break
			}
		}
		if !okKey {
			syntaxErr(l, "key は英字で始まる英数字のみ許可する")
			continue
		}

		if seen[key] {
			// 重複を報告したうえで構文としては読み進める。
			// field 解釈では各 key の最初の出現だけを採用する
			errs = append(errs, ValidationError{
				Code:    CodeYAMLDuplicateKey,
				Line:    l.num,
				Section: sectionContract,
				Field:   key,
				Message: "key `" + key + "` が重複している",
			})
		}
		seen[key] = true

		rest := text[colon+1:]
		switch {
		case rest == "":
			entries = append(entries, yamlEntry{key: key, line: l.num, isSeq: true})
			seqOpen = true
		case strings.HasPrefix(rest, " "):
			value := rest[1:]
			if value == "[]" {
				entries = append(entries, yamlEntry{key: key, line: l.num, isSeq: true, items: []yamlItem{}})
			} else if !validScalar(value) {
				// 値を解釈できない field は entry を作らない（field 欠落として扱う）
				syntaxErr(l, "値は quoting・空白・comment を含まない printable ASCII で書く")
			} else {
				entries = append(entries, yamlEntry{key: key, line: l.num, scalar: value})
			}
		default:
			syntaxErr(l, "`:` の後には space を 1 つ置く")
		}
	}
	closeSeq()
	return entries, errs
}
