package reviewerfixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const fixtureBaseSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var fixtureRepository = githubadapter.Repository{Owner: "acme", Name: "reviewer-fixtures"}

var fixtureIdentity = githubadapter.RecorderIdentity{
	CommentAuthor: githubadapter.Actor{ID: 101, Login: "kudo-implementer[bot]"},
	CheckRunApp:   githubadapter.AppIdentity{ID: 202, Slug: "kudo-implementer", Name: "Kudo Implementer"},
}

func TestSeederCreatesGatewayReadableClaimFixture(t *testing.T) {
	t.Parallel()

	target, err := NewMemoryTarget(fixtureRepository, fixtureIdentity, fixtureBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadCase("valid")
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := NewSeeder(target)
	if err != nil {
		t.Fatal(err)
	}

	result, err := seeder.Seed(t.Context(), SeedRequest{
		Repository: fixtureRepository,
		Issue:      71,
		Fixture:    fixture,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.PullRequest.Draft || result.PullRequest.Number <= 0 {
		t.Fatalf("Pull Request = %#v", result.PullRequest)
	}
	if result.Head.Base.SHA != fixtureBaseSHA || result.Head.HeadSHA == fixtureBaseSHA ||
		result.PullRequest.HeadSHA != result.Head.HeadSHA {
		t.Fatalf("head = %#v, Pull Request = %#v", result.Head, result.PullRequest)
	}
	if !strings.HasSuffix(result.Head.TestFile.Path, "_test.go") || len(result.Head.TestFile.Data) == 0 {
		t.Fatalf("test-only file = %#v", result.Head.TestFile)
	}

	claimSurface, err := githubadapter.ParseRecordSurface(result.PullRequestBody)
	if err != nil {
		t.Fatalf("claim surface を gateway parser で読めない: %v", err)
	}
	checkpoint := readCheckpoint(t, claimSurface)
	if checkpoint.Context.BaseSHA != fixtureBaseSHA ||
		claimSurface.Marker.Kind != string(contract.ArtifactKindClaimCheckpoint) {
		t.Fatalf("checkpoint = %#v, marker = %#v", checkpoint, claimSurface.Marker)
	}

	testPlanSurface, err := githubadapter.ParseRecordSurface(string(result.TestPlan.Body))
	if err != nil {
		t.Fatalf("test plan surface を gateway parser で読めない: %v", err)
	}
	assertBoundPayload(t, testPlanSurface, fixture.TestPlan)
	if testPlanSurface.Marker.Kind != string(contract.ArtifactNameTestPlan) ||
		testPlanSurface.Marker.Head != result.Head.HeadSHA {
		t.Fatalf("test plan marker = %#v", testPlanSurface.Marker)
	}

	if result.RedEvidence.Name != workflow.CheckRunEvidenceRed || result.RedEvidence.App.ID != fixtureIdentity.CheckRunApp.ID {
		t.Fatalf("RED evidence = %#v", result.RedEvidence)
	}
	redSurface, err := githubadapter.ParseRecordSurface(result.RedEvidence.Output.Text)
	if err != nil {
		t.Fatalf("RED evidence surface を gateway parser で読めない: %v", err)
	}
	assertBoundPayload(t, redSurface, fixture.RedEvidence)
}

func TestSeederConvergesWithoutDuplicateRecords(t *testing.T) {
	t.Parallel()

	target, err := NewMemoryTarget(fixtureRepository, fixtureIdentity, fixtureBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadCase("valid")
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := NewSeeder(target)
	if err != nil {
		t.Fatal(err)
	}
	request := SeedRequest{Repository: fixtureRepository, Issue: 71, Fixture: fixture}

	first, err := seeder.Seed(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := seeder.Seed(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}

	if first.PullRequest.Number != second.PullRequest.Number || first.Head.HeadSHA != second.Head.HeadSHA ||
		first.TestPlan.ID != second.TestPlan.ID || first.RedEvidence.ID != second.RedEvidence.ID {
		t.Fatalf("再実行で identity が変わった: first=%#v second=%#v", first, second)
	}
	if got, want := target.Counts(), (MemoryCounts{Branches: 1, PullRequests: 1, Comments: 1, CheckRuns: 1}); got != want {
		t.Fatalf("counts = %#v, want %#v", got, want)
	}
}

func TestNegativeFixtureCorpusInjectsOnlySelectedSurfaceDefect(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"digest-mismatch", "missing-required-input", "missing-marker"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			target, err := NewMemoryTarget(fixtureRepository, fixtureIdentity, fixtureBaseSHA)
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := LoadCase(name)
			if err != nil {
				t.Fatal(err)
			}
			seeder, err := NewSeeder(target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := seeder.Seed(t.Context(), SeedRequest{
				Repository: fixtureRepository,
				Issue:      71,
				Fixture:    fixture,
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := githubadapter.ParseRecordSurface(result.PullRequestBody); err != nil {
				t.Fatalf("%s が正常な claim checkpoint まで壊した: %v", name, err)
			}
			if result.Head.HeadSHA == fixtureBaseSHA || !strings.HasSuffix(result.Head.TestFile.Path, "_test.go") {
				t.Fatalf("%s が test-only head まで壊した: %#v", name, result.Head)
			}

			switch name {
			case "digest-mismatch":
				if result.TestPlan.ID == 0 || result.RedEvidence.ID == 0 {
					t.Fatal("digest 不一致以外の required input が欠落した")
				}
				plan, err := githubadapter.ParseRecordSurface(string(result.TestPlan.Body))
				if err != nil {
					t.Fatal(err)
				}
				assertBoundPayload(t, plan, fixture.TestPlan)
				red, err := githubadapter.ParseRecordSurface(result.RedEvidence.Output.Text)
				if err != nil {
					t.Fatal(err)
				}
				if red.MachineBlock == nil || red.MachineBlock.Digest == string(contract.SHA256(red.MachineBlock.Payload)) {
					t.Fatalf("RED evidence に digest 不一致が無い: %#v", red)
				}
			case "missing-required-input":
				if result.TestPlan.ID == 0 || result.RedEvidence.ID != 0 {
					t.Fatalf("required input の欠陥範囲が不正: %#v", result)
				}
				if _, err := githubadapter.ParseRecordSurface(string(result.TestPlan.Body)); err != nil {
					t.Fatalf("残した test plan が壊れている: %v", err)
				}
			case "missing-marker":
				if result.TestPlan.ID == 0 || result.RedEvidence.ID == 0 {
					t.Fatal("marker 以外の required input が欠落した")
				}
				if _, err := githubadapter.ParseRecordSurface(string(result.TestPlan.Body)); !errors.Is(err, githubadapter.ErrInvalidRecordSurface) {
					t.Fatalf("marker 欠落 surface の parse error = %v", err)
				}
				if _, err := githubadapter.ParseMachineBlock(string(result.TestPlan.Body)); err != nil {
					t.Fatalf("marker 以外の machine block まで壊した: %v", err)
				}
				if _, err := githubadapter.ParseRecordSurface(result.RedEvidence.Output.Text); err != nil {
					t.Fatalf("RED evidence まで壊した: %v", err)
				}
			}
		})
	}
}

func TestCorpusRejectsUnknownCase(t *testing.T) {
	t.Parallel()

	if _, err := LoadCase("unknown"); err == nil {
		t.Fatal("unknown fixture case was accepted")
	}
}

func TestValidFixtureRecordSurfacesGolden(t *testing.T) {
	t.Parallel()

	target, err := NewMemoryTarget(fixtureRepository, fixtureIdentity, fixtureBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadCase("valid")
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := NewSeeder(target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := seeder.Seed(t.Context(), SeedRequest{
		Repository: fixtureRepository, Issue: 71, Fixture: fixture,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := struct {
		PullRequestBody string `json:"pullRequestBody"`
		TestPlanBody    string `json:"testPlanBody"`
		RedEvidenceText string `json:"redEvidenceText"`
	}{
		PullRequestBody: result.PullRequestBody,
		TestPlanBody:    string(result.TestPlan.Body),
		RedEvidenceText: result.RedEvidence.Output.Text,
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "golden", "valid-record-surfaces.json"))
	if err != nil {
		t.Fatalf("golden を読めない: %v\n--- generated ---\n%s", err, encoded.String())
	}
	if !bytes.Equal(bytes.TrimSpace(encoded.Bytes()), bytes.TrimSpace(want)) {
		t.Fatalf("record surface golden mismatch\n--- got ---\n%s\n--- want ---\n%s", encoded.String(), want)
	}

	for name, surface := range map[string]string{
		"claim": result.PullRequestBody, "test-plan": string(result.TestPlan.Body),
		"red-evidence": result.RedEvidence.Output.Text,
	} {
		if _, err := githubadapter.ParseRecordSurface(surface); err != nil {
			t.Fatalf("golden %s を gateway parser で読めない: %v", name, err)
		}
	}
}

func readCheckpoint(t *testing.T, surface githubadapter.RecordSurface) contract.ClaimCheckpoint {
	t.Helper()
	if surface.MachineBlock == nil {
		t.Fatal("claim machine block = nil")
	}
	payload := contract.ArtifactPayload{
		Kind:      contract.ArtifactKindClaimCheckpoint,
		Schema:    contract.ClaimCheckpointSchemaV1Alpha1,
		MediaType: surface.MachineBlock.MediaType,
		Digest:    contract.Digest(surface.MachineBlock.Digest),
		Data:      surface.MachineBlock.Payload,
	}
	checkpoint, err := contract.ReadClaimCheckpointArtifact(contract.ClaimCheckpointRef{
		Schema: contract.ClaimCheckpointSchemaV1Alpha1,
		Digest: payload.Digest,
	}, payload)
	if err != nil {
		t.Fatalf("claim checkpoint payload を contract parser で読めない: %v", err)
	}
	return checkpoint
}

func assertBoundPayload(t *testing.T, surface githubadapter.RecordSurface, want []byte) {
	t.Helper()
	if surface.MachineBlock == nil {
		t.Fatal("machine block = nil")
	}
	if got := contract.SHA256(surface.MachineBlock.Payload); surface.MachineBlock.Digest != string(got) {
		t.Fatalf("payload digest = %s, want %s", surface.MachineBlock.Digest, got)
	}
	if string(surface.MachineBlock.Payload) != string(want) {
		t.Fatalf("payload = %q, want %q", surface.MachineBlock.Payload, want)
	}
}

// test plan は人間が Pull Request 上で読む artifact なので、machine block の base64 だけでなく
// 可読な本文としても載っていなければならない。GitHub を single source of truth にする以上、
// 記録面を開いた人間が中身を読めない状態は許容しない。
func TestSeededTestPlanStaysHumanReadableInCommentBody(t *testing.T) {
	t.Parallel()

	target, err := NewMemoryTarget(fixtureRepository, fixtureIdentity, fixtureBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadCase("valid")
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := NewSeeder(target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := seeder.Seed(t.Context(), SeedRequest{
		Repository: fixtureRepository, Issue: 71, Fixture: fixture,
	})
	if err != nil {
		t.Fatal(err)
	}

	human, _, found := strings.Cut(string(result.TestPlan.Body), "<!-- kudo-marker ")
	if !found {
		t.Fatalf("test plan comment に marker が無い: %s", result.TestPlan.Body)
	}
	if !strings.Contains(human, string(fixture.TestPlan)) {
		t.Fatalf("人間向け本文に test plan 本文が含まれていない:\n--- human ---\n%s", human)
	}
}
