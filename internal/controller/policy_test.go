package controller

import (
	"testing"
	"time"

	"github.com/mrbaron3/kudo/internal/contract"
)

func TestResolvePoliciesBuildsImmutableRefsFromDeploymentConfig(t *testing.T) {
	t.Parallel()

	config := PolicyConfig{
		IssueWorker: WorkerPolicyConfig{
			Provider: "codex", Model: "gpt-5.6", Adapter: "codex-cli", AdapterVersion: "1.4.0",
			ToolPermissions: []string{"repository:write", "github:write"}, Timeout: 45 * time.Minute,
		},
		ReviewWorker: WorkerPolicyConfig{
			Provider: "codex", Model: "gpt-5.6", Adapter: "codex-cli", AdapterVersion: "1.4.0",
			ToolPermissions: []string{"repository:read"}, Timeout: 30 * time.Minute,
		},
		AttemptRetries:            4,
		TestValidityRounds:        2,
		FinalImplementationRounds: 3,
	}

	first, err := ResolvePolicies(config)
	if err != nil {
		t.Fatalf("ResolvePolicies() error = %v", err)
	}
	second, err := ResolvePolicies(config)
	if err != nil {
		t.Fatal(err)
	}
	if first.Execution.Ref != second.Execution.Ref || first.Escalation.Ref != second.Escalation.Ref {
		t.Fatalf("same config produced different refs: %#v %#v", first, second)
	}
	if first.Execution.Policy.Schema != contract.ExecutionPolicySchemaV1Alpha1 ||
		first.Escalation.Policy.Schema != contract.EscalationPolicySchemaV1Alpha1 {
		t.Fatalf("resolved schemas = %#v", first)
	}
	if first.Escalation.Policy.AttemptRetries != 4 || first.Escalation.Policy.ReviewRounds.TestValidity != 2 {
		t.Fatalf("escalation = %#v", first.Escalation.Policy)
	}

	changed := config
	changed.IssueWorker.Model = "gpt-5.6-mini"
	changedPolicies, err := ResolvePolicies(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedPolicies.Execution.Ref == first.Execution.Ref || changedPolicies.Escalation.Ref != first.Escalation.Ref {
		t.Fatalf("policy dimensions were not isolated")
	}
}

func TestResolvePoliciesRejectsInvalidDeploymentConfig(t *testing.T) {
	t.Parallel()

	_, err := ResolvePolicies(PolicyConfig{})
	if err == nil {
		t.Fatal("ResolvePolicies() error = nil")
	}
}
