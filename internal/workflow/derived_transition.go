package workflow

import (
	"fmt"
	"slices"
)

// declaredDerivedTransitions は、同じ Run を続けて観測したときに現れてよい phase の
// 動きである。宣言していない動きは gate の飛び越しであり、進行として受理しない。
//
// 表は docs/spec/05_design/02_workflow.md の Normal flow と各 review gate の loop に対応する。
// 停止（needs_human）、打ち切り（superseded）、再開（needs_human からの復帰）は
// 個別の行ではなく allowedDerivedTransition の規則が扱う。どの phase からでも
// 起こりうる動きを全行へ書き写すと、行が増えたときに漏れる。
var declaredDerivedTransitions = map[Phase][]Phase{
	PhaseNone:                  {PhaseCandidate},
	PhaseCandidate:             {PhaseNone, PhaseClaimed},
	PhaseClaimed:               {PhaseAuthoringTests},
	PhaseAuthoringTests:        {PhaseAwaitingTestReview},
	PhaseAwaitingTestReview:    {PhaseImplementing, PhaseAuthoringTests},
	PhaseImplementing:          {PhaseAwaitingFinalReview, PhaseAuthoringTests},
	PhaseAwaitingFinalReview:   {PhaseFinalizingPullRequest, PhaseImplementing},
	PhaseFinalizingPullRequest: {PhaseMergingPullRequest},
	PhaseMergingPullRequest:    {PhaseMerged},
	PhaseMerged:                nil,
	// supersede の後始末（PR close、branch 削除）が終わってからだけ新しい Run を作れる。
	PhaseSuperseded: {PhaseNone, PhaseCandidate, PhaseClaimed},
	// needs_human からの復帰先は停止 phase（resume）か新しい Run（supersede）であり、
	// どちらかは記録済み ResumeIdentity の照合が決める。phase の組だけでは絞れない。
	PhaseNeedsHuman: nil,
}

// DerivedTransitionError は宣言されていない phase の動きである。
//
// Code を Unwrap で返すため、errors.Is(err, TransitionNotAllowed) で分類できる。
// durable state machine の TransitionError とは別型にしているのは、こちらが
// 「2 回の観測の間に起きた動き」の判定で、event を持たないためである。
type DerivedTransitionError struct {
	From Phase
	To   Phase
}

func (e *DerivedTransitionError) Error() string {
	return fmt.Sprintf("%s [from=%s to=%s]: 宣言されていない phase の動きである",
		TransitionNotAllowed, e.From, e.To)
}

func (e *DerivedTransitionError) Unwrap() error { return TransitionNotAllowed }

// ValidateDerivedTransition は、同じ Run に対する 2 回の導出結果の間の動きが
// 宣言されたものかを検証する。
//
// pure である。Controller は Operation を dispatch する前と後で導出した phase を
// この関数へ渡す。宣言されていない動きは「Kudo の Operation では起こり得ない
// record surface の変化」であり、gate を飛び越した進行として受理しない。
// 逆に、同じ phase のままであることは常に正しい（観測しても進んでいないだけである）。
func ValidateDerivedTransition(from, to Phase) error {
	if !derivedPhase(from) || !derivedPhase(to) {
		return &DerivedTransitionError{From: from, To: to}
	}
	if allowedDerivedTransition(from, to) {
		return nil
	}
	return &DerivedTransitionError{From: from, To: to}
}

func allowedDerivedTransition(from, to Phase) bool {
	switch {
	case from == to:
		return true
	// 停止はどの phase からでも起こる。merged な Run でも、人間が
	// `ai-needs-human`を付ければ導出は needs_human になる（label が表の先頭行である）。
	case to == PhaseNeedsHuman:
		return true
	// 人間が対応した後の再開先は停止 phase であり、事前に絞れない。
	case from == PhaseNeedsHuman:
		return true
	// semantic input が変われば進行中の Run はどこからでも打ち切られる。merged は
	// 取り消せない terminal なので対象外である。
	case to == PhaseSuperseded:
		return from != PhaseMerged
	}
	return slices.Contains(declaredDerivedTransitions[from], to)
}

func derivedPhase(phase Phase) bool {
	return phase == PhaseNone || slices.Contains(derivedPhases, phase)
}
