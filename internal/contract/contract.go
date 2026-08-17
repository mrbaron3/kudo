// Package contract は versioned Issue Contract を strict に parse・compile し、
// immutable な実行入力と Worker Operation / Review protocol の canonical identity を
// 構築する pure core である。identity の比較だけで staleness を判定できるよう、
// exact Issue 観測（audit lineage）と semantic input を分けて扱う。
//
// 正本は docs/contracts/issue-contract-v1alpha1.md、
// docs/contracts/task-context-v1alpha1.md、
// docs/contracts/operation-protocol-v1alpha1.md、
// docs/contracts/review-protocol-v1alpha1.md、.github/ISSUE_TEMPLATE/kudo-task.md である。
// 本 package は GitHub API、filesystem、clock 等の外部境界へ接続しない。
package contract

import (
	"fmt"
	"strings"
)

// repositoryRef は Task Issue が属する repository の identity を表す。
// Issue 本文には repository を自己申告させないため、呼び出し側が
// GitHub API または検証済み event envelope の値を明示的に渡す。
type repositoryRef struct {
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

func (r IssueRef) repositoryRef() repositoryRef {
	return repositoryRef{Owner: r.Owner, Name: r.Repository}
}

// canonical は GitHub の case-insensitive な identity に合わせて owner / repository を
// 小文字へ揃える。同じ Issue を指す reference が表記の case 差分だけで別 digest を
// 生まないよう、Issue identity の同値関係を本 method 一つに集約する。
func (r IssueRef) canonical() IssueRef {
	return IssueRef{
		Owner:      strings.ToLower(r.Owner),
		Repository: strings.ToLower(r.Repository),
		Number:     r.Number,
	}
}

// String は canonical な github:// 表記を返す。
func (r IssueRef) String() string {
	c := r.canonical()
	return fmt.Sprintf("github://%s/%s/issues/%d", c.Owner, c.Repository, c.Number)
}

// repositoryURL は Issue が属する repository の canonical な github:// 表記を返す。
// protocol envelope は repository を独立 field として保持せず Issue から導出する。
// 同じ envelope に食い違いうる identity を二重に持たせないためである。
func (r IssueRef) repositoryURL() string {
	c := r.canonical()
	return fmt.Sprintf("github://%s/%s", c.Owner, c.Repository)
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

// parsedContract は strict parse 済みの Contract block を表す。
// application-facing consumer へは Compiler が TaskContext/ClaimRequirements として公開する。
type parsedContract struct {
	Schema                string
	Kind                  Kind
	Readiness             Readiness
	Parent                *IssueRef // parent: null の場合は nil
	DependsOn             []IssueRef
	AcceptanceCriteriaIDs []string
	AuthorityRefs         []AuthorityRef
}

// isExecutableReadiness は readiness が実行可能な値かどうかを返す。
// candidate 条件や dependency completion の判定は本 package の範囲外である。
func (c parsedContract) isExecutableReadiness() bool {
	return c.Readiness == ReadinessReady
}

// parsedSection は Issue 本文の H2 section を文書順に表す。
type parsedSection struct {
	Title   string
	Line    int    // H2 heading の行番号（1 始まり）
	Content string // heading 行を除く section 本文
}

// parsedCriterion は Acceptance Criteria section 内の 1 criterion を表す。
type parsedCriterion struct {
	ID   string
	Line int    // H3 heading の行番号（1 始まり）
	Body string // heading 行を除く criterion 本文
}

// parsedTask は strict parse に成功した Task Issue 本文の全体を表す。
type parsedTask struct {
	Contract           parsedContract
	Sections           []parsedSection
	AcceptanceCriteria []parsedCriterion
}

// section は指定した title の section を返す。存在しない場合は ok が false になる。
func (t *parsedTask) section(title string) (parsedSection, bool) {
	for _, s := range t.Sections {
		if s.Title == title {
			return s, true
		}
	}
	return parsedSection{}, false
}
