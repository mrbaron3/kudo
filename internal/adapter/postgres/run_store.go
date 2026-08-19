package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const activeRunConstraint = "runs_one_writer_per_issue"

// runDB は pool と既存 transaction の共通境界である。pgx.Tx を渡した場合、各操作の
// transaction は savepoint となり、呼び出し側の transaction に参加する。
type runDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RunStore は workflow.Run と append-only lineage を同じ transaction で永続化する。
type RunStore struct {
	db runDB
}

// Transition は既に application 層で決定された次 Run と CAS 条件を運ぶ。
// Store は phase と event の組み合わせを再判定しない。
type Transition struct {
	ExpectedVersion int64
	Event           workflow.EventKind
	Run             workflow.Run
	Observation     *contract.IssueObservationRef
}

// ObservationRecord は observation ref が current になった Run version を表す。
type ObservationRecord struct {
	RunVersion int64
	Ref        contract.IssueObservationRef
}

// TransitionRecord は一回の永続化済み phase 遷移を表す。
type TransitionRecord struct {
	Version int64
	Event   workflow.EventKind
	From    workflow.Phase
	To      workflow.Phase
}

// NewRunStore は pool または pgx.Tx を使う store を返す。pgx.Tx を渡した操作は
// savepoint を使うため、最終 commit/rollback は外側の transaction が所有する。
func NewRunStore(db runDB) *RunStore {
	return &RunStore{db: db}
}

