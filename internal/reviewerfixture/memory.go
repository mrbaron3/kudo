package reviewerfixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"

	githubadapter "github.com/mrbaron3/kudo/internal/adapter/github"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/issueworker"
	"github.com/mrbaron3/kudo/internal/workflow"
)

type memoryRun struct {
	head       FixtureHead
	pull       issueworker.ClaimPullRequest
	title      string
	checkpoint contract.ClaimCheckpoint
}

type memoryComment struct {
	target  int64
	comment githubadapter.Comment
}

// MemoryTarget は GitHub と同じ marker/check identity 規則を使う deterministic fake である。
// Seed の結果だけをメモリに保持し、test 間や process restart を越えて state を共有しない。
type MemoryTarget struct {
	mu         sync.Mutex
	repository githubadapter.Repository
	identity   githubadapter.RecorderIdentity
	baseSHA    string
	runs       map[int64]memoryRun
	comments   []memoryComment
	checks     []githubadapter.CheckRun
	nextID     int64
}

func NewMemoryTarget(repository githubadapter.Repository, identity githubadapter.RecorderIdentity, baseSHA string) (*MemoryTarget, error) {
	if repository.Owner == "" || repository.Name == "" || identity.CommentAuthor.ID <= 0 ||
		identity.CheckRunApp.ID <= 0 || !validSHA(baseSHA) {
		return nil, errors.New("memory fixture repository、Implementer identity、base SHA は必須")
	}
	return &MemoryTarget{
		repository: repository,
		identity:   identity,
		baseSHA:    baseSHA,
		runs:       make(map[int64]memoryRun),
		nextID:     1,
	}, nil
}

