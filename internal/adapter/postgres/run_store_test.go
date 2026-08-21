package postgres

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

func TestValidateTransitionRejectsInitialClaimEvent(t *testing.T) {
	run := validStoredRun()
	err := validateTransition(Transition{
		ExpectedVersion: run.Version,
		Event:           workflow.KindClaimSucceeded,
		Run:             run,
	})
	if !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("claim_succeeded transition error = %v, want ErrInvalidRun", err)
	}
}

func TestValidateTransitionRejectsInvalidEnvelope(t *testing.T) {
	base := validStoredRun()
	tests := map[string]func(*Transition){
		"expected version": func(transition *Transition) { transition.ExpectedVersion = 0 },
		"run version":      func(transition *Transition) { transition.Run.Version++ },
		"event kind":       func(transition *Transition) { transition.Event = "unknown" },
		"run":              func(transition *Transition) { transition.Run.ID = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			transition := Transition{
				ExpectedVersion: base.Version,
				Event:           workflow.KindOperationStarted,
				Run:             base,
			}
			mutate(&transition)
			if err := validateTransition(transition); !errors.Is(err, ErrInvalidRun) {
				t.Fatalf("error = %v, want ErrInvalidRun", err)
			}
		})
	}
}

func TestValidateTransitionRequiresReviewBindingOnlyForReviewEvent(t *testing.T) {
	base := validStoredRun()
	review := workflow.ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictRequestChanges,
		Head:          "1111111111111111111111111111111111111111",
		RequestDigest: contract.SHA256([]byte("request")),
		ResultDigest:  contract.SHA256([]byte("result")),
	}
	for name, transition := range map[string]Transition{
		"missing binding": {
			ExpectedVersion: base.Version,
			Event:           workflow.KindReviewCompleted,
			Run:             base,
		},
		"unexpected binding": {
			ExpectedVersion: base.Version,
			Event:           workflow.KindOperationStarted,
			Run:             base,
			Review:          &review,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateTransition(transition); !errors.Is(err, ErrInvalidRun) {
				t.Fatalf("error = %v, want ErrInvalidRun", err)
			}
		})
	}

	valid := Transition{
		ExpectedVersion: base.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             base,
		Review:          &review,
	}
	if err := validateTransition(valid); err != nil {
		t.Fatalf("valid review transition error = %v", err)
	}
}

func TestValidateReviewProgress(t *testing.T) {
	head := "1111111111111111111111111111111111111111"
	request := contract.SHA256([]byte("request"))
	result := contract.SHA256([]byte("result"))
	current := validStoredRun()
	current.PublishedHead = head
	review := workflow.ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictApprove,
		Head:          head,
		RequestDigest: request,
		ResultDigest:  result,
	}
	next := current
	next.Rounds.TestValidity = 1
	next.TotalRounds.TestValidity = 1
	next.TestApproval = &workflow.Approval{
		Head:          head,
		RequestDigest: request,
		ResultDigest:  result,
	}

	if round, err := validateReviewProgress(current, next, review); err != nil || round != 1 {
		t.Fatalf("valid review progress = round %d error %v, want round 1", round, err)
	}

	for name, mutate := range map[string]func(*workflow.Run, *workflow.ReviewCompleted){
		"head mismatch": func(next *workflow.Run, _ *workflow.ReviewCompleted) {
			next.PublishedHead = "2222222222222222222222222222222222222222"
		},
		"counter jump": func(next *workflow.Run, _ *workflow.ReviewCompleted) {
			next.TotalRounds.TestValidity = 2
		},
		"approval mismatch": func(_ *workflow.Run, review *workflow.ReviewCompleted) {
			review.ResultDigest = contract.SHA256([]byte("other-result"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			gotNext, gotReview := next, review
			mutate(&gotNext, &gotReview)
			if _, err := validateReviewProgress(current, gotNext, gotReview); !errors.Is(err, ErrInvalidRun) {
				t.Fatalf("error = %v, want ErrInvalidRun", err)
			}
		})
	}

	escalated := current
	escalated.Phase = workflow.PhaseNeedsHuman
	escalated.TotalRounds.TestValidity = 1
	escalated.Rounds = workflow.ReviewRounds{}
	review.Verdict = contract.VerdictNeedsHuman
	if round, err := validateReviewProgress(current, escalated, review); err != nil || round != 1 {
		t.Fatalf("escalated review progress = round %d error %v, want round 1", round, err)
	}
}

