package contract_test

import (
	"os"
	"testing"

	"github.com/mrbaron3/kudo/internal/contract"
)

func TestCompilerIsApplicationFacingBoundary(t *testing.T) {
	body, err := os.ReadFile("testdata/valid/minimal.md")
	if err != nil {
		t.Fatal(err)
	}
	compiled, validationErrors := contract.Compile(string(body), contract.IssueRef{
		Owner:      "mrbaron3",
		Repository: "kudo",
		Number:     10,
	})
	if len(validationErrors) > 0 {
		t.Fatalf("compile error: %v", validationErrors)
	}
	if compiled.TaskContextRef.Schema != contract.TaskContextSchemaV1Alpha1 {
		t.Fatalf("TaskContextRef schema = %q", compiled.TaskContextRef.Schema)
	}
	if compiled.ObservationRef.Schema != contract.IssueObservationSchemaV1Alpha1 {
		t.Fatalf("IssueObservationRef schema = %q", compiled.ObservationRef.Schema)
	}
	if compiled.ClaimRequirements.Readiness != contract.ReadinessReady {
		t.Fatalf("readiness = %q", compiled.ClaimRequirements.Readiness)
	}
}
