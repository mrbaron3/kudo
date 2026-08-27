package workflow

import "testing"

func TestDefaultCandidateFilterMatchesTheRoutingPolicy(t *testing.T) {
	t.Parallel()

	filter := DefaultCandidateFilter()
	if filter.Assignee != "mrbaron3" || filter.ReadyLabel != LabelReady {
		t.Fatalf("DefaultCandidateFilter() = %#v", filter)
	}
	if err := filter.Validate(); err != nil {
		t.Fatalf("既定 filter が Validate に通らない: %v", err)
	}
}

func TestCandidateFilterRejectsIncompleteRoutingIdentity(t *testing.T) {
	t.Parallel()

	for name, filter := range map[string]CandidateFilter{
		"assignee なし": {ReadyLabel: "ai-ready"},
		"label なし":    {Assignee: "mrbaron3"},
		"空白だけ":        {Assignee: " ", ReadyLabel: "ai-ready"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := filter.Validate(); err == nil {
				t.Fatalf("不完全な filter %#v を受理した", filter)
			}
		})
	}
}

// 起動時検証は後段 adapter の受理集合を満たさなければならない。GitHub の Issue 列挙は
// label と assignee を comma 区切りの query parameter で渡すため、値そのものに comma を
// 含む設定は列挙側が必ず拒否する。起動時に通すと、polling が毎 cycle 失敗し続ける
// という無言の停止になる。
func TestCandidateFilterRejectsCommaInIdentity(t *testing.T) {
	t.Parallel()

	for name, filter := range map[string]CandidateFilter{
		"assignee":    {Assignee: "mrbaron3,bot", ReadyLabel: LabelReady},
		"ready label": {Assignee: DefaultTargetAssignee, ReadyLabel: "ready,bot"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := filter.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}