func TestValidateApprovalRequiresCompleteReviewBinding(t *testing.T) {
	requestDigest := contract.SHA256([]byte("request"))
	resultDigest := contract.SHA256([]byte("result"))
	tests := map[string]*workflow.Approval{
		"valid": {
			Head:          "1111111111111111111111111111111111111111",
			RequestDigest: requestDigest,
			ResultDigest:  resultDigest,
		},
		"missing head": {
			RequestDigest: requestDigest,
			ResultDigest:  resultDigest,
		},
		"missing request": {
			Head:         "1111111111111111111111111111111111111111",
			ResultDigest: resultDigest,
		},
		"missing result": {
			Head:          "1111111111111111111111111111111111111111",
			RequestDigest: requestDigest,
		},
	}
	for name, approval := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateApproval("test", approval)
			if name == "valid" {
				if err != nil {
					t.Fatalf("valid approval error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRun) {
				t.Fatalf("error = %v, want ErrInvalidRun", err)
			}
		})
	}
}

func TestScanApprovalRejectsPartialOrInvalidStoredBinding(t *testing.T) {
	validHead := sql.NullString{String: "1111111111111111111111111111111111111111", Valid: true}
	validRequest := sql.NullString{String: string(contract.SHA256([]byte("request"))), Valid: true}
	validResult := sql.NullString{String: string(contract.SHA256([]byte("result"))), Valid: true}

	approval, err := scanApproval("test", validHead, validRequest, validResult)
	if err != nil {
		t.Fatalf("valid stored approval error = %v", err)
	}
	if approval == nil || approval.ResultDigest != contract.Digest(validResult.String) {
		t.Fatalf("restored approval = %#v", approval)
	}

	for name, values := range map[string][3]sql.NullString{
		"missing result": {validHead, validRequest, {}},
		"invalid result": {validHead, validRequest, {String: "sha256:nope", Valid: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scanApproval("test", values[0], values[1], values[2]); !errors.Is(err, ErrCorruptRun) {
				t.Fatalf("error = %v, want ErrCorruptRun", err)
			}
		})
	}
}

func TestValidateRoundCounters(t *testing.T) {
	for name, test := range map[string]struct {
		rounds workflow.ReviewRounds
		total  workflow.ReviewRounds
		valid  bool
	}{
		"zero":             {valid: true},
		"within total":     {rounds: workflow.ReviewRounds{TestValidity: 2}, total: workflow.ReviewRounds{TestValidity: 3}, valid: true},
		"negative round":   {rounds: workflow.ReviewRounds{TestValidity: -1}},
		"negative total":   {total: workflow.ReviewRounds{FinalImplementation: -1}},
		"round over total": {rounds: workflow.ReviewRounds{FinalImplementation: 2}, total: workflow.ReviewRounds{FinalImplementation: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateRoundCounters(test.rounds, test.total)
			if test.valid && err != nil {
				t.Fatalf("valid counters error = %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidRun) {
				t.Fatalf("error = %v, want ErrInvalidRun", err)
			}
		})
	}
}

func validStoredRun() workflow.Run {
	return workflow.Run{
		ID:      "run-unit",
		Issue:   contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: 13},
		Version: 1,
		Phase:   workflow.PhaseClaimed,
		Input: workflow.InputIdentity{
			ContextManifest: contract.ContextManifestRef{
				Schema: contract.ContextManifestSchemaV1Alpha1,
				Digest: contract.SHA256([]byte("context")),
			},
			ExecutionPolicy: contract.ExecutionPolicyRef{
				Schema: contract.ExecutionPolicySchemaV1Alpha1,
				Digest: contract.SHA256([]byte("execution")),
			},
		},
		EscalationPolicy: contract.EscalationPolicyRef{
			Schema: contract.EscalationPolicySchemaV1Alpha1,
			Digest: contract.SHA256([]byte("escalation")),
		},
		RoundLimits: contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3},
		Observation: contract.IssueObservationRef{
			Schema: contract.IssueObservationSchemaV1Alpha1,
			Digest: contract.SHA256([]byte("observation")),
		},
		ObservationBodyDigest: contract.SHA256([]byte("issue-body")),
	}
}
