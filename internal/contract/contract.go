// Package contract は kudo.issue/v1alpha1 の Issue Contract を strict に parse・検証する。
//
// 正本は docs/contracts/issue-contract-v1alpha1.md と .github/ISSUE_TEMPLATE/kudo-task.md
// である。本 package は GitHub API へ接続せず、Issue 本文の文字列だけを入力にする。
// digest 生成と reference の内容解決は後続 Task（Issue Revision / Context Manifest）の範囲とする。
package contract

import "fmt"

// RepositoryRef は Task Issue が属する repository の identity を表す。
// Issue 本文には repository を自己申告させないため、呼び出し側が
// GitHub API または検証済み event envelope の値を明示的に渡す。
type RepositoryRef struct {
	Owner string
	Name  string
}

// Kind は Contract block の kind field の値を表す。
type Kind string

// KindTask は v1alpha1 で実行可能な唯一の kind である。
const KindTask Kind = "task"

// Readiness は Contract block の readiness field の値を表す。
type Readiness string

// v1alpha1 で許可される readiness の値。実行可能なのは ReadinessReady のみである。
const (
	ReadinessDraft   Readiness = "draft"
	ReadinessReady   Readiness = "ready"
	ReadinessBlocked Readiness = "blocked"
)

// IssueRef は github://owner/repository/issues/number 形式の Issue reference を表す。
type IssueRef struct {
	Owner      string
	Repository string
	Number     int
}

// String は canonical な github:// 表記を返す。
func (r IssueRef) String() string {
	return fmt.Sprintf("github://%s/%s/issues/%d", r.Owner, r.Repository, r.Number)
}

// AuthorityRef は authorityRefs の 1 要素を表す。repository 内 relative path か、
// 同一 repository の Issue reference のどちらか一方だけを持つ。
type AuthorityRef struct {
	Path  string
	Issue *IssueRef
}

// String は Contract block に書かれた形式の表記を返す。
func (r AuthorityRef) String() string {
	if r.Issue != nil {
		return r.Issue.String()
	}
	return r.Path
}

// Contract は strict parse 済みの Contract block を表す。
type Contract struct {
	Schema                string
	Kind                  Kind
	Readiness             Readiness
	Parent                *IssueRef // parent: null の場合は nil
	DependsOn             []IssueRef
	AcceptanceCriteriaIDs []string
	AuthorityRefs         []AuthorityRef
}

// IsExecutableReadiness は readiness が実行可能な値かどうかを返す。
// candidate 条件や dependency completion の判定は本 package の範囲外である。
func (c Contract) IsExecutableReadiness() bool {
	return c.Readiness == ReadinessReady
}

// Section は Issue 本文の H2 section を文書順に表す。
type Section struct {
	Title   string
	Line    int    // H2 heading の行番号（1 始まり）
	Content string // heading 行を除く section 本文
}

// Criterion は Acceptance Criteria section 内の 1 criterion を表す。
type Criterion struct {
	ID   string
	Line int    // H3 heading の行番号（1 始まり）
	Body string // heading 行を除く criterion 本文
}

// Task は strict parse に成功した Task Issue 本文の全体を表す。
type Task struct {
	Contract           Contract
	Sections           []Section
	AcceptanceCriteria []Criterion
}

// Section は指定した title の section を返す。存在しない場合は ok が false になる。
func (t *Task) Section(title string) (Section, bool) {
	for _, s := range t.Sections {
		if s.Title == title {
			return s, true
		}
	}
	return Section{}, false
}
