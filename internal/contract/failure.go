package contract

import (
	"errors"
	"fmt"
)

// FailureClass は attempt を失敗させた execution / transport failure の分類である。
// review の品質 verdict とは別の値空間であり、相互に変換しない。
type FailureClass string

const (
	FailureTimeout                 FailureClass = "timeout"
	FailureRateLimit               FailureClass = "rate_limit"
	FailureNetwork                 FailureClass = "network"
	FailureProviderCrash           FailureClass = "provider_crash"
	FailureProviderInvalidResponse FailureClass = "provider_invalid_response"
	FailureGitHubTransport         FailureClass = "github_transport"
)

var failureClasses = map[FailureClass]bool{
	FailureTimeout:                 true,
	FailureRateLimit:               true,
	FailureNetwork:                 true,
	FailureProviderCrash:           true,
	FailureProviderInvalidResponse: true,
	FailureGitHubTransport:         true,
}

// AttemptFailure は 1 attempt の execution failure を表す。
//
// 品質 verdict の field を持たず、ReviewVerdict へ変換する API も提供しない。
// timeout、provider crash、GitHub transport failure を request_changes や needs_human へ
// 読み替えると、外部要因の失敗が実装の欠陥として記録され、gate 判断が壊れる。
type AttemptFailure struct {
	Class     FailureClass
	AttemptID string
	Evidence  string
}

// Validate は failure record として保存できる形かを検証する。
func (f AttemptFailure) Validate() error {
	if !failureClasses[f.Class] {
		return fmt.Errorf("failure class が不正: %q", f.Class)
	}
	if !validProtocolID(f.AttemptID) {
		return fmt.Errorf("attemptId が不正: %q", f.AttemptID)
	}
	if !validCanonicalText(f.Evidence) {
		return errors.New("evidence が空または canonical text でない")
	}
	return nil
}

// TerminalOutcome は bounded retry を使い切った failure だけを Operation outcome へ落とす。
// retry 余地がある間、および failure record 自体が不正な間は ok = false を返し、
// caller は同じ logical Operation へ次の attempt を積む。
func (f AttemptFailure) TerminalOutcome(retryBudgetExhausted bool) (OperationOutcome, bool) {
	if !retryBudgetExhausted || f.Validate() != nil {
		return "", false
	}
	return OutcomeFailedTerminal, true
}
