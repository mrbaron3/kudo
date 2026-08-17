package contract

import (
	"reflect"
	"testing"
)

func sampleAttemptFailure() AttemptFailure {
	return AttemptFailure{
		Class:     FailureTimeout,
		AttemptID: "attempt-01",
		Evidence:  "provider process が 45m0s の timeout を超えた",
	}
}

// timeout、provider crash、GitHub transport failure は execution failure であり、
// 品質判断ではない。retry budget を使い切るまでは terminal Result にもしない。
func TestExecutionFailureNeverBecomesQualityVerdict(t *testing.T) {
	classes := []FailureClass{
		FailureTimeout,
		FailureRateLimit,
		FailureNetwork,
		FailureProviderCrash,
		FailureProviderInvalidResponse,
		FailureGitHubTransport,
	}
	for _, class := range classes {
		t.Run(string(class), func(t *testing.T) {
			failure := sampleAttemptFailure()
			failure.Class = class
			if err := failure.Validate(); err != nil {
				t.Fatalf("valid な attempt failure を拒否した: %v", err)
			}

			outcome, ok, err := failure.TerminalOutcome(false)
			if err != nil {
				t.Fatalf("valid な failure で error を返した: %v", err)
			}
			if ok {
				t.Fatalf("retry budget が残っているのに terminal outcome %q へ落とした", outcome)
			}
			outcome, ok, err = failure.TerminalOutcome(true)
			if err != nil || !ok || outcome != OutcomeFailedTerminal {
				t.Fatalf("TerminalOutcome = (%q, %v, %v), want (%q, true, nil)", outcome, ok, err, OutcomeFailedTerminal)
			}

			// 同じ文字列が quality verdict として通ってはならない
			result := sampleOperationResult(t, sampleWorkerOperation(t))
			result.Outcome = OperationOutcome(class)
			if err := ValidateOperationResult(result); err == nil {
				t.Fatalf("failure class %q を Operation outcome として受理した", class)
			}
		})
	}
}

// Operation Result は execution outcome だけを表す。approve / request_changes は
// Review Worker だけが返す品質 verdict であり、Operation outcome に混ぜない。
func TestOperationOutcomeRejectsReviewVerdicts(t *testing.T) {
	verdicts := []ReviewVerdict{VerdictApprove, VerdictRequestChanges}
	for _, verdict := range verdicts {
		t.Run(string(verdict), func(t *testing.T) {
			result := sampleOperationResult(t, sampleWorkerOperation(t))
			result.Outcome = OperationOutcome(verdict)
			if err := ValidateOperationResult(result); err == nil {
				t.Fatalf("quality verdict %q を Operation outcome として受理した", verdict)
			}
		})
	}
}

// 品質 verdict と attempt failure を同じ field で表現しない、を型で固定する。
// AttemptFailure へ verdict 型の field を足すと本 test が落ちる。
func TestAttemptFailureHasNoVerdictField(t *testing.T) {
	forbidden := map[reflect.Type]bool{
		reflect.TypeOf(VerdictApprove):    true,
		reflect.TypeOf(OutcomeSucceeded):  true,
		reflect.TypeOf(ReviewFinding{}):   true,
		reflect.TypeOf(OperationResult{}): true,
		reflect.TypeOf(ReviewResult{}):    true,
	}
	failureType := reflect.TypeOf(AttemptFailure{})
	for i := range failureType.NumField() {
		field := failureType.Field(i)
		if forbidden[field.Type] {
			t.Fatalf("AttemptFailure.%s が quality verdict 型 %s を持つ", field.Name, field.Type)
		}
	}
}

func TestAttemptFailureValidation(t *testing.T) {
	tests := map[string]func(*AttemptFailure){
		"unknown class": func(f *AttemptFailure) { f.Class = "flaky" },
		"empty class":   func(f *AttemptFailure) { f.Class = "" },
		"attempt id":    func(f *AttemptFailure) { f.AttemptID = "" },
		"evidence":      func(f *AttemptFailure) { f.Evidence = "  " },
		"control char":  func(f *AttemptFailure) { f.Evidence = "timeout\x00" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := sampleAttemptFailure()
			mutate(&got)
			if err := got.Validate(); err == nil {
				t.Fatal("invalid attempt failure を受理した")
			}
			// 不正な record を「retry 余地あり」と同じ戻り値へ潰すと、bounded retry が
			// 無効化され、escalation も failed_terminal も起きないまま attempt が積まれる。
			for _, exhausted := range []bool{true, false} {
				outcome, ok, err := got.TerminalOutcome(exhausted)
				if err == nil {
					t.Fatalf("retryBudgetExhausted=%v で invalid な failure record を受理した", exhausted)
				}
				if ok || outcome != "" {
					t.Fatalf("invalid attempt failure から outcome を返した: (%q, %v)", outcome, ok)
				}
			}
		})
	}
}