// Create は claimed/Version 0 の Run を version 1 として初期履歴ごと保存する。
func (s *RunStore) Create(ctx context.Context, run workflow.Run, observation contract.IssueObservationRef) (workflow.Run, error) {
	if err := validateCreate(run, observation); err != nil {
		return workflow.Run{}, err
	}

	normalizedIssue, err := parseIssueRef(run.Issue.String())
	if err != nil {
		return workflow.Run{}, invalidRun("Issue reference が不正: %v", err)
	}
	run.Issue = normalizedIssue
	run.Version = 1

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return workflow.Run{}, fmt.Errorf("Run 作成 transaction を開始する: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	refs := []versionedRef{
		{Schema: run.Input.ContextManifest.Schema, Digest: run.Input.ContextManifest.Digest},
		{Schema: run.Input.ExecutionPolicy.Schema, Digest: run.Input.ExecutionPolicy.Digest},
		{Schema: run.EscalationPolicy.Schema, Digest: run.EscalationPolicy.Digest},
		{Schema: observation.Schema, Digest: observation.Digest},
	}
	for _, ref := range refs {
		if err := insertArtifactRef(ctx, tx, ref); err != nil {
			return workflow.Run{}, fmt.Errorf("Run artifact ref を保存する: %w", err)
		}
	}

	if err := insertRun(ctx, tx, run); err != nil {
		return workflow.Run{}, classifyWriteError("Run を保存する", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_transitions
			(run_id, version, event_kind, from_phase, to_phase)
		VALUES ($1, $2, $3, $4, $5)
	`, run.ID, run.Version, workflow.KindClaimSucceeded, nil, run.Phase); err != nil {
		return workflow.Run{}, fmt.Errorf("初期 transition を保存する: %w", err)
	}
	if err := insertObservation(ctx, tx, run.ID, run.Version, observation); err != nil {
		return workflow.Run{}, fmt.Errorf("初期 Issue Observation を保存する: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return workflow.Run{}, classifyWriteError("Run 作成 transaction を確定する", err)
	}
	return run, nil
}

// Load は Run の全 binding を復元する。保存値が contract を満たさない場合は
// ErrCorruptRun を返し、未知の schema version 自体は拒否しない。
func (s *RunStore) Load(ctx context.Context, id string) (workflow.Run, error) {
	if id == "" {
		return workflow.Run{}, invalidRun("Run ID が空")
	}
	run, err := loadRun(ctx, s.db, id, false)
	if err != nil {
		return workflow.Run{}, err
	}
	return run, nil
}

// Transition は対象 Run を行ロックし、ExpectedVersion が一致した場合だけ version を
// 一つ進める。phase/event の合法性は application 層の判断をそのまま保存する。
func (s *RunStore) Transition(ctx context.Context, transition Transition) (workflow.Run, error) {
	if err := validateTransition(transition); err != nil {
		return workflow.Run{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return workflow.Run{}, fmt.Errorf("transition transaction を開始する: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := loadRun(ctx, tx, transition.Run.ID, true)
	if err != nil {
		return workflow.Run{}, err
	}
	if current.Version != transition.ExpectedVersion {
		return workflow.Run{}, fmt.Errorf("%w: got %d, want %d", ErrVersionConflict, transition.ExpectedVersion, current.Version)
	}
	if current.Issue.String() != transition.Run.Issue.String() || current.Input != transition.Run.Input {
		return workflow.Run{}, ErrImmutableRunInput
	}

	observationRecorded := transition.Event == workflow.KindObservationRecorded
	appendObservation := false
	if observationRecorded {
		if transition.Observation == nil {
			return workflow.Run{}, invalidRun("Observation event には IssueObservationRef が必要")
		}
		if err := validateVersionedRef("Issue Observation", transition.Observation.Schema, transition.Observation.Digest); err != nil {
			return workflow.Run{}, err
		}
		if transition.Observation.Digest != transition.Run.Observation {
			return workflow.Run{}, invalidRun("IssueObservationRef digest が Run の Observation と一致しない")
		}
		currentObservation, loadErr := loadLatestObservation(ctx, tx, current.ID)
		if loadErr != nil {
			return workflow.Run{}, loadErr
		}
		appendObservation = currentObservation != *transition.Observation
	} else {
		if transition.Observation != nil {
			return workflow.Run{}, invalidRun("Observation event 以外は IssueObservationRef を追記できない")
		}
		if transition.Run.Observation != current.Observation {
			return workflow.Run{}, invalidRun("Observation event 以外は Run の Observation を変更できない")
		}
	}

	next := transition.Run
	next.Issue = current.Issue
	next.Version = current.Version + 1
	if next.PullRequest != (contract.PullRequestRef{}) {
		normalized, parseErr := parsePullRequestRef(next.PullRequest.String())
		if parseErr != nil {
			return workflow.Run{}, invalidRun("Pull Request reference が不正: %v", parseErr)
		}
		next.PullRequest = normalized
	}

	if appendObservation {
		ref := versionedRef{Schema: transition.Observation.Schema, Digest: transition.Observation.Digest}
		if err := insertArtifactRef(ctx, tx, ref); err != nil {
			return workflow.Run{}, fmt.Errorf("Issue Observation artifact ref を保存する: %w", err)
		}
	}

	result, err := tx.Exec(ctx, `
		UPDATE runs
		SET version = $2,
			phase = $3,
			pull_request_ref = $4,
			fixed_head = $5,
			published_head = $6,
			published_test_head = $7,
			checks_head = $8,
			test_approval_head = $9,
			test_approval_request_digest = $10,
			final_approval_head = $11,
			final_approval_request_digest = $12,
			rounds_test_validity = $13,
			rounds_final_implementation = $14,
			total_rounds_test_validity = $15,
			total_rounds_final_implementation = $16
		WHERE id = $1 AND version = $17
	`, next.ID, next.Version, next.Phase, nullablePullRequest(next.PullRequest),
		next.FixedHead, next.PublishedHead, next.PublishedTestHead, next.ChecksHead,
		nullableApprovalHead(next.TestApproval), nullableApprovalDigest(next.TestApproval),
		nullableApprovalHead(next.FinalApproval), nullableApprovalDigest(next.FinalApproval),
		next.Rounds.TestValidity, next.Rounds.FinalImplementation,
		next.TotalRounds.TestValidity, next.TotalRounds.FinalImplementation,
		transition.ExpectedVersion)
	if err != nil {
		return workflow.Run{}, classifyWriteError("Run transition を保存する", err)
	}
	if result.RowsAffected() != 1 {
		return workflow.Run{}, ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_transitions
			(run_id, version, event_kind, from_phase, to_phase)
		VALUES ($1, $2, $3, $4, $5)
	`, next.ID, next.Version, transition.Event, current.Phase, next.Phase); err != nil {
		return workflow.Run{}, fmt.Errorf("transition 履歴を保存する: %w", err)
	}
	if appendObservation {
		if err := insertObservation(ctx, tx, next.ID, next.Version, *transition.Observation); err != nil {
			return workflow.Run{}, fmt.Errorf("Issue Observation lineage を保存する: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return workflow.Run{}, classifyWriteError("transition transaction を確定する", err)
	}
	return next, nil
}

// ObservationLineage は current になった observation ref を version 昇順で返す。
func (s *RunStore) ObservationLineage(ctx context.Context, id string) ([]ObservationRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT run_version, schema, digest
		FROM run_issue_observations
		WHERE run_id = $1
		ORDER BY run_version
	`, id)
	if err != nil {
		return nil, fmt.Errorf("Issue Observation lineage を取得する: %w", err)
	}
	defer rows.Close()

	records := make([]ObservationRecord, 0)
	for rows.Next() {
		var record ObservationRecord
		if err := rows.Scan(&record.RunVersion, &record.Ref.Schema, &record.Ref.Digest); err != nil {
			return nil, fmt.Errorf("Issue Observation lineage を読む: %w", err)
		}
		if record.RunVersion < 1 {
			return nil, corruptRun("Issue Observation run version が不正: %d", record.RunVersion)
		}
		if err := validateStoredVersionedRef("Issue Observation", record.Ref.Schema, record.Ref.Digest); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Issue Observation lineage を読む: %w", err)
	}
	if len(records) == 0 {
		rows.Close()
		if err := s.requireRun(ctx, id); err != nil {
			return nil, err
		}
		return nil, corruptRun("Run %q に初期 Issue Observation が無い", id)
	}
	return records, nil
}

// TransitionHistory は初期 claim を含む全 transition を version 昇順で返す。
func (s *RunStore) TransitionHistory(ctx context.Context, id string) ([]TransitionRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT version, event_kind, from_phase, to_phase
		FROM run_transitions
		WHERE run_id = $1
		ORDER BY version
	`, id)
	if err != nil {
		return nil, fmt.Errorf("transition 履歴を取得する: %w", err)
	}
	defer rows.Close()

	records := make([]TransitionRecord, 0)
	for rows.Next() {
		var record TransitionRecord
		var from sql.NullString
		if err := rows.Scan(&record.Version, &record.Event, &from, &record.To); err != nil {
			return nil, fmt.Errorf("transition 履歴を読む: %w", err)
		}
		if from.Valid {
			record.From = workflow.Phase(from.String)
		}
		if record.Version < 1 || !knownEvent(record.Event) || !knownPhase(record.To) ||
			(record.From != workflow.PhaseNew && !knownPhase(record.From)) {
			return nil, corruptRun("不正な transition 履歴: %#v", record)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transition 履歴を読む: %w", err)
	}
	if len(records) == 0 {
		rows.Close()
		if err := s.requireRun(ctx, id); err != nil {
			return nil, err
		}
		return nil, corruptRun("Run %q に初期 transition が無い", id)
	}
	return records, nil
}

func (s *RunStore) requireRun(ctx context.Context, id string) error {
	var exists int
	err := s.db.QueryRow(ctx, "SELECT 1 FROM runs WHERE id = $1", id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("Run の存在を確認する: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

type runQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadRun(ctx context.Context, db runQuerier, id string, forUpdate bool) (workflow.Run, error) {
	query := `
		SELECT run.id, run.issue_ref, run.version, run.phase,
			run.context_manifest_schema, run.context_manifest_digest,
			run.execution_policy_schema, run.execution_policy_digest,
			run.escalation_policy_schema, run.escalation_policy_digest,
			observation.digest, run.pull_request_ref,
			run.fixed_head, run.published_head, run.published_test_head, run.checks_head,
			run.test_approval_head, run.test_approval_request_digest,
			run.final_approval_head, run.final_approval_request_digest,
			run.round_limit_test_validity, run.round_limit_final_implementation,
			run.rounds_test_validity, run.rounds_final_implementation,
			run.total_rounds_test_validity, run.total_rounds_final_implementation
		FROM runs AS run
		LEFT JOIN LATERAL (
			SELECT digest
			FROM run_issue_observations
			WHERE run_id = run.id
			ORDER BY run_version DESC
			LIMIT 1
		) AS observation ON true
		WHERE run.id = $1`
	if forUpdate {
		query += " FOR UPDATE OF run"
	}
	run, err := scanRun(db.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.Run{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	if err != nil {
		return workflow.Run{}, fmt.Errorf("Run を読む: %w", err)
	}
	return run, nil
}

func loadLatestObservation(ctx context.Context, db runQuerier, id string) (contract.IssueObservationRef, error) {
	var ref contract.IssueObservationRef
	err := db.QueryRow(ctx, `
		SELECT schema, digest
		FROM run_issue_observations
		WHERE run_id = $1
		ORDER BY run_version DESC
		LIMIT 1
	`, id).Scan(&ref.Schema, &ref.Digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.IssueObservationRef{}, corruptRun("Run %q に初期 Issue Observation が無い", id)
	}
	if err != nil {
		return contract.IssueObservationRef{}, fmt.Errorf("最新 Issue Observation を読む: %w", err)
	}
	if err := validateStoredVersionedRef("Issue Observation", ref.Schema, ref.Digest); err != nil {
		return contract.IssueObservationRef{}, err
	}
	return ref, nil
}

func scanRun(row rowScanner) (workflow.Run, error) {
	var (
		run                                          workflow.Run
		issueRef, observationDigest, pullRequestRef  sql.NullString
		testHead, testDigest, finalHead, finalDigest sql.NullString
	)
	err := row.Scan(
		&run.ID, &issueRef, &run.Version, &run.Phase,
		&run.Input.ContextManifest.Schema, &run.Input.ContextManifest.Digest,
		&run.Input.ExecutionPolicy.Schema, &run.Input.ExecutionPolicy.Digest,
		&run.EscalationPolicy.Schema, &run.EscalationPolicy.Digest,
		&observationDigest, &pullRequestRef,
		&run.FixedHead, &run.PublishedHead, &run.PublishedTestHead, &run.ChecksHead,
		&testHead, &testDigest, &finalHead, &finalDigest,
		&run.RoundLimits.TestValidity, &run.RoundLimits.FinalImplementation,
		&run.Rounds.TestValidity, &run.Rounds.FinalImplementation,
		&run.TotalRounds.TestValidity, &run.TotalRounds.FinalImplementation,
	)
	if err != nil {
		return workflow.Run{}, err
	}
	if !issueRef.Valid {
		return workflow.Run{}, corruptRun("Issue reference が NULL")
	}
	if !observationDigest.Valid {
		return workflow.Run{}, corruptRun("Issue Observation lineage が無い")
	}
	run.Observation = contract.Digest(observationDigest.String)
	run.Issue, err = parseIssueRef(issueRef.String)
	if err != nil {
		return workflow.Run{}, corruptRun("Issue reference が不正: %v", err)
	}
	if pullRequestRef.Valid {
		if pullRequestRef.String != "" {
			run.PullRequest, err = parsePullRequestRef(pullRequestRef.String)
			if err != nil {
				return workflow.Run{}, corruptRun("Pull Request reference が不正: %v", err)
			}
		}
	}
	run.TestApproval, err = scanApproval("test", testHead, testDigest)
	if err != nil {
		return workflow.Run{}, err
	}
	run.FinalApproval, err = scanApproval("final", finalHead, finalDigest)
	if err != nil {
		return workflow.Run{}, err
	}
	if err := validateStoredRun(run); err != nil {
		return workflow.Run{}, err
	}
	return run, nil
}

func insertRun(ctx context.Context, tx pgx.Tx, run workflow.Run) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO runs (
			id, issue_ref, version, phase,
			context_manifest_schema, context_manifest_digest,
			execution_policy_schema, execution_policy_digest,
			escalation_policy_schema, escalation_policy_digest,
			pull_request_ref,
			fixed_head, published_head, published_test_head, checks_head,
			test_approval_head, test_approval_request_digest,
			final_approval_head, final_approval_request_digest,
			round_limit_test_validity, round_limit_final_implementation,
			rounds_test_validity, rounds_final_implementation,
			total_rounds_test_validity, total_rounds_final_implementation
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23, $24, $25
		)
	`, run.ID, run.Issue.String(), run.Version, run.Phase,
		run.Input.ContextManifest.Schema, run.Input.ContextManifest.Digest,
		run.Input.ExecutionPolicy.Schema, run.Input.ExecutionPolicy.Digest,
		run.EscalationPolicy.Schema, run.EscalationPolicy.Digest,
		nullablePullRequest(run.PullRequest),
		run.FixedHead, run.PublishedHead, run.PublishedTestHead, run.ChecksHead,
		nullableApprovalHead(run.TestApproval), nullableApprovalDigest(run.TestApproval),
		nullableApprovalHead(run.FinalApproval), nullableApprovalDigest(run.FinalApproval),
		run.RoundLimits.TestValidity, run.RoundLimits.FinalImplementation,
		run.Rounds.TestValidity, run.Rounds.FinalImplementation,
		run.TotalRounds.TestValidity, run.TotalRounds.FinalImplementation,
	)
	return err
}

type versionedRef struct {
	Schema string
	Digest contract.Digest
}

func insertArtifactRef(ctx context.Context, tx pgx.Tx, ref versionedRef) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO artifact_refs (schema, digest)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, ref.Schema, ref.Digest)
	return err
}

func insertObservation(ctx context.Context, tx pgx.Tx, runID string, version int64, ref contract.IssueObservationRef) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO run_issue_observations (run_id, run_version, schema, digest)
		VALUES ($1, $2, $3, $4)
	`, runID, version, ref.Schema, ref.Digest)
	return err
}

func validateCreate(run workflow.Run, observation contract.IssueObservationRef) error {
	if run.Version != 0 {
		return invalidRun("Create 前の Version は 0 でなければならない: %d", run.Version)
	}
	if run.Phase != workflow.PhaseClaimed {
		return invalidRun("Create できる phase は claimed だけである: %q", run.Phase)
	}
	if err := validateRunIdentity(run); err != nil {
		return err
	}
	if err := validateVersionedRef("Issue Observation", observation.Schema, observation.Digest); err != nil {
		return err
	}
	if run.Observation != observation.Digest {
		return invalidRun("初期 IssueObservationRef digest が Run の Observation と一致しない")
	}
	if err := validateVersionedRef("Escalation Policy", run.EscalationPolicy.Schema, run.EscalationPolicy.Digest); err != nil {
		return err
	}
	// round 上限は claim の時点で解決済みでなければならない。0 を許すと最初の
	// request_changes で必ず escalate する Run が gate を持つ顔で保存される。
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"testValidity", run.RoundLimits.TestValidity},
		{"finalImplementation", run.RoundLimits.FinalImplementation},
	} {
		if limit.value < contract.MinReviewRounds || limit.value > contract.MaxReviewRounds {
			return invalidRun("review round 上限 %s は %d 以上 %d 以下でなければならない: %d",
				limit.name, contract.MinReviewRounds, contract.MaxReviewRounds, limit.value)
		}
	}
	return nil
}

func validateTransition(transition Transition) error {
	if transition.ExpectedVersion < 1 {
		return invalidRun("expected version が不正: %d", transition.ExpectedVersion)
	}
	if transition.Run.Version != transition.ExpectedVersion {
		return invalidRun("次 Run の Version は expected version のままでなければならない: got %d, want %d",
			transition.Run.Version, transition.ExpectedVersion)
	}
	if !knownEvent(transition.Event) {
		return invalidRun("event kind が不正: %q", transition.Event)
	}
	return validateRunIdentity(transition.Run)
}

func validateRunIdentity(run workflow.Run) error {
	if run.ID == "" {
		return invalidRun("Run ID が空")
	}
	if _, err := parseIssueRef(run.Issue.String()); err != nil {
		return invalidRun("Issue reference が不正: %v", err)
	}
	if !knownPhase(run.Phase) {
		return invalidRun("phase が不正: %q", run.Phase)
	}
	if err := validateVersionedRef("Context Manifest", run.Input.ContextManifest.Schema, run.Input.ContextManifest.Digest); err != nil {
		return err
	}
	if err := validateVersionedRef("Execution Policy", run.Input.ExecutionPolicy.Schema, run.Input.ExecutionPolicy.Digest); err != nil {
		return err
	}
	if !run.Observation.Valid() {
		return invalidRun("Observation digest が不正: %q", run.Observation)
	}
	if run.PullRequest != (contract.PullRequestRef{}) {
		if _, err := parsePullRequestRef(run.PullRequest.String()); err != nil {
			return invalidRun("Pull Request reference が不正: %v", err)
		}
	}
	if err := validateApproval("test", run.TestApproval); err != nil {
		return err
	}
	return validateApproval("final", run.FinalApproval)
}

func validateStoredRun(run workflow.Run) error {
	if run.Version < 1 {
		return corruptRun("version が不正: %d", run.Version)
	}
	if err := validateRunIdentity(run); err != nil {
		return fmt.Errorf("%w: %v", ErrCorruptRun, err)
	}
	return nil
}

func validateVersionedRef(name, schema string, digest contract.Digest) error {
	if schema == "" {
		return invalidRun("%s schema が空", name)
	}
	if !digest.Valid() {
		return invalidRun("%s digest が不正: %q", name, digest)
	}
	return nil
}

func validateStoredVersionedRef(name, schema string, digest contract.Digest) error {
	if schema == "" || !digest.Valid() {
		return corruptRun("%s ref が不正: schema=%q digest=%q", name, schema, digest)
	}
	return nil
}

func validateApproval(name string, approval *workflow.Approval) error {
	if approval == nil {
		return nil
	}
	if approval.Head == "" {
		return invalidRun("%s approval head が空", name)
	}
	if !approval.RequestDigest.Valid() {
		return invalidRun("%s approval request digest が不正: %q", name, approval.RequestDigest)
	}
	return nil
}

func scanApproval(name string, head, digest sql.NullString) (*workflow.Approval, error) {
	if head.Valid != digest.Valid {
		return nil, corruptRun("%s approval binding の NULL が不整合", name)
	}
	if !head.Valid {
		return nil, nil
	}
	requestDigest := contract.Digest(digest.String)
	if head.String == "" || !requestDigest.Valid() {
		return nil, corruptRun("%s approval binding が不正", name)
	}
	return &workflow.Approval{Head: head.String, RequestDigest: requestDigest}, nil
}

func nullablePullRequest(ref contract.PullRequestRef) any {
	if ref == (contract.PullRequestRef{}) {
		return ""
	}
	return ref.String()
}

func nullableApprovalHead(approval *workflow.Approval) any {
	if approval == nil {
		return nil
	}
	return approval.Head
}

func nullableApprovalDigest(approval *workflow.Approval) any {
	if approval == nil {
		return nil
	}
	return approval.RequestDigest
}

func parseIssueRef(raw string) (contract.IssueRef, error) {
	owner, repository, number, err := parseGitHubRef(raw, "issues")
	if err != nil {
		return contract.IssueRef{}, err
	}
	ref := contract.IssueRef{Owner: owner, Repository: repository, Number: number}
	if ref.String() != raw {
		return contract.IssueRef{}, fmt.Errorf("canonical Issue reference でない: %q", raw)
	}
	return ref, nil
}

func parsePullRequestRef(raw string) (contract.PullRequestRef, error) {
	owner, repository, number, err := parseGitHubRef(raw, "pull")
	if err != nil {
		return contract.PullRequestRef{}, err
	}
	ref := contract.PullRequestRef{Owner: owner, Repository: repository, Number: number}
	if ref.String() != raw {
		return contract.PullRequestRef{}, fmt.Errorf("canonical Pull Request reference でない: %q", raw)
	}
	return ref, nil
}

func parseGitHubRef(raw, resource string) (string, string, int, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, err
	}
	if parsed.Scheme != "github" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, fmt.Errorf("形式が不正: %q", raw)
	}
	parts := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != resource || parts[2] == "" {
		return "", "", 0, fmt.Errorf("path が不正: %q", raw)
	}
	repository, err := url.PathUnescape(parts[0])
	if err != nil || repository != parts[0] {
		return "", "", 0, fmt.Errorf("repository が不正: %q", parts[0])
	}
	number, err := strconv.Atoi(parts[2])
	if err != nil || number <= 0 {
		return "", "", 0, fmt.Errorf("number が不正: %q", parts[2])
	}
	return parsed.Host, repository, number, nil
}

func knownPhase(phase workflow.Phase) bool {
	for _, candidate := range workflow.Phases() {
		if phase == candidate {
			return true
		}
	}
	return false
}

func knownEvent(event workflow.EventKind) bool {
	for _, candidate := range workflow.EventKinds() {
		if event == candidate {
			return true
		}
	}
	return false
}

func classifyWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.ConstraintName == activeRunConstraint {
		return fmt.Errorf("%w: %s", ErrActiveRun, postgresError.Detail)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func invalidRun(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRun, fmt.Sprintf(format, args...))
}

func corruptRun(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorruptRun, fmt.Sprintf(format, args...))
}
