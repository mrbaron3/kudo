package workflow

import (
	"fmt"
	"strings"
)

// DefaultTargetAssignee は routing 対象の既定 assignee である。
// login と label name は configuration で上書きできるが、deployment 内では一意に固定する
// （docs/spec/05_design/04_github-routing.md の Candidate selection）。
const DefaultTargetAssignee = "mrbaron3"

// CandidateFilter は Kudo の対応候補を選ぶ routing 条件のうち、configuration で
// 上書きできる部分である。
//
// open であること、Pull Request でないことは条件に含めない。これらは上書き対象では
// なく、列挙する adapter が常に適用する不変条件だからである。
type CandidateFilter struct {
	Assignee   string
	ReadyLabel string
}

// DefaultCandidateFilter は spec が定める既定の routing 条件を返す。
func DefaultCandidateFilter() CandidateFilter {
	return CandidateFilter{Assignee: DefaultTargetAssignee, ReadyLabel: LabelReady}
}

// Validate は routing 条件が確定しているかを検査する。
//
// 欠けた値を既定で補わないのは、補うと「設定し忘れ」と「既定を選んだ」が同じ結果になり、
// 意図しない assignee の Issue を候補にし得るためである。
func (f CandidateFilter) Validate() error {
	if strings.TrimSpace(f.Assignee) == "" {
		return fmt.Errorf("candidate filter の target assignee が空である")
	}
	if strings.TrimSpace(f.ReadyLabel) == "" {
		return fmt.Errorf("candidate filter の ready label が空である")
	}
	// comma を拒むのは、GitHub の Issue 列挙が label と assignee を comma 区切りの
	// query parameter で渡すためである。値そのものに comma を含めると列挙側が必ず
	// 失敗し、起動時には通った設定が「polling が毎 cycle 失敗し続ける」という無言の
	// 停止になる。起動時検証は後段 adapter の受理集合を満たさなければならない
	// （docs/spec/05_design/03_runtime-platform.md の Configuration contract）。
	if strings.Contains(f.Assignee, ",") {
		return fmt.Errorf("candidate filter の target assignee に comma は使えない: %q", f.Assignee)
	}
	if strings.Contains(f.ReadyLabel, ",") {
		return fmt.Errorf("candidate filter の ready label に comma は使えない: %q", f.ReadyLabel)
	}
	return nil
}
