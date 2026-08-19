package workflow

import (
	"errors"
	"fmt"
)

// TransitionCode は transition を拒否した理由の機械可読な分類である。
//
// Controller は error 文字列の一致で分岐しない。code 自身が error を実装するため、
// errors.Is(err, TransitionGateUnsatisfied) のように判定できる。
// contract package の ProtocolCode とは別の値空間である。protocol validation は
// 受け取った envelope の形の話で、こちらは Run の進行順序の話である。
type TransitionCode string

const (
	// TransitionNotAllowed は現在 phase でその event が宣言されていないことを表す。
	TransitionNotAllowed TransitionCode = "transition_not_allowed"
	// TransitionGateUnsatisfied は event 自体は宣言されているが、gate 条件
	// （承認済み head との binding、required checks 等）を満たさないことを表す。
	TransitionGateUnsatisfied TransitionCode = "transition_gate_unsatisfied"
	// TransitionTerminal は Run が終端 phase にあり、これ以上進まないことを表す。
	TransitionTerminal TransitionCode = "transition_terminal"
	// TransitionUnknownPhase は Run が語彙に無い phase を持つことを表す。
	TransitionUnknownPhase TransitionCode = "transition_unknown_phase"
	// TransitionUnknownEvent は event が語彙に無いことを表す。
	TransitionUnknownEvent TransitionCode = "transition_unknown_event"
)

func (c TransitionCode) Error() string { return string(c) }

// TransitionError は拒否された transition の内容である。
type TransitionError struct {
	Code    TransitionCode
	Phase   Phase
	Event   EventKind
	Message string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s [phase=%s event=%s]: %s", e.Code, e.Phase, e.Event, e.Message)
}

// Unwrap は Code を返し、errors.Is での分類を可能にする。
func (e *TransitionError) Unwrap() error { return e.Code }

// InvalidTransition は err が transition の拒否かを判定して内容を返す。
// false は「進めてよい」を意味せず、本 package が返した拒否ではないことだけを表す。
func InvalidTransition(err error) (*TransitionError, bool) {
	var target *TransitionError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func transitionErr(code TransitionCode, phase Phase, event EventKind, format string, args ...any) error {
	return &TransitionError{Code: code, Phase: phase, Event: event, Message: fmt.Sprintf(format, args...)}
}
