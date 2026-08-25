// Package controller は deployment policy の解決と workflow の調停を担う。
package controller

import (
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

type WorkerPolicyConfig struct {
	Provider        string
	Model           string
	Adapter         string
	AdapterVersion  string
	ToolPermissions []string
	Timeout         time.Duration
}

// PolicyConfig は deployment configuration だけを表す。gate される workload が自分の
// 実行境界や予算を選べないよう、Task Issue と Worker Result は入力に持たない。
type PolicyConfig struct {
	IssueWorker               WorkerPolicyConfig
	ReviewWorker              WorkerPolicyConfig
	AttemptRetries            int
	TestValidityRounds        int
	FinalImplementationRounds int
}

type ResolvedExecutionPolicy struct {
	Policy  contract.ExecutionPolicy
	Ref     contract.ExecutionPolicyRef
	Payload contract.ArtifactPayload
}

type ResolvedEscalationPolicy struct {
	Policy  contract.EscalationPolicy
	Ref     contract.EscalationPolicyRef
	Payload contract.ArtifactPayload
}

type ResolvedPolicies struct {
	Execution  ResolvedExecutionPolicy
	Escalation ResolvedEscalationPolicy
}

// ResolvePolicies は一つの deployment snapshot を決定論的な versioned policy ref へ変換する。
func ResolvePolicies(config PolicyConfig) (ResolvedPolicies, error) {
	execution := contract.ExecutionPolicy{
		Schema:       contract.ExecutionPolicySchemaV1Alpha1,
		IssueWorker:  workerPolicy(config.IssueWorker),
		ReviewWorker: workerPolicy(config.ReviewWorker),
	}
	executionRef, executionPayload, err := contract.EncodeExecutionPolicy(execution)
	if err != nil {
		return ResolvedPolicies{}, err
	}
	escalation := contract.EscalationPolicy{
		Schema:         contract.EscalationPolicySchemaV1Alpha1,
		AttemptRetries: config.AttemptRetries,
		ReviewRounds: contract.ReviewRoundLimits{
			TestValidity:        config.TestValidityRounds,
			FinalImplementation: config.FinalImplementationRounds,
		},
	}
	escalationRef, escalationPayload, err := contract.EncodeEscalationPolicy(escalation)
	if err != nil {
		return ResolvedPolicies{}, err
	}
	return ResolvedPolicies{
		Execution:  ResolvedExecutionPolicy{Policy: execution, Ref: executionRef, Payload: executionPayload},
		Escalation: ResolvedEscalationPolicy{Policy: escalation, Ref: escalationRef, Payload: escalationPayload},
	}, nil
}

func workerPolicy(config WorkerPolicyConfig) contract.WorkerExecutionPolicy {
	return contract.WorkerExecutionPolicy{
		Provider:        config.Provider,
		Model:           config.Model,
		Adapter:         config.Adapter,
		AdapterVersion:  config.AdapterVersion,
		ToolPermissions: append([]string(nil), config.ToolPermissions...),
		Timeout:         config.Timeout,
	}
}
