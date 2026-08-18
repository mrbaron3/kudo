package contract

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
		return protocolErr(ProtocolFieldInvalid, "class", "failure class が不正: %q", f.Class)
	}
	if !validProtocolID(f.AttemptID) {
		return protocolErr(ProtocolFieldInvalid, "attemptId", "attempt identifier が不正: %q", f.AttemptID)
	}
	if !validCanonicalText(f.Evidence, MaxCanonicalTextBytes) {
		return protocolErr(canonicalTextCode(f.Evidence, MaxCanonicalTextBytes), "evidence",
			"空、canonical text でない、または上限 %d byte を超えている", MaxCanonicalTextBytes)
	}
	return nil
}

// TerminalOutcome は bounded retry を使い切った failure だけを Operation outcome へ落とす。
//
// ok が false かつ error が nil のときだけ retry 余地があり、caller は同じ logical
// Operation へ次の attempt を積んでよい。record 自体が保存できない形なら error を返す。
// 不正な record を「retry 余地あり」と同じ戻り値へ潰すと、bounded retry が無効化され、
// escalation も failed_terminal も起きないまま attempt が積み続けられる。誤受理は
// caller から見えないため、判定不能はここで拒否する。
func (f AttemptFailure) TerminalOutcome(retryBudgetExhausted bool) (OperationOutcome, bool, error) {
	if err := f.Validate(); err != nil {
		return "", false, err
	}
	if !retryBudgetExhausted {
		return "", false, nil
	}
	return OutcomeFailedTerminal, true, nil
}
