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
