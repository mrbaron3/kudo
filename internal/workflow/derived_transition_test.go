package workflow

import (
	"errors"
	"testing"
)

func derivedPhaseVocabulary() []Phase {
	return append(DerivedPhases(), PhaseNone)
}

// 正常 flow と review loop の動きは宣言済みであり、gate を飛び越す動きは拒否される。
func TestValidateDerivedTransitionDeclaresTheNormalFlow(t *testing.T) {
	allowed := [][2]Phase{
		{PhaseNone, PhaseCandidate},
		{PhaseCandidate, PhaseClaimed},
		{PhaseClaimed, PhaseAuthoringTests},
		{PhaseAuthoringTests, PhaseAwaitingTestReview},
		{PhaseAwaitingTestReview, PhaseImplementing},
		{PhaseAwaitingTestReview, PhaseAuthoringTests},
		{PhaseImplementing, PhaseAwaitingFinalReview},
		{PhaseImplementing, PhaseAuthoringTests},
		{PhaseAwaitingFinalReview, PhaseFinalizingPullRequest},
		{PhaseAwaitingFinalReview, PhaseImplementing},
		{PhaseFinalizingPullRequest, PhaseMergingPullRequest},
		{PhaseMergingPullRequest, PhaseMerged},
		{PhaseSuperseded, PhaseClaimed},
	}
	for _, move := range allowed {
		if err := ValidateDerivedTransition(move[0], move[1]); err != nil {
			t.Fatalf("%s → %s: %v", move[0], move[1], err)
		}
	}

	rejected := [][2]Phase{
		// test gate を飛ばして実装へ入る。
		{PhaseAuthoringTests, PhaseImplementing},
		// final review を飛ばして PR を確定する。
		{PhaseImplementing, PhaseFinalizingPullRequest},
		// final approve を飛ばして merge する。
		{PhaseAwaitingFinalReview, PhaseMergingPullRequest},
		{PhaseAuthoringTests, PhaseMerged},
		// claim を飛ばして作業を始める。
		{PhaseCandidate, PhaseAuthoringTests},
		// merged は terminal である。
		{PhaseMerged, PhaseImplementing},
		{PhaseMerged, PhaseSuperseded},
		// branch が消えて Run が無かったことになる。
		{PhaseClaimed, PhaseNone},
		{PhaseImplementing, PhaseNone},
	}
	for _, move := range rejected {
		err := ValidateDerivedTransition(move[0], move[1])
		if err == nil {
			t.Fatalf("%s → %s が受理された", move[0], move[1])
		}
		if !errors.Is(err, TransitionNotAllowed) {
			t.Fatalf("%s → %s の error が分類できない: %v", move[0], move[1], err)
		}
	}
}

// 停止・打ち切り・再開はどの phase からでも観測されうる。
func TestValidateDerivedTransitionAllowsStopSupersedeAndResume(t *testing.T) {
	for _, phase := range derivedPhaseVocabulary() {
		if err := ValidateDerivedTransition(phase, phase); err != nil {
			t.Fatalf("%s の据え置き: %v", phase, err)
		}
		if err := ValidateDerivedTransition(phase, PhaseNeedsHuman); err != nil {
			t.Fatalf("%s → needs_human: %v", phase, err)
		}
		if err := ValidateDerivedTransition(PhaseNeedsHuman, phase); err != nil {
			t.Fatalf("needs_human → %s: %v", phase, err)
		}
		if phase == PhaseMerged {
			continue
		}
		if err := ValidateDerivedTransition(phase, PhaseSuperseded); err != nil {
			t.Fatalf("%s → superseded: %v", phase, err)
		}
	}
}

// 語彙に無い phase は「宣言されていない動き」として拒否する。durable 語彙にしか
// 無い publish 中の phase を導出結果として渡されても受理しない。
func TestValidateDerivedTransitionRejectsUnknownPhases(t *testing.T) {
	for _, move := range [][2]Phase{
		{PhaseNew, PhaseCandidate},
		{PhaseCandidate, "reviewing"},
		{PhasePublishingTestHead, PhaseAwaitingTestReview},
		{PhaseAwaitingTestReview, PhasePublishingFinalHead},
	} {
		if err := ValidateDerivedTransition(move[0], move[1]); !errors.Is(err, TransitionNotAllowed) {
			t.Fatalf("%s → %s の結果 = %v", move[0], move[1], err)
		}
	}
}

// 表の全 phase が起点として宣言されている（漏れた phase が暗黙に「動けない」へ
// 落ちないことを固定する）。
func TestDeclaredDerivedTransitionsCoverEveryDerivedPhase(t *testing.T) {
	for _, phase := range derivedPhaseVocabulary() {
		if _, ok := declaredDerivedTransitions[phase]; !ok {
			t.Fatalf("phase %q が transition 表に無い", phase)
		}
	}
	for phase := range declaredDerivedTransitions {
		if !derivedPhase(phase) {
			t.Fatalf("transition 表の %q が導出語彙に無い", phase)
		}
	}
}
