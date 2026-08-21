package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

const activeRunConstraint = "runs_one_writer_per_issue"

const storeTransactionCleanupTimeout = 5 * time.Second

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
	Review          *workflow.ReviewCompleted
}

// ObservationRecord は observation ref が current になった Run version を表す。
type ObservationRecord struct {
	RunVersion int64
	Ref        contract.IssueObservationRef
	BodyDigest contract.Digest
}

// TransitionRecord は一回の永続化済み phase 遷移を表す。
type TransitionRecord struct {
	Version int64
	Event   workflow.EventKind
	From    workflow.Phase
	To      workflow.Phase
}

// ReviewRecord は Review Result と、その判断を採用した Run version の binding である。
type ReviewRecord struct {
	RunVersion    int64
	Round         int
	Kind          contract.ReviewKind
	Head          string
	RequestDigest contract.Digest
	ResultDigest  contract.Digest
	Verdict       contract.ReviewVerdict
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
	defer rollbackStoreTransaction(ctx, tx)

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
	if err := insertObservation(ctx, tx, run.ID, run.Version, observation, run.ObservationBodyDigest); err != nil {
		return workflow.Run{}, fmt.Errorf("初期 Issue Observation を保存する: %w", err)
	}
	if err := commitStoreTransaction(ctx, tx); err != nil {
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
	defer rollbackStoreTransaction(ctx, tx)

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
	if current.EscalationPolicy != transition.Run.EscalationPolicy ||
		current.RoundLimits != transition.Run.RoundLimits {
		return workflow.Run{}, invalidRun("claim 時に固定した Escalation Policy と review round 上限は変更できない")
	}
	if transition.Run.TotalRounds.TestValidity < current.TotalRounds.TestValidity ||
		transition.Run.TotalRounds.FinalImplementation < current.TotalRounds.FinalImplementation {
		return workflow.Run{}, invalidRun(
			"生涯 review round counter は巻き戻せない: got %+v, current %+v",
			transition.Run.TotalRounds, current.TotalRounds,
		)
	}
	if !consumesReviewRound(transition.Event) && transition.Run.TotalRounds != current.TotalRounds {
		return workflow.Run{}, invalidRun("review round を消費しない event は生涯 review round counter を変更できない")
	}

	observationRecorded := transition.Event == workflow.KindObservationRecorded
	appendObservation := false
	if observationRecorded {
		if transition.Observation == nil {
			return workflow.Run{}, invalidRun("Observation event には IssueObservationRef が必要")
		}
		if err := validateVersionedRef(
			"Issue Observation",
			transition.Observation.Schema,
			transition.Observation.Digest,
			transition.Observation.Valid(),
		); err != nil {
			return workflow.Run{}, err
		}
		if *transition.Observation != transition.Run.Observation {
			return workflow.Run{}, invalidRun("IssueObservationRef が Run の Observation と一致しない")
		}
		if !transition.Run.ObservationBodyDigest.Valid() {
			return workflow.Run{}, invalidRun(
				"Issue 本文 digest が不正: %q", transition.Run.ObservationBodyDigest)
		}
		currentObservation, loadErr := loadLatestObservation(ctx, tx, current.ID)
		if loadErr != nil {
			return workflow.Run{}, loadErr
		}
		// ref と本文 digest のどちらかが動いたら追記する。ref だけで判定すると、
		// 同じ ref のまま本文 digest が変わった観測が lineage に残らず、復元した Run
		// だけ古い本文 digest を指す。
		appendObservation = currentObservation != *transition.Observation ||
			current.ObservationBodyDigest != transition.Run.ObservationBodyDigest
	} else {
		if transition.Observation != nil {
			return workflow.Run{}, invalidRun("Observation event 以外は IssueObservationRef を追記できない")
		}
		if transition.Run.Observation != current.Observation {
			return workflow.Run{}, invalidRun("Observation event 以外は Run の Observation を変更できない")
		}
		if transition.Run.ObservationBodyDigest != current.ObservationBodyDigest {
			return workflow.Run{}, invalidRun("Observation event 以外は Run の Issue 本文 digest を変更できない")
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
	reviewRound := 0
	if transition.Review != nil {
		reviewRound, err = validateReviewProgress(current, next, *transition.Review)
		if err != nil {
			return workflow.Run{}, err
		}
	} else if transition.Event == workflow.KindTestRevisionRequired {
		if _, err := validateRoundConsumption(current, next, contract.ReviewTestValidity); err != nil {
			return workflow.Run{}, err
		}
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
			test_approval_result_digest = $11,
			final_approval_head = $12,
			final_approval_request_digest = $13,
			final_approval_result_digest = $14,
			rounds_test_validity = $15,
			rounds_final_implementation = $16,
			total_rounds_test_validity = $17,
			total_rounds_final_implementation = $18
		WHERE id = $1 AND version = $19
	`, next.ID, next.Version, next.Phase, nullablePullRequest(next.PullRequest),
		next.FixedHead, next.PublishedHead, next.PublishedTestHead, next.ChecksHead,
		nullableApprovalHead(next.TestApproval), nullableApprovalRequestDigest(next.TestApproval),
		nullableApprovalResultDigest(next.TestApproval),
		nullableApprovalHead(next.FinalApproval), nullableApprovalRequestDigest(next.FinalApproval),
		nullableApprovalResultDigest(next.FinalApproval),
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
	if transition.Review != nil {
		if err := insertReviewBinding(ctx, tx, next.ID, next.Version, reviewRound, *transition.Review); err != nil {
			return workflow.Run{}, fmt.Errorf("Review binding を保存する: %w", err)
		}
	}
	if appendObservation {
		if err := insertObservation(ctx, tx, next.ID, next.Version, *transition.Observation, next.ObservationBodyDigest); err != nil {
			return workflow.Run{}, fmt.Errorf("Issue Observation lineage を保存する: %w", err)
		}
	}
	if err := commitStoreTransaction(ctx, tx); err != nil {
		return workflow.Run{}, classifyWriteError("transition transaction を確定する", err)
	}
	return next, nil
}

// ObservationLineage は current になった observation ref を version 昇順で返す。
func (s *RunStore) ObservationLineage(ctx context.Context, id string) ([]ObservationRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT run_version, schema, digest, body_digest
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
		if err := rows.Scan(&record.RunVersion, &record.Ref.Schema, &record.Ref.Digest, &record.BodyDigest); err != nil {
			return nil, fmt.Errorf("Issue Observation lineage を読む: %w", err)
		}
		if record.RunVersion < 1 {
			return nil, corruptRun("Issue Observation run version が不正: %d", record.RunVersion)
		}
		if err := validateStoredVersionedRef(
			"Issue Observation", record.Ref.Schema, record.Ref.Digest, record.Ref.Valid(),
		); err != nil {
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

// ReviewLineage は確定した Review Result binding を Run version 昇順で返す。
func (s *RunStore) ReviewLineage(ctx context.Context, id string) ([]ReviewRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT binding.run_version, binding.review_round, binding.review_kind, binding.head,
			binding.request_digest, binding.result_digest, binding.verdict, transition.event_kind
		FROM run_review_bindings AS binding
		JOIN run_transitions AS transition
			ON transition.run_id = binding.run_id AND transition.version = binding.run_version
		WHERE binding.run_id = $1
		ORDER BY binding.run_version
	`, id)
	if err != nil {
		return nil, fmt.Errorf("Review binding lineage を取得する: %w", err)
	}
	defer rows.Close()

	records := make([]ReviewRecord, 0)
	seenRounds := map[contract.ReviewKind]int{
		contract.ReviewTestValidity:        0,
		contract.ReviewFinalImplementation: 0,
	}
	var lastVersion int64
	for rows.Next() {
		var record ReviewRecord
		var event workflow.EventKind
		if err := rows.Scan(
			&record.RunVersion,
			&record.Round,
			&record.Kind,
			&record.Head,
			&record.RequestDigest,
			&record.ResultDigest,
			&record.Verdict,
			&event,
		); err != nil {
			return nil, fmt.Errorf("Review binding lineage を読む: %w", err)
		}
		if err := validateStoredReviewRecord(record); err != nil {
			return nil, err
		}
		if event != workflow.KindReviewCompleted || record.RunVersion <= lastVersion {
			return nil, corruptRun(
				"Review binding が review transition へ接続しない: version=%d event=%q",
				record.RunVersion, event,
			)
		}
		lastVersion = record.RunVersion
		seenRounds[record.Kind]++
		if record.Round != seenRounds[record.Kind] {
			return nil, corruptRun(
				"Review binding round が連続していない: kind=%q got=%d want=%d",
				record.Kind, record.Round, seenRounds[record.Kind],
			)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Review binding lineage を読む: %w", err)
	}
	rows.Close()

	run, err := loadRun(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if seenRounds[contract.ReviewTestValidity] != run.TotalRounds.TestValidity ||
		seenRounds[contract.ReviewFinalImplementation] != run.TotalRounds.FinalImplementation {
		return nil, corruptRun(
			"Review binding lineage が生涯 counter と一致しない: binding=%+v total=%+v",
			seenRounds, run.TotalRounds,
		)
	}
	return records, nil
}

// TransitionHistory は初期 claim を含む全 transition を version 昇順で返す。
func (s *RunStore) TransitionHistory(ctx context.Context, id string) ([]TransitionRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT transition.version, transition.event_kind, transition.from_phase, transition.to_phase,
			run.version, run.phase
		FROM run_transitions AS transition
		JOIN runs AS run ON run.id = transition.run_id
		WHERE transition.run_id = $1
		ORDER BY transition.version
	`, id)
	if err != nil {
		return nil, fmt.Errorf("transition 履歴を取得する: %w", err)
	}
	defer rows.Close()

	records := make([]TransitionRecord, 0)
	var runVersion int64
	var runPhase workflow.Phase
	for rows.Next() {
		var record TransitionRecord
		var from sql.NullString
		if err := rows.Scan(
			&record.Version, &record.Event, &from, &record.To, &runVersion, &runPhase,
		); err != nil {
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
	if err := validateTransitionHistory(records, runVersion, runPhase); err != nil {
		return nil, err
	}
	return records, nil
}

func validateTransitionHistory(records []TransitionRecord, runVersion int64, runPhase workflow.Phase) error {
	for i, record := range records {
		wantVersion := int64(i + 1)
		if record.Version != wantVersion {
			return corruptRun("transition version が連続していない: got %d, want %d", record.Version, wantVersion)
		}
		if i == 0 {
			if record.Event != workflow.KindClaimSucceeded || record.From != workflow.PhaseNew ||
				record.To != workflow.PhaseClaimed {
				return corruptRun("初期 transition が claim を表さない: %#v", record)
			}
			continue
		}
		if record.Event == workflow.KindClaimSucceeded || record.From != records[i-1].To {
			return corruptRun("transition phase が直前の履歴へ接続しない: %#v", record)
		}
	}
	last := records[len(records)-1]
	if last.Version != runVersion || last.To != runPhase {
		return corruptRun(
			"transition history が current Run と一致しない: history version=%d phase=%q, Run version=%d phase=%q",
			last.Version, last.To, runVersion, runPhase,
		)
	}
	return nil
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
			observation.schema, observation.digest, observation.body_digest, run.pull_request_ref,
			run.fixed_head, run.published_head, run.published_test_head, run.checks_head,
			run.test_approval_head, run.test_approval_request_digest, run.test_approval_result_digest,
			run.final_approval_head, run.final_approval_request_digest, run.final_approval_result_digest,
			run.round_limit_test_validity, run.round_limit_final_implementation,
			run.rounds_test_validity, run.rounds_final_implementation,
			run.total_rounds_test_validity, run.total_rounds_final_implementation
		FROM runs AS run
		LEFT JOIN LATERAL (
			SELECT schema, digest, body_digest
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
	if err := validateStoredVersionedRef("Issue Observation", ref.Schema, ref.Digest, ref.Valid()); err != nil {
		return contract.IssueObservationRef{}, err
	}
	return ref, nil
}

func scanRun(row rowScanner) (workflow.Run, error) {
	var (
		run                                                            workflow.Run
		issueRef, observationSchema, observationDigest, pullRequestRef sql.NullString
		observationBodyDigest                                          sql.NullString
		testHead, testRequest, testResult                              sql.NullString
		finalHead, finalRequest, finalResult                           sql.NullString
	)
	err := row.Scan(
		&run.ID, &issueRef, &run.Version, &run.Phase,
		&run.Input.ContextManifest.Schema, &run.Input.ContextManifest.Digest,
		&run.Input.ExecutionPolicy.Schema, &run.Input.ExecutionPolicy.Digest,
		&run.EscalationPolicy.Schema, &run.EscalationPolicy.Digest,
		&observationSchema, &observationDigest, &observationBodyDigest, &pullRequestRef,
		&run.FixedHead, &run.PublishedHead, &run.PublishedTestHead, &run.ChecksHead,
		&testHead, &testRequest, &testResult, &finalHead, &finalRequest, &finalResult,
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
	if observationSchema.Valid != observationDigest.Valid || observationSchema.Valid != observationBodyDigest.Valid {
		return workflow.Run{}, corruptRun("Issue Observation ref の NULL が不整合")
	}
	if !observationSchema.Valid {
		return workflow.Run{}, corruptRun("Issue Observation lineage が無い")
	}
	observation := contract.IssueObservationRef{
		Schema: observationSchema.String,
		Digest: contract.Digest(observationDigest.String),
	}
	if !observation.Valid() {
		return workflow.Run{}, corruptRun(
			"Issue Observation ref が不正: schema=%q digest=%q",
			observation.Schema, observation.Digest,
		)
	}
	bodyDigest := contract.Digest(observationBodyDigest.String)
	if !bodyDigest.Valid() {
		return workflow.Run{}, corruptRun("Issue 本文 digest が不正: %q", bodyDigest)
	}
	run.Observation = observation
	run.ObservationBodyDigest = bodyDigest
	run.Issue, err = parseIssueRef(issueRef.String)
	if err != nil {
		return workflow.Run{}, corruptRun("Issue reference が不正: %v", err)
	}
	if pullRequestRef.Valid {
		if pullRequestRef.String == "" {
			return workflow.Run{}, corruptRun("Pull Request reference が空文字")
		}
		run.PullRequest, err = parsePullRequestRef(pullRequestRef.String)
		if err != nil {
			return workflow.Run{}, corruptRun("Pull Request reference が不正: %v", err)
		}
	}
	run.TestApproval, err = scanApproval("test", testHead, testRequest, testResult)
	if err != nil {
		return workflow.Run{}, err
	}
	run.FinalApproval, err = scanApproval("final", finalHead, finalRequest, finalResult)
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
			test_approval_head, test_approval_request_digest, test_approval_result_digest,
			final_approval_head, final_approval_request_digest, final_approval_result_digest,
			round_limit_test_validity, round_limit_final_implementation,
			rounds_test_validity, rounds_final_implementation,
			total_rounds_test_validity, total_rounds_final_implementation
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23, $24, $25,
			$26, $27
		)
	`, run.ID, run.Issue.String(), run.Version, run.Phase,
		run.Input.ContextManifest.Schema, run.Input.ContextManifest.Digest,
		run.Input.ExecutionPolicy.Schema, run.Input.ExecutionPolicy.Digest,
		run.EscalationPolicy.Schema, run.EscalationPolicy.Digest,
		nullablePullRequest(run.PullRequest),
		run.FixedHead, run.PublishedHead, run.PublishedTestHead, run.ChecksHead,
		nullableApprovalHead(run.TestApproval), nullableApprovalRequestDigest(run.TestApproval),
		nullableApprovalResultDigest(run.TestApproval),
		nullableApprovalHead(run.FinalApproval), nullableApprovalRequestDigest(run.FinalApproval),
		nullableApprovalResultDigest(run.FinalApproval),
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

func insertObservation(
	ctx context.Context,
	tx pgx.Tx,
	runID string,
	version int64,
	ref contract.IssueObservationRef,
	bodyDigest contract.Digest,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO run_issue_observations (run_id, run_version, schema, digest, body_digest)
		VALUES ($1, $2, $3, $4, $5)
	`, runID, version, ref.Schema, ref.Digest, bodyDigest)
	return err
}

func insertReviewBinding(
	ctx context.Context,
	tx pgx.Tx,
	runID string,
	version int64,
	round int,
	review workflow.ReviewCompleted,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO run_review_bindings (
			run_id, run_version, review_round, review_kind, head,
			request_digest, result_digest, verdict
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, runID, version, round, review.Kind, review.Head,
		review.RequestDigest, review.ResultDigest, review.Verdict)
	return err
}

// consumesReviewRound は round 予算を消費しうる event かを返す。
//
// reviewer の verdict 確定に加えて、implement lane 発の test 差し戻しも消費する。
// 後者は verdict ではないが、test gate を再び開いて無人 loop を継続させるため、
// 予算が縛る対象は同じである（ADR-0003 D2 の 2026-08-21 追記）。
func consumesReviewRound(kind workflow.EventKind) bool {
	return kind == workflow.KindReviewCompleted || kind == workflow.KindTestRevisionRequired
}

func validateCreate(run workflow.Run, observation contract.IssueObservationRef) error {
	if run.Version != 0 {
		return invalidRun("Create 前の Version は 0 でなければならない: %d", run.Version)
	}
	if run.Phase != workflow.PhaseClaimed {
		return invalidRun("Create できる phase は claimed だけである: %q", run.Phase)
	}
	if run.Rounds != (workflow.ReviewRounds{}) || run.TotalRounds != (workflow.ReviewRounds{}) {
		return invalidRun("Create 時の review round counter は 0 でなければならない")
	}
	if err := validateRun(run); err != nil {
		return err
	}
	if err := validateVersionedRef(
		"Issue Observation", observation.Schema, observation.Digest, observation.Valid(),
	); err != nil {
		return err
	}
	if run.Observation != observation {
		return invalidRun("初期 IssueObservationRef が Run の Observation と一致しない")
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
	if transition.Event == workflow.KindClaimSucceeded {
		return invalidRun("claim_succeeded は Create の初期履歴にだけ使える")
	}
	if (transition.Event == workflow.KindReviewCompleted) != (transition.Review != nil) {
		return invalidRun("review_completed event と Review binding の有無が一致しない")
	}
	if transition.Review != nil {
		if err := validateReview(*transition.Review); err != nil {
			return err
		}
	}
	return validateRun(transition.Run)
}

func validateReview(review workflow.ReviewCompleted) error {
	if !knownReviewKind(review.Kind) {
		return invalidRun("review kind が不正: %q", review.Kind)
	}
	if !knownReviewVerdict(review.Verdict) {
		return invalidRun("review verdict が不正: %q", review.Verdict)
	}
	if review.Head == "" {
		return invalidRun("review head が空")
	}
	if !review.RequestDigest.Valid() {
		return invalidRun("Review Request digest が不正: %q", review.RequestDigest)
	}
	if !review.ResultDigest.Valid() {
		return invalidRun("Review Result digest が不正: %q", review.ResultDigest)
	}
	return nil
}

// validateRoundConsumption は round 予算を消費する transition が、対象 gate の counter
// だけをちょうど 1 進めていることを検証する。escalation で停止する場合は無人区間
// counter が reset されていることも要求する。
//
// reviewer の verdict と implement lane 発の test 差し戻しは、どちらも同じ予算を
// 消費する（ADR-0003 D2）。両者で検証の強さを変えると、緩い側の経路から counter を
// 好きに動かせてしまい、予算が縛る「無人区間の有限性」が成立しなくなる。
func validateRoundConsumption(current, next workflow.Run, gate contract.ReviewKind) (int, error) {
	var round int
	switch gate {
	case contract.ReviewTestValidity:
		if next.TotalRounds.TestValidity != current.TotalRounds.TestValidity+1 ||
			next.TotalRounds.FinalImplementation != current.TotalRounds.FinalImplementation {
			return 0, invalidRun("test validity の生涯 counter が1だけ進んでいない")
		}
		round = next.TotalRounds.TestValidity
	case contract.ReviewFinalImplementation:
		if next.TotalRounds.FinalImplementation != current.TotalRounds.FinalImplementation+1 ||
			next.TotalRounds.TestValidity != current.TotalRounds.TestValidity {
			return 0, invalidRun("final implementation の生涯 counter が1だけ進んでいない")
		}
		round = next.TotalRounds.FinalImplementation
	default:
		return 0, invalidRun("round を消費する gate が不正: %q", gate)
	}

	if next.Phase == workflow.PhaseNeedsHuman {
		if next.Rounds != (workflow.ReviewRounds{}) {
			return 0, invalidRun("needs_human へ進んだ round は無人区間 counter を reset しなければならない")
		}
		return round, nil
	}
	if gate == contract.ReviewTestValidity {
		if next.Rounds.TestValidity != current.Rounds.TestValidity+1 ||
			next.Rounds.FinalImplementation != current.Rounds.FinalImplementation {
			return 0, invalidRun("test validity の無人区間 counter が1だけ進んでいない")
		}
		return round, nil
	}
	if next.Rounds.FinalImplementation != current.Rounds.FinalImplementation+1 ||
		next.Rounds.TestValidity != current.Rounds.TestValidity {
		return 0, invalidRun("final implementation の無人区間 counter が1だけ進んでいない")
	}
	return round, nil
}

func validateReviewProgress(
	current workflow.Run,
	next workflow.Run,
	review workflow.ReviewCompleted,
) (int, error) {
	if review.Head != next.PublishedHead {
		return 0, invalidRun(
			"Review binding head が publish 済み head と一致しない: got %s, want %s",
			review.Head, next.PublishedHead,
		)
	}

	round, err := validateRoundConsumption(current, next, review.Kind)
	if err != nil {
		return 0, err
	}

	if review.Verdict != contract.VerdictApprove {
		return round, nil
	}
	approval := next.TestApproval
	if review.Kind == contract.ReviewFinalImplementation {
		approval = next.FinalApproval
	}
	if approval == nil || approval.Head != review.Head ||
		approval.RequestDigest != review.RequestDigest || approval.ResultDigest != review.ResultDigest {
		return 0, invalidRun("approve の Run binding が Review Result と一致しない")
	}
	return round, nil
}

func validateStoredReviewRecord(record ReviewRecord) error {
	if record.RunVersion < 2 || record.Round < 1 || !knownReviewKind(record.Kind) ||
		record.Head == "" || !record.RequestDigest.Valid() || !record.ResultDigest.Valid() ||
		!knownReviewVerdict(record.Verdict) {
		return corruptRun("不正な Review binding: %#v", record)
	}
	return nil
}

func validateRun(run workflow.Run) error {
	if run.ID == "" {
		return invalidRun("Run ID が空")
	}
	if _, err := parseIssueRef(run.Issue.String()); err != nil {
		return invalidRun("Issue reference が不正: %v", err)
	}
	if !knownPhase(run.Phase) {
		return invalidRun("phase が不正: %q", run.Phase)
	}
	if err := validateVersionedRef(
		"Context Manifest",
		run.Input.ContextManifest.Schema,
		run.Input.ContextManifest.Digest,
		run.Input.ContextManifest.Valid(),
	); err != nil {
		return err
	}
	if err := validateVersionedRef(
		"Execution Policy",
		run.Input.ExecutionPolicy.Schema,
		run.Input.ExecutionPolicy.Digest,
		run.Input.ExecutionPolicy.Valid(),
	); err != nil {
		return err
	}
	if err := validateVersionedRef(
		"Escalation Policy",
		run.EscalationPolicy.Schema,
		run.EscalationPolicy.Digest,
		run.EscalationPolicy.Valid(),
	); err != nil {
		return err
	}
	if err := validateRoundLimits(run.RoundLimits); err != nil {
		return err
	}
	if err := validateRoundCounters(run.Rounds, run.TotalRounds); err != nil {
		return err
	}
	if !run.Observation.Valid() {
		return invalidRun("IssueObservationRef が不正: schema=%q digest=%q",
			run.Observation.Schema, run.Observation.Digest)
	}
	if !run.ObservationBodyDigest.Valid() {
		return invalidRun("Issue 本文 digest が不正: %q", run.ObservationBodyDigest)
	}
	// structured claim context の durable schema は同じ Milestone 2 の別 slice である。
	// 保存できない値を黙って捨てると、復元した Run だけ checkpoint を失う。
	if run.ClaimContext != (contract.ClaimContext{}) {
		return invalidRun("structured claim context を保存する schema がまだ無い")
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
	if err := validateRun(run); err != nil {
		return fmt.Errorf("%w: %v", ErrCorruptRun, err)
	}
	return nil
}

func validateVersionedRef(name, schema string, digest contract.Digest, valid bool) error {
	if !valid {
		return invalidRun("%s ref が不正: schema=%q digest=%q", name, schema, digest)
	}
	return nil
}

func validateStoredVersionedRef(name, schema string, digest contract.Digest, valid bool) error {
	if !valid {
		return corruptRun("%s ref が不正: schema=%q digest=%q", name, schema, digest)
	}
	return nil
}

func validateRoundLimits(limits contract.ReviewRoundLimits) error {
	// round 上限は claim の時点で解決済みでなければならない。0 を許すと最初の
	// request_changes で必ず escalate する Run が gate を持つ顔で保存される。
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"testValidity", limits.TestValidity},
		{"finalImplementation", limits.FinalImplementation},
	} {
		if limit.value < contract.MinReviewRounds || limit.value > contract.MaxReviewRounds {
			return invalidRun("review round 上限 %s は %d 以上 %d 以下でなければならない: %d",
				limit.name, contract.MinReviewRounds, contract.MaxReviewRounds, limit.value)
		}
	}
	return nil
}

func validateRoundCounters(rounds, total workflow.ReviewRounds) error {
	for _, counter := range []struct {
		name  string
		value int
		total int
	}{
		{"testValidity", rounds.TestValidity, total.TestValidity},
		{"finalImplementation", rounds.FinalImplementation, total.FinalImplementation},
	} {
		if counter.value < 0 || counter.total < 0 {
			return invalidRun("review round counter %s は負にできない: rounds=%d total=%d",
				counter.name, counter.value, counter.total)
		}
		if counter.value > counter.total {
			return invalidRun("review round counter %s は生涯 counter 以下でなければならない: rounds=%d total=%d",
				counter.name, counter.value, counter.total)
		}
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
	if !approval.ResultDigest.Valid() {
		return invalidRun("%s approval result digest が不正: %q", name, approval.ResultDigest)
	}
	return nil
}

func scanApproval(name string, head, request, result sql.NullString) (*workflow.Approval, error) {
	if head.Valid != request.Valid || head.Valid != result.Valid {
		return nil, corruptRun("%s approval binding の NULL が不整合", name)
	}
	if !head.Valid {
		return nil, nil
	}
	requestDigest := contract.Digest(request.String)
	resultDigest := contract.Digest(result.String)
	if head.String == "" || !requestDigest.Valid() || !resultDigest.Valid() {
		return nil, corruptRun("%s approval binding が不正", name)
	}
	return &workflow.Approval{
		Head:          head.String,
		RequestDigest: requestDigest,
		ResultDigest:  resultDigest,
	}, nil
}

func nullablePullRequest(ref contract.PullRequestRef) any {
	if ref == (contract.PullRequestRef{}) {
		return nil
	}
	return ref.String()
}

func nullableApprovalHead(approval *workflow.Approval) any {
	if approval == nil {
		return nil
	}
	return approval.Head
}

func nullableApprovalRequestDigest(approval *workflow.Approval) any {
	if approval == nil {
		return nil
	}
	return approval.RequestDigest
}

func nullableApprovalResultDigest(approval *workflow.Approval) any {
	if approval == nil {
		return nil
	}
	return approval.ResultDigest
}

func parseIssueRef(raw string) (contract.IssueRef, error) {
	ref, ok := contract.ParseIssueRef(raw)
	if !ok || ref.String() != raw {
		return contract.IssueRef{}, fmt.Errorf("canonical Issue reference でない: %q", raw)
	}
	return ref, nil
}

func parsePullRequestRef(raw string) (contract.PullRequestRef, error) {
	ref, ok := contract.ParsePullRequestRef(raw)
	if !ok || ref.String() != raw {
		return contract.PullRequestRef{}, fmt.Errorf("canonical Pull Request reference でない: %q", raw)
	}
	return ref, nil
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

func knownReviewKind(kind contract.ReviewKind) bool {
	return kind == contract.ReviewTestValidity || kind == contract.ReviewFinalImplementation
}

func knownReviewVerdict(verdict contract.ReviewVerdict) bool {
	return verdict == contract.VerdictApprove || verdict == contract.VerdictRequestChanges ||
		verdict == contract.VerdictNeedsHuman
}

func rollbackStoreTransaction(parent context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), storeTransactionCleanupTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func commitStoreTransaction(parent context.Context, tx pgx.Tx) error {
	if err := parent.Err(); err != nil {
		return err
	}
	// pgx の nested transaction は RELEASE SAVEPOINT が失敗しても closed になる。
	// request cancellation と同時に部分書き込みを外側 transaction へ残さないよう、
	// 全 statement 成功後の確定処理だけは短い独立 context で完了させる。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), storeTransactionCleanupTimeout)
	defer cancel()
	return tx.Commit(ctx)
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
