// Package reviewerfixture は Review Worker 開発用の claim 形 Pull Request を合成する。
// production runtime からは呼ばず、fake と opt-in live GitHub の同じ Target port に対して使う。
package reviewerfixture

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
	"github.com/mrbaron3/kudo/internal/workflow"
)

type Fault string

const (
	FaultNone                 Fault = "none"
	FaultDigestMismatch       Fault = "digest-mismatch"
	FaultMissingRequiredInput Fault = "missing-required-input"
	FaultMissingMarker        Fault = "missing-marker"
)

type TestFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type Case struct {
	Name        string   `json:"name"`
	Fault       Fault    `json:"fault"`
	TestFile    TestFile `json:"testFile"`
	TestPlan    []byte   `json:"testPlan"`
	RedEvidence []byte   `json:"redEvidence"`
}

//go:embed testdata/corpus/*.json
var corpus embed.FS

func LoadCase(name string) (Case, error) {
	if name == "" || path.Base(name) != name {
		return Case{}, fmt.Errorf("fixture case 名が不正: %q", name)
	}
	data, err := corpus.ReadFile("testdata/corpus/" + name + ".json")
	if err != nil {
		return Case{}, fmt.Errorf("fixture case %q が存在しない", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var encoded struct {
		Name     string `json:"name"`
		Fault    Fault  `json:"fault"`
		TestFile struct {
			Path string `json:"path"`
			Data string `json:"data"`
		} `json:"testFile"`
		TestPlan    string `json:"testPlan"`
		RedEvidence string `json:"redEvidence"`
	}
	if err := decoder.Decode(&encoded); err != nil {
		return Case{}, fmt.Errorf("fixture case %q を読めない: %w", name, err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return Case{}, fmt.Errorf("fixture case %q の後に余分な JSON 値がある", name)
	}
	if encoded.Name != name {
		return Case{}, fmt.Errorf("fixture case identity が file 名と一致しない: %q", encoded.Name)
	}
	fixture := Case{
		Name: encoded.Name, Fault: encoded.Fault,
		TestFile:    TestFile{Path: encoded.TestFile.Path, Data: []byte(encoded.TestFile.Data)},
		TestPlan:    []byte(encoded.TestPlan),
		RedEvidence: []byte(encoded.RedEvidence),
	}
	if err := fixture.validate(); err != nil {
		return Case{}, err
	}
	return fixture.clone(), nil
}

func (c Case) validate() error {
	switch c.Fault {
	case FaultNone, FaultDigestMismatch, FaultMissingRequiredInput, FaultMissingMarker:
	default:
		return fmt.Errorf("fixture fault が不正: %q", c.Fault)
	}
	if c.Name == "" || c.TestFile.Path == "" || path.Clean(c.TestFile.Path) != c.TestFile.Path ||
		path.IsAbs(c.TestFile.Path) || strings.HasPrefix(c.TestFile.Path, "../") ||
		!strings.HasSuffix(c.TestFile.Path, "_test.go") || len(c.TestFile.Data) == 0 ||
		len(c.TestPlan) == 0 || len(c.RedEvidence) == 0 {
		return fmt.Errorf("fixture case %q の必須 payload が不正", c.Name)
	}
	return nil
}

func (c Case) clone() Case {
	c.TestFile.Data = bytes.Clone(c.TestFile.Data)
	c.TestPlan = bytes.Clone(c.TestPlan)
	c.RedEvidence = bytes.Clone(c.RedEvidence)
	return c
}

type FixtureHead struct {
	Base         issueworker.ClaimBase
	BranchName   string
	BootstrapSHA string
	HeadSHA      string
	TestFile     TestFile
}

type Target interface {
	EnsureFixtureHead(context.Context, contract.IssueRef, TestFile) (FixtureHead, error)
	EnsureDraftPullRequest(context.Context, issueworker.DraftPullRequestInput) (issueworker.ClaimPullRequest, error)
	EnsureComment(context.Context, int64, githubadapter.CommentRecord) (githubadapter.Comment, bool, error)
	EnsureUnmarkedComment(context.Context, int64, string) (githubadapter.Comment, bool, error)
	EnsureCheckRun(context.Context, githubadapter.CheckRunRecord) (githubadapter.CheckRun, bool, error)
}

type Seeder struct {
	target Target
}

func NewSeeder(target Target) (*Seeder, error) {
	if target == nil {
		return nil, errors.New("fixture Target は必須")
	}
	return &Seeder{target: target}, nil
}

type SeedRequest struct {
	Repository githubadapter.Repository
	Issue      int64
	Fixture    Case
}

type Result struct {
	Head            FixtureHead
	PullRequest     issueworker.ClaimPullRequest
	PullRequestBody string
	TestPlan        githubadapter.Comment
	RedEvidence     githubadapter.CheckRun
}

func (s *Seeder) Seed(ctx context.Context, request SeedRequest) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context は必須")
	}
	if request.Repository.Owner == "" || request.Repository.Name == "" || request.Issue <= 0 {
		return Result{}, errors.New("repository と Issue number は必須")
	}
	if int64(int(request.Issue)) != request.Issue {
		return Result{}, errors.New("Issue number がこの platform の範囲を超える")
	}
	if err := request.Fixture.validate(); err != nil {
		return Result{}, err
	}
	issue := contract.IssueRef{
		Owner: request.Repository.Owner, Repository: request.Repository.Name, Number: int(request.Issue),
	}
	head, err := s.target.EnsureFixtureHead(ctx, issue, request.Fixture.TestFile)
	if err != nil {
		return Result{}, fmt.Errorf("test-only head を ensure する: %w", err)
	}
	checkpoint := fixtureCheckpoint(head.Base.SHA)
	pull, err := s.target.EnsureDraftPullRequest(ctx, issueworker.DraftPullRequestInput{
		Issue:      issue,
		Title:      fmt.Sprintf("[fixture] Reviewer input for #%d", request.Issue),
		BranchName: head.BranchName,
		HeadSHA:    head.HeadSHA,
		Base:       head.Base,
		Checkpoint: checkpoint,
	})
	if err != nil {
		return Result{}, fmt.Errorf("draft Pull Request を ensure する: %w", err)
	}
	pullBody, err := githubadapter.RenderClaimPullRequestBody(request.Repository, issue, int64(pull.Number), checkpoint)
	if err != nil {
		return Result{}, fmt.Errorf("claim Pull Request body を render する: %w", err)
	}
	result := Result{Head: head, PullRequest: pull, PullRequestBody: pullBody}

	testPlanRecord := artifactRecord(
		request.Repository, request.Issue, pull.Number, head.HeadSHA,
		contract.ArtifactNameTestPlan, contract.MediaTypeMarkdown, request.Fixture.TestPlan,
	)
	if request.Fixture.Fault == FaultMissingMarker {
		body, renderErr := renderWithoutMarker("Reviewer fixture の test plan です。", testPlanRecord)
		if renderErr != nil {
			return Result{}, renderErr
		}
		result.TestPlan, _, err = s.target.EnsureUnmarkedComment(ctx, int64(pull.Number), body)
	} else {
		result.TestPlan, _, err = s.target.EnsureComment(ctx, int64(pull.Number), githubadapter.CommentRecord{
			Marker:       testPlanRecord.Marker,
			Body:         "Reviewer fixture の test plan です。",
			MachineBlock: testPlanRecord.MachineBlock,
		})
	}
	if err != nil {
		return Result{}, fmt.Errorf("test plan comment を ensure する: %w", err)
	}

	if request.Fixture.Fault == FaultMissingRequiredInput {
		return result, nil
	}
	redRecord := artifactRecord(
		request.Repository, request.Issue, pull.Number, head.HeadSHA,
		contract.ArtifactNameRedEvidence, contract.MediaTypeYAML, request.Fixture.RedEvidence,
	)
	if request.Fixture.Fault == FaultDigestMismatch {
		wrong := contract.SHA256(append(bytes.Clone(request.Fixture.RedEvidence), "-different"...))
		redRecord.Marker.Digest = string(wrong)
		redRecord.MachineBlock.Digest = string(wrong)
	}
	result.RedEvidence, _, err = s.target.EnsureCheckRun(ctx, githubadapter.CheckRunRecord{
		Marker:       redRecord.Marker,
		Name:         workflow.CheckRunEvidenceRed,
		HeadSHA:      head.HeadSHA,
		Conclusion:   "success",
		Title:        "RED evidence recorded",
		Summary:      "Reviewer fixture が test-only head の RED evidence を固定しました。",
		MachineBlock: redRecord.MachineBlock,
	})
	if err != nil {
		return Result{}, fmt.Errorf("RED evidence check run を ensure する: %w", err)
	}
	return result, nil
}

type artifactSurface struct {
	Marker       githubadapter.Marker
	MachineBlock *githubadapter.MachineBlock
}

func artifactRecord(repository githubadapter.Repository, issue int64, pullNumber int, head string,
	name contract.ArtifactName, mediaType string, payload []byte) artifactSurface {
	digest := contract.SHA256(payload)
	return artifactSurface{
		Marker: githubadapter.Marker{
			Repository: repository, Issue: issue, Run: strconv.Itoa(pullNumber), Kind: string(name),
			Round: 1, Head: head, Digest: string(digest),
		},
		MachineBlock: &githubadapter.MachineBlock{
			Kind: string(name), MediaType: mediaType, Digest: string(digest), Payload: bytes.Clone(payload),
		},
	}
}

func renderWithoutMarker(body string, record artifactSurface) (string, error) {
	rendered, err := githubadapter.RenderComment(body, record.Marker, record.MachineBlock)
	if err != nil {
		return "", err
	}
	marker, err := githubadapter.EncodeMarker(record.Marker)
	if err != nil {
		return "", err
	}
	withoutMarker := strings.Replace(rendered, marker+"\n", "", 1)
	if withoutMarker == rendered {
		return "", errors.New("gateway が render した marker を fault injection できない")
	}
	return withoutMarker, nil
}

func fixtureCheckpoint(baseSHA string) contract.ClaimCheckpoint {
	identity := func(name string) contract.Digest {
		return contract.SHA256([]byte("reviewer-fixture/valid/" + name))
	}
	return contract.ClaimCheckpoint{
		Schema: contract.ClaimCheckpointSchemaV1Alpha1,
		Context: contract.ClaimContext{
			Compiler:        contract.IssueCompilerVersionV1Alpha1,
			Observation:     contract.IssueObservationRef{Schema: contract.IssueObservationSchemaV1Alpha1, Digest: identity("issue-observation")},
			BodyDigest:      identity("body"),
			TaskContext:     contract.TaskContextRef{Schema: contract.TaskContextSchemaV1Alpha1, Digest: identity("task-context")},
			ContextManifest: contract.ContextManifestRef{Schema: contract.ContextManifestSchemaV1Alpha1, Digest: identity("context-manifest")},
			BaseSHA:         baseSHA,
		},
		ExecutionPolicy: contract.ExecutionPolicyRef{
			Schema: contract.ExecutionPolicySchemaV1Alpha1, Digest: identity("execution-policy"),
		},
		EscalationPolicy: contract.EscalationPolicyRef{
			Schema: contract.EscalationPolicySchemaV1Alpha1, Digest: identity("escalation-policy"),
		},
	}
}