func (m *MemoryTarget) EnsureFixtureHead(ctx context.Context, issue contract.IssueRef, file TestFile) (FixtureHead, error) {
	if err := contextError(ctx); err != nil {
		return FixtureHead{}, err
	}
	if !sameRepository(m.repository, issue) || issue.Number <= 0 {
		return FixtureHead{}, errors.New("fixture Issue が memory repository と一致しない")
	}
	if err := (Case{Name: "memory", Fault: FaultNone, TestFile: file, TestPlan: []byte("x"), RedEvidence: []byte("x")}).validate(); err != nil {
		return FixtureHead{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.runs[int64(issue.Number)]; ok {
		if existing.head.TestFile.Path != file.Path || !bytes.Equal(existing.head.TestFile.Data, file.Data) {
			return FixtureHead{}, errors.New("既存 fixture head が別 test payload を持つ")
		}
		return cloneHead(existing.head), nil
	}
	branchName := workflow.IssueBranchName(int64(issue.Number))
	bootstrapSHA := deterministicSHA("bootstrap", m.baseSHA, strconv.Itoa(issue.Number))
	headSHA := deterministicSHA("test-only", bootstrapSHA, file.Path, string(file.Data))
	head := FixtureHead{
		Base:         issueworker.ClaimBase{Name: "main", SHA: m.baseSHA},
		BranchName:   branchName,
		BootstrapSHA: bootstrapSHA,
		HeadSHA:      headSHA,
		TestFile:     TestFile{Path: file.Path, Data: bytes.Clone(file.Data)},
	}
	m.runs[int64(issue.Number)] = memoryRun{head: head}
	return cloneHead(head), nil
}

func (m *MemoryTarget) EnsureDraftPullRequest(ctx context.Context, input issueworker.DraftPullRequestInput) (issueworker.ClaimPullRequest, error) {
	if err := contextError(ctx); err != nil {
		return issueworker.ClaimPullRequest{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[int64(input.Issue.Number)]
	if !ok || run.head.BranchName != input.BranchName || run.head.HeadSHA != input.HeadSHA ||
		run.head.Base != input.Base {
		return issueworker.ClaimPullRequest{}, errors.New("draft Pull Request input が fixture head と一致しない")
	}
	if run.pull.Number > 0 {
		if run.title != input.Title || run.checkpoint != input.Checkpoint {
			return issueworker.ClaimPullRequest{}, errors.New("既存 fixture Pull Request が別 identity を持つ")
		}
		return run.pull, nil
	}
	pullNumber := input.Issue.Number + 1000
	pull := issueworker.ClaimPullRequest{
		Number: int(pullNumber), State: "open", Draft: true,
		HeadSHA: input.HeadSHA, BaseSHA: input.Base.SHA, Checkpoint: &input.Checkpoint,
	}
	run.pull = pull
	run.title = input.Title
	run.checkpoint = input.Checkpoint
	m.runs[int64(input.Issue.Number)] = run
	return pull, nil
}

func (m *MemoryTarget) EnsureComment(ctx context.Context, targetNumber int64, record githubadapter.CommentRecord) (githubadapter.Comment, bool, error) {
	if err := contextError(ctx); err != nil {
		return githubadapter.Comment{}, false, err
	}
	body, err := githubadapter.RenderComment(record.Body, record.Marker, record.MachineBlock)
	if err != nil {
		return githubadapter.Comment{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasPullRequest(targetNumber) {
		return githubadapter.Comment{}, false, errors.New("comment target Pull Request が存在しない")
	}
	for _, stored := range m.comments {
		if stored.target != targetNumber {
			continue
		}
		comment := stored.comment
		markers, parseErr := githubadapter.ParseMarkers(string(comment.Body))
		if parseErr != nil {
			return githubadapter.Comment{}, false, parseErr
		}
		if len(markers) == 1 && markers[0] == record.Marker && comment.Author.ID == m.identity.CommentAuthor.ID {
			return comment, false, nil
		}
	}
	comment := githubadapter.Comment{
		ID: m.allocateID(), Body: []byte(body), Author: m.identity.CommentAuthor,
	}
	m.comments = append(m.comments, memoryComment{target: targetNumber, comment: comment})
	return comment, true, nil
}

func (m *MemoryTarget) EnsureUnmarkedComment(ctx context.Context, targetNumber int64, body string) (githubadapter.Comment, bool, error) {
	if err := contextError(ctx); err != nil {
		return githubadapter.Comment{}, false, err
	}
	if targetNumber <= 0 || body == "" {
		return githubadapter.Comment{}, false, errors.New("unmarked comment target と body は必須")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasPullRequest(targetNumber) {
		return githubadapter.Comment{}, false, errors.New("comment target Pull Request が存在しない")
	}
	for _, stored := range m.comments {
		if stored.target != targetNumber {
			continue
		}
		comment := stored.comment
		if comment.Author.ID == m.identity.CommentAuthor.ID && string(comment.Body) == body {
			return comment, false, nil
		}
	}
	comment := githubadapter.Comment{ID: m.allocateID(), Body: []byte(body), Author: m.identity.CommentAuthor}
	m.comments = append(m.comments, memoryComment{target: targetNumber, comment: comment})
	return comment, true, nil
}

func (m *MemoryTarget) EnsureCheckRun(ctx context.Context, record githubadapter.CheckRunRecord) (githubadapter.CheckRun, bool, error) {
	if err := contextError(ctx); err != nil {
		return githubadapter.CheckRun{}, false, err
	}
	text, err := githubadapter.RenderCheckRunText(record.Marker, record.MachineBlock)
	if err != nil {
		return githubadapter.CheckRun{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, check := range m.checks {
		markers, parseErr := githubadapter.ParseMarkers(check.Output.Text)
		if parseErr != nil {
			return githubadapter.CheckRun{}, false, parseErr
		}
		if len(markers) == 1 && markers[0] == record.Marker && check.App.ID == m.identity.CheckRunApp.ID {
			return check, false, nil
		}
	}
	check := githubadapter.CheckRun{
		ID: m.allocateID(), Name: record.Name, HeadSHA: record.HeadSHA,
		Status: "completed", Conclusion: record.Conclusion, App: m.identity.CheckRunApp,
		Output: githubadapter.CheckRunOutput{Title: record.Title, Summary: record.Summary, Text: text},
	}
	m.checks = append(m.checks, check)
	return check, true, nil
}

type MemoryCounts struct {
	Branches     int
	PullRequests int
	Comments     int
	CheckRuns    int
}

func (m *MemoryTarget) Counts() MemoryCounts {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := MemoryCounts{Branches: len(m.runs), Comments: len(m.comments), CheckRuns: len(m.checks)}
	for _, run := range m.runs {
		if run.pull.Number > 0 {
			counts.PullRequests++
		}
	}
	return counts
}

func (m *MemoryTarget) allocateID() int64 {
	id := m.nextID
	m.nextID++
	return id
}

func (m *MemoryTarget) hasPullRequest(number int64) bool {
	for _, run := range m.runs {
		if int64(run.pull.Number) == number {
			return true
		}
	}
	return false
}

func deterministicSHA(parts ...string) string {
	digest := sha256.Sum256([]byte(stringsWithLength(parts)))
	return hex.EncodeToString(digest[:20])
}

func stringsWithLength(parts []string) string {
	var result string
	for _, part := range parts {
		result += strconv.Itoa(len(part)) + ":" + part
	}
	return result
}

func validSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context は必須")
	}
	return ctx.Err()
}

func sameRepository(repository githubadapter.Repository, issue contract.IssueRef) bool {
	return stringsEqualFold(repository.Owner, issue.Owner) && stringsEqualFold(repository.Name, issue.Repository)
}

func stringsEqualFold(left, right string) bool {
	return bytes.EqualFold([]byte(left), []byte(right))
}

func cloneHead(head FixtureHead) FixtureHead {
	head.TestFile.Data = bytes.Clone(head.TestFile.Data)
	return head
}
