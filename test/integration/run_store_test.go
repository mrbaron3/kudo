//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	postgresadapter "github.com/mrbaron3/kudo/internal/adapter/postgres"
	"github.com/mrbaron3/kudo/internal/contract"
	"github.com/mrbaron3/kudo/internal/workflow"
)

var testSchemaSequence atomic.Uint64

func isolatedDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("KUDO_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("KUDO_TEST_DATABASE_URL を設定して実行する（例: docker compose run --build --rm integration）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("PostgreSQL へ接続できない: %v", err)
	}
	schema := fmt.Sprintf("kudo_test_%d_%d", os.Getpid(), testSchemaSequence.Add(1))
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("test schema を作成できない: %v", err)
	}

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("database URL を解釈できない: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("connection pool を作成できない: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = admin.Close(ctx)
		t.Fatalf("test schema へ接続できない: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})
	return pool
}

func migrateTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := isolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postgresadapter.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migration に失敗した: %v", err)
	}
	return pool
}

func TestMigrateUpIsVersionedAndIdempotent(t *testing.T) {
	pool := isolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := postgresadapter.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("1回目の migration に失敗した: %v", err)
	}
	if err := postgresadapter.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("2回目の migration が no-op にならない: %v", err)
	}
	if err := postgresadapter.ValidateSchemaVersion(ctx, pool); err != nil {
		t.Fatalf("schema version を検証できない: %v", err)
	}

	var version int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(max(version_id), 0)
		FROM goose_db_version
		WHERE is_applied
	`).Scan(&version); err != nil {
		t.Fatalf("migration 履歴を取得できない: %v", err)
	}
	if version != postgresadapter.CurrentSchemaVersion {
		t.Fatalf("適用済み version = %d, want %d", version, postgresadapter.CurrentSchemaVersion)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied)
		VALUES ($1, true)
	`, postgresadapter.CurrentSchemaVersion+1); err != nil {
		t.Fatalf("future schema version の fixture を作成できない: %v", err)
	}
	if err := postgresadapter.ValidateSchemaVersion(ctx, pool); !errors.Is(err, postgresadapter.ErrSchemaVersionMismatch) {
		t.Fatalf("future schema version の検証 error = %v, want ErrSchemaVersionMismatch", err)
	}
}

func TestValidateSchemaVersionUsesTheSameSearchPathResolutionAsItsRead(t *testing.T) {
	pool := migrateTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var migratedSchema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&migratedSchema); err != nil {
		t.Fatalf("migration schema を取得できない: %v", err)
	}
	emptySchema := fmt.Sprintf("kudo_empty_%d_%d", os.Getpid(), testSchemaSequence.Add(1))
	emptyIdentifier := pgx.Identifier{emptySchema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+emptyIdentifier); err != nil {
		t.Fatalf("先頭の空 schema を作成できない: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DROP SCHEMA "+emptyIdentifier+" CASCADE")
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("search_path 検証 transaction を開始できない: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	searchPath := emptyIdentifier + ", " + pgx.Identifier{migratedSchema}.Sanitize()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+searchPath); err != nil {
		t.Fatalf("search_path を設定できない: %v", err)
	}
	if err := postgresadapter.ValidateSchemaVersion(ctx, tx); err != nil {
		t.Fatalf("search_path の後続 schema にある履歴 table を検証できない: %v", err)
	}
}

func TestRunStoreRestoresOpaqueBindingsAndObservationLineage(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initialObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("observation-1")),
	}
	run := claimedRun("run-round-trip", 1301, initialObservation)
	// Store は schema の中身を解釈しない。将来 version も schema/digest の組として扱う。
	run.Input.ContextManifest.Schema = "kudo.context-manifest/v1alpha2"
	run.Input.ExecutionPolicy.Schema = "kudo.execution-policy/v2"

	created, err := store.Create(ctx, run, initialObservation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("作成後 version = %d, want 1", created.Version)
	}

	testHead := "1111111111111111111111111111111111111111"
	implementationHead := "2222222222222222222222222222222222222222"
	pullRequest := contract.PullRequestRef{Owner: "mrbaron3", Repository: "kudo", Number: 77}
	stored := persistEvent(t, ctx, store, created, workflow.OperationStarted{Kind: contract.OperationAuthorTests}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.TestsAuthored{Head: testHead}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.HeadPublished{Head: testHead, PullRequest: pullRequest}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictApprove,
		Head:          testHead,
		RequestDigest: contract.SHA256([]byte("test-review-request")),
		ResultDigest:  contract.SHA256([]byte("test-review-result")),
	}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.ImplementationFixed{
		Head:         implementationHead,
		ChecksPassed: true,
	}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.HeadPublished{
		Head:        implementationHead,
		PullRequest: pullRequest,
	}, nil)
	stored = persistEvent(t, ctx, store, stored, workflow.ReviewCompleted{
		Kind:          contract.ReviewFinalImplementation,
		Verdict:       contract.VerdictApprove,
		Head:          implementationHead,
		RequestDigest: contract.SHA256([]byte("final-review-request")),
		ResultDigest:  contract.SHA256([]byte("final-review-result")),
	}, nil)

	secondObservation := contract.IssueObservationRef{
		Schema: "kudo.issue-observation/v1alpha2",
		Digest: contract.SHA256([]byte("observation-2")),
	}
	secondBody := contract.SHA256([]byte("body-2"))
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: secondObservation,
		BodyDigest:  secondBody,
	}, &secondObservation)

	// digest が同じでも schema が変われば別の opaque ref であり、lineage に残す。
	thirdObservation := contract.IssueObservationRef{
		Schema: "kudo.issue-observation/v1alpha3",
		Digest: secondObservation.Digest,
	}
	thirdBody := contract.SHA256([]byte("body-3"))
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: thirdObservation,
		BodyDigest:  thirdBody,
	}, &thirdObservation)
	// 同一refかつ同一本文の再観測はRun履歴には残るが、Observation lineageを重複させない。
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: thirdObservation,
		BodyDigest:  thirdBody,
	}, &thirdObservation)

	// process-localなStoreと接続を捨てても、PostgreSQLだけから全bindingを復元できる。
	pool.Reset()
	store = postgresadapter.NewRunStore(pool)

	loaded, err := store.Load(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Run を再読込できない: %v", err)
	}
	if !reflect.DeepEqual(loaded, stored) {
		t.Fatalf("再読込した Run が一致しない:\n got: %#v\nwant: %#v", loaded, stored)
	}
	lineage, err := store.ObservationLineage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Issue Observation lineage を取得できない: %v", err)
	}
	wantLineage := []postgresadapter.ObservationRecord{
		{RunVersion: 1, Ref: initialObservation, BodyDigest: contract.SHA256([]byte("run-round-trip-body"))},
		{RunVersion: 9, Ref: secondObservation, BodyDigest: secondBody},
		{RunVersion: 10, Ref: thirdObservation, BodyDigest: thirdBody},
	}
	if !reflect.DeepEqual(lineage, wantLineage) {
		t.Fatalf("Observation lineage = %#v, want %#v", lineage, wantLineage)
	}
	reviewLineage, err := store.ReviewLineage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Review binding lineage を取得できない: %v", err)
	}
	wantReviewLineage := []postgresadapter.ReviewRecord{
		{
			RunVersion:    5,
			Round:         1,
			Kind:          contract.ReviewTestValidity,
			Head:          testHead,
			RequestDigest: contract.SHA256([]byte("test-review-request")),
			ResultDigest:  contract.SHA256([]byte("test-review-result")),
			Verdict:       contract.VerdictApprove,
		},
		{
			RunVersion:    8,
			Round:         1,
			Kind:          contract.ReviewFinalImplementation,
			Head:          implementationHead,
			RequestDigest: contract.SHA256([]byte("final-review-request")),
			ResultDigest:  contract.SHA256([]byte("final-review-result")),
			Verdict:       contract.VerdictApprove,
		},
	}
	if !reflect.DeepEqual(reviewLineage, wantReviewLineage) {
		t.Fatalf("Review binding lineage = %#v, want %#v", reviewLineage, wantReviewLineage)
	}

	history, err := store.TransitionHistory(ctx, stored.ID)
	if err != nil {
		t.Fatalf("transition history を取得できない: %v", err)
	}
	if got, want := len(history), 11; got != want {
		t.Fatalf("transition history length = %d, want %d", got, want)
	}
	if history[0].From != workflow.PhaseNew || history[0].To != workflow.PhaseClaimed {
		t.Fatalf("initial transition = %#v", history[0])
	}

	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: stored.Version,
		Event:           workflow.KindAttemptFailed,
		Run:             stored,
		Observation:     &thirdObservation,
	})
	if !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("Observation event 以外の lineage 追記 error = %v, want ErrInvalidRun", err)
	}

	changedInput := stored
	changedInput.Input.ContextManifest = contract.ContextManifestRef{
		Schema: "kudo.context-manifest/v3",
		Digest: contract.SHA256([]byte("new-context")),
	}
	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: stored.Version,
		Event:           workflow.KindSemanticInputChanged,
		Run:             changedInput,
	})
	if !errors.Is(err, postgresadapter.ErrImmutableRunInput) {
		t.Fatalf("active Run の ref 書き換え error = %v, want ErrImmutableRunInput", err)
	}
}

func TestRunStoreStoresAnAbsentPullRequestAsNull(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("absent-pull-request")),
	}
	created, err := store.Create(ctx, claimedRun("run-absent-pull-request", 1306, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	var absent bool
	if err := pool.QueryRow(ctx,
		"SELECT pull_request_ref IS NULL FROM runs WHERE id = $1", created.ID,
	).Scan(&absent); err != nil {
		t.Fatalf("Pull Request binding を確認できない: %v", err)
	}
	if !absent {
		t.Fatal("未 binding の Pull Request が NULL で保存されていない")
	}
}

func TestRunStoreRejectsCrossKindArtifactReferences(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tests := []struct {
		name   string
		mutate func(*workflow.Run, *contract.IssueObservationRef)
	}{
		{
			name: "Issue Observation",
			mutate: func(_ *workflow.Run, observation *contract.IssueObservationRef) {
				observation.Schema = contract.ContextManifestSchemaV1Alpha1
			},
		},
		{
			name: "Context Manifest",
			mutate: func(run *workflow.Run, _ *contract.IssueObservationRef) {
				run.Input.ContextManifest.Schema = contract.ExecutionPolicySchemaV1Alpha1
			},
		},
		{
			name: "Execution Policy",
			mutate: func(run *workflow.Run, _ *contract.IssueObservationRef) {
				run.Input.ExecutionPolicy.Schema = contract.EscalationPolicySchemaV1Alpha1
			},
		},
		{
			name: "Escalation Policy",
			mutate: func(run *workflow.Run, _ *contract.IssueObservationRef) {
				run.EscalationPolicy.Schema = contract.IssueObservationSchemaV1Alpha1
			},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := contract.IssueObservationRef{
				Schema: contract.IssueObservationSchemaV1Alpha1,
				Digest: contract.SHA256([]byte(fmt.Sprintf("cross-kind-%d", i))),
			}
			run := claimedRun(fmt.Sprintf("run-cross-kind-%d", i), 1310+i, observation)
			test.mutate(&run, &observation)

			if _, err := store.Create(ctx, run, observation); !errors.Is(err, postgresadapter.ErrInvalidRun) {
				t.Fatalf("別schema familyの%s refのerror = %v, want ErrInvalidRun", test.name, err)
			}
		})
	}
}

func TestRunStoreRejectsGitHubReferencesOutsideTheContractGrammar(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i, issue := range []contract.IssueRef{
		{Owner: "bad_owner", Repository: "kudo", Number: 1},
		{Owner: "-owner", Repository: "kudo", Number: 1},
		{Owner: "owner:8080", Repository: "kudo", Number: 1},
		{Owner: "owner", Repository: ".", Number: 1},
	} {
		observation := contract.IssueObservationRef{
			Schema: contract.IssueObservationSchemaV1Alpha1,
			Digest: contract.SHA256([]byte(fmt.Sprintf("invalid-issue-ref-%d", i))),
		}
		run := claimedRun(fmt.Sprintf("run-invalid-issue-ref-%d", i), 1340+i, observation)
		run.Issue = issue
		if _, err := store.Create(ctx, run, observation); !errors.Is(err, postgresadapter.ErrInvalidRun) {
			t.Fatalf("Issue reference %q の error = %v, want ErrInvalidRun", issue.String(), err)
		}
	}

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("invalid-pull-request-ref")),
	}
	created, err := store.Create(ctx, claimedRun("run-invalid-pull-request-ref", 1344, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}
	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.PullRequest = contract.PullRequestRef{Owner: "bad_owner", Repository: "kudo", Number: 1}
	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindOperationStarted,
		Run:             next,
	})
	if !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("Pull Request reference %q の error = %v, want ErrInvalidRun", next.PullRequest.String(), err)
	}
}

func TestRunStoreRejectsCrossKindObservationTransition(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initial := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("initial-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-cross-kind-transition", 1320, initial), initial)
	if err != nil {
		t.Fatalf("Runを作成できない: %v", err)
	}

	next := created
	badObservation := contract.IssueObservationRef{
		Schema: contract.ContextManifestSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("cross-kind-observation")),
	}
	next.Observation = badObservation
	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindObservationRecorded,
		Run:             next,
		Observation:     &badObservation,
	})
	if !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("別schema familyのObservation transition error = %v, want ErrInvalidRun", err)
	}
}

func TestRunStoreRejectsCorruptStoredSchemaFamily(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("corrupt-schema-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-corrupt-schema", 1321, observation), observation)
	if err != nil {
		t.Fatalf("Runを作成できない: %v", err)
	}

	badDigest := contract.SHA256([]byte("cross-kind-escalation-policy"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO artifact_refs (schema, digest)
		VALUES ($1, $2)
	`, contract.ContextManifestSchemaV1Alpha1, badDigest); err != nil {
		t.Fatalf("不正保存値のartifact refを準備できない: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"ALTER TABLE runs DISABLE TRIGGER runs_reject_gate_budget_update",
	); err != nil {
		t.Fatalf("corruption fixture 用に gate-budget trigger を無効化できない: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE runs
		SET escalation_policy_schema = $2, escalation_policy_digest = $3
		WHERE id = $1
	`, created.ID, contract.ContextManifestSchemaV1Alpha1, badDigest); err != nil {
		t.Fatalf("不正保存値を準備できない: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"ALTER TABLE runs ENABLE TRIGGER runs_reject_gate_budget_update",
	); err != nil {
		t.Fatalf("gate-budget trigger を再有効化できない: %v", err)
	}

	if _, err := store.Load(ctx, created.ID); !errors.Is(err, postgresadapter.ErrCorruptRun) {
		t.Fatalf("別schema familyを持つ保存Runのerror = %v, want ErrCorruptRun", err)
	}

	observation = contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("corrupt-observation-schema-initial")),
	}
	created, err = store.Create(ctx, claimedRun("run-corrupt-observation-schema", 1322, observation), observation)
	if err != nil {
		t.Fatalf("Runを作成できない: %v", err)
	}
	badDigest = contract.SHA256([]byte("cross-kind-observation"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO artifact_refs (schema, digest)
		VALUES ($1, $2)
	`, contract.ContextManifestSchemaV1Alpha1, badDigest); err != nil {
		t.Fatalf("不正な Issue Observation ref を準備できない: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_transitions (run_id, version, event_kind, from_phase, to_phase)
		VALUES ($1, 2, $2, $3, $3)
	`, created.ID, workflow.KindObservationRecorded, workflow.PhaseClaimed); err != nil {
		t.Fatalf("不正な Issue Observation の transition を準備できない: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_issue_observations (run_id, run_version, schema, digest, body_digest)
		VALUES ($1, 2, $2, $3, $4)
	`, created.ID, contract.ContextManifestSchemaV1Alpha1, badDigest,
		contract.SHA256([]byte("corrupt-observation-body"))); err != nil {
		t.Fatalf("不正な Issue Observation lineage を準備できない: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE runs SET version = 2 WHERE id = $1", created.ID); err != nil {
		t.Fatalf("不正な保存 Run を準備できない: %v", err)
	}

	if _, err := store.Load(ctx, created.ID); !errors.Is(err, postgresadapter.ErrCorruptRun) {
		t.Fatalf("別 schema family の Observation を持つ保存 Run の error = %v, want ErrCorruptRun", err)
	}
	if _, err := store.ObservationLineage(ctx, created.ID); !errors.Is(err, postgresadapter.ErrCorruptRun) {
		t.Fatalf("別 schema family を持つ Observation lineage の error = %v, want ErrCorruptRun", err)
	}
}

func TestRunStoreRejectsPinnedReviewBudgetChanges(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tests := []struct {
		name   string
		mutate func(*workflow.Run)
	}{
		{
			name: "Escalation Policy",
			mutate: func(run *workflow.Run) {
				run.EscalationPolicy.Digest = contract.SHA256([]byte("other-escalation-policy"))
			},
		},
		{
			name: "RoundLimits",
			mutate: func(run *workflow.Run) {
				run.RoundLimits.TestValidity++
			},
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := contract.IssueObservationRef{
				Schema: contract.IssueObservationSchemaV1Alpha1,
				Digest: contract.SHA256([]byte(fmt.Sprintf("pinned-budget-observation-%d", i))),
			}
			created, err := store.Create(ctx,
				claimedRun(fmt.Sprintf("run-pinned-budget-%d", i), 1322+i, observation),
				observation,
			)
			if err != nil {
				t.Fatalf("Runを作成できない: %v", err)
			}

			next := created
			next.Phase = workflow.PhaseAuthoringTests
			test.mutate(&next)
			if _, err = store.Transition(ctx, postgresadapter.Transition{
				ExpectedVersion: created.Version,
				Event:           workflow.KindOperationStarted,
				Run:             next,
			}); !errors.Is(err, postgresadapter.ErrInvalidRun) {
				t.Fatalf("固定済み%sの変更 error = %v, want ErrInvalidRun", test.name, err)
			}
		})
	}
}

func TestRunStoreRejectsTotalRoundRollback(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("total-round-rollback-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-total-round-rollback", 1323, observation), observation)
	if err != nil {
		t.Fatalf("Runを作成できない: %v", err)
	}

	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.PublishedHead = "1111111111111111111111111111111111111111"
	next.Rounds.TestValidity = 1
	next.TotalRounds.TestValidity = 1
	review := workflow.ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictRequestChanges,
		Head:          next.PublishedHead,
		RequestDigest: contract.SHA256([]byte("rollback-request")),
		ResultDigest:  contract.SHA256([]byte("rollback-result")),
	}
	stored, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             next,
		Review:          &review,
	})
	if err != nil {
		t.Fatalf("生涯round counterを進められない: %v", err)
	}

	rolledBack := stored
	rolledBack.TotalRounds.TestValidity = 0
	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: stored.Version,
		Event:           workflow.KindAttemptFailed,
		Run:             rolledBack,
	})
	if !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("生涯round counter巻き戻し error = %v, want ErrInvalidRun", err)
	}
}

// TestRunStoreRestoresReviewRoundBudget は round counter が process をまたいで残ることを検証する。
//
// 上限が縛るのは「人間が次に見るまでに何 round 回すか」であり、counter が揮発すると
// crash や lease 失効のたびに予算が満額へ戻って無人区間が実質無制限になる。
// pin 済みの Escalation Policy と上限も、同じ Run が同じ予算で再開するために復元する。
func TestRunStoreRestoresReviewRoundBudget(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("rounds-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-rounds", 1303, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	next := created
	for round := 1; round <= 2; round++ {
		next.Phase = workflow.PhaseAuthoringTests
		next.Rounds.TestValidity++
		next.TotalRounds.TestValidity++
		review := workflow.ReviewCompleted{
			Kind:          contract.ReviewTestValidity,
			Verdict:       contract.VerdictRequestChanges,
			Head:          fmt.Sprintf("%040d", round),
			RequestDigest: contract.SHA256([]byte(fmt.Sprintf("test-request-%d", round))),
			ResultDigest:  contract.SHA256([]byte(fmt.Sprintf("test-result-%d", round))),
		}
		next.PublishedHead = review.Head
		next.PublishedTestHead = review.Head
		stored, transitionErr := store.Transition(ctx, postgresadapter.Transition{
			ExpectedVersion: next.Version,
			Event:           workflow.KindReviewCompleted,
			Run:             next,
			Review:          &review,
		})
		if transitionErr != nil {
			t.Fatalf("test review round %d を保存できない: %v", round, transitionErr)
		}
		next = stored
	}
	next.Rounds.FinalImplementation++
	next.TotalRounds.FinalImplementation++
	finalReview := workflow.ReviewCompleted{
		Kind:          contract.ReviewFinalImplementation,
		Verdict:       contract.VerdictRequestChanges,
		Head:          "3333333333333333333333333333333333333333",
		RequestDigest: contract.SHA256([]byte("final-request")),
		ResultDigest:  contract.SHA256([]byte("final-result")),
	}
	next.PublishedHead = finalReview.Head
	stored, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: next.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             next,
		Review:          &finalReview,
	})
	if err != nil {
		t.Fatalf("final review round を保存できない: %v", err)
	}
	next = stored

	// 別 store instance から読み直し、process-local memory ではなく DB から復元されることを示す。
	restored, err := postgresadapter.NewRunStore(pool).Load(ctx, created.ID)
	if err != nil {
		t.Fatalf("Run を復元できない: %v", err)
	}
	if restored.Rounds != next.Rounds {
		t.Errorf("Rounds = %+v, want %+v", restored.Rounds, next.Rounds)
	}
	if restored.TotalRounds != next.TotalRounds {
		t.Errorf("TotalRounds = %+v, want %+v", restored.TotalRounds, next.TotalRounds)
	}
	if restored.RoundLimits != created.RoundLimits {
		t.Errorf("RoundLimits = %+v, want %+v", restored.RoundLimits, created.RoundLimits)
	}
	if restored.EscalationPolicy != created.EscalationPolicy {
		t.Errorf("EscalationPolicy = %+v, want %+v", restored.EscalationPolicy, created.EscalationPolicy)
	}
}

// TestRunStoreRejectsRoundsAboveTotal は無人区間 counter が生涯 counter を超える入力を
// Store が SQL 実行前に拒むことを検証する。schema 側の同じ不変条件は guard test で固定する。
func TestRunStoreRejectsRoundsAboveTotal(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("rounds-invariant-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-rounds-invariant", 1304, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.Rounds = workflow.ReviewRounds{TestValidity: 3}
	next.TotalRounds = workflow.ReviewRounds{TestValidity: 1}
	_, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             next,
	})
	if !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("rounds > total_rounds の error = %v, want ErrInvalidRun", err)
	}
}

func TestRunStoreCompareAndSwap(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("cas-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-cas", 1302, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}
	next := created
	next.Phase = workflow.PhaseAuthoringTests

	start := make(chan struct{})
	errorsByWriter := make(chan error, 2)
	var writers sync.WaitGroup
	for range 2 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, transitionErr := store.Transition(ctx, postgresadapter.Transition{
				ExpectedVersion: created.Version,
				Event:           workflow.KindOperationStarted,
				Run:             next,
			})
			errorsByWriter <- transitionErr
		}()
	}
	close(start)
	writers.Wait()
	close(errorsByWriter)

	var succeeded, conflicted int
	for err := range errorsByWriter {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, postgresadapter.ErrVersionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected transition error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("CAS results = succeeded %d, conflicted %d, want 1/1", succeeded, conflicted)
	}
	loaded, err := store.Load(ctx, created.ID)
	if err != nil {
		t.Fatalf("CAS 後の Run を取得できない: %v", err)
	}
	if loaded.Version != 2 || loaded.Phase != workflow.PhaseAuthoringTests {
		t.Fatalf("CAS 後の Run = version %d, phase %s", loaded.Version, loaded.Phase)
	}
	history, err := store.TransitionHistory(ctx, created.ID)
	if err != nil {
		t.Fatalf("CAS 後の transition history を取得できない: %v", err)
	}
	if got, want := len(history), 2; got != want {
		t.Fatalf("CAS loser が history を追加した: length %d, want %d", got, want)
	}
}

func TestRunStoreRollsBackSavepointAfterRequestCancellation(t *testing.T) {
	pool := migrateTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("外側 transaction を開始できない: %v", err)
	}
	defer func() { _ = outer.Rollback(ctx) }()

	requestCtx, cancelRequest := context.WithCancel(ctx)
	store := postgresadapter.NewRunStore(cancelAfterObservationDB{
		Tx:     outer,
		cancel: cancelRequest,
	})
	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("cancelled-savepoint-observation")),
	}
	run := claimedRun("run-cancelled-savepoint", 1390, observation)
	_, err = store.Create(requestCtx, run, observation)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel 後の Create error = %v, want context.Canceled", err)
	}

	// Store が失敗を返した操作だけを rollback し、呼び出し側の transaction は継続できる。
	if err := outer.Commit(ctx); err != nil {
		t.Fatalf("Store 失敗後の外側 transaction を commit できない: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE id = $1", run.ID).Scan(&count); err != nil {
		t.Fatalf("cancel 後の Run 件数を取得できない: %v", err)
	}
	if count != 0 {
		t.Fatalf("失敗した Create の部分書き込み件数 = %d, want 0", count)
	}
}

func TestRunStoreAllowsOnlyOneWriterPerIssue(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("writer-1")),
	}
	if _, err := store.Create(ctx, claimedRun("run-writer-1", 1303, firstObservation), firstObservation); err != nil {
		t.Fatalf("最初の writer Run を作成できない: %v", err)
	}
	secondObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("writer-2")),
	}
	_, err := store.Create(ctx, claimedRun("run-writer-2", 1303, secondObservation), secondObservation)
	if !errors.Is(err, postgresadapter.ErrActiveRun) {
		t.Fatalf("同じ Issue の二つ目の writer error = %v, want ErrActiveRun", err)
	}

	otherIssueObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("other-issue")),
	}
	if _, err := store.Create(ctx, claimedRun("run-other-issue", 1304, otherIssueObservation), otherIssueObservation); err != nil {
		t.Fatalf("別 Issue の独立 Run が global lock で拒否された: %v", err)
	}
}

func TestRunStoreRequiresNeedsHumanRunToBeSupersededBeforeAReplacementClaim(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("needs-human-first")),
	}
	created, err := store.Create(ctx, claimedRun("run-needs-human-first", 1350, firstObservation), firstObservation)
	if err != nil {
		t.Fatalf("最初の Run を作成できない: %v", err)
	}
	paused := created
	paused.Phase = workflow.PhaseNeedsHuman
	paused, err = store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindHumanEscalated,
		Run:             paused,
	})
	if err != nil {
		t.Fatalf("Run を needs_human にできない: %v", err)
	}

	secondObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("needs-human-second")),
	}
	_, err = store.Create(ctx, claimedRun("run-needs-human-second", 1350, secondObservation), secondObservation)
	if !errors.Is(err, postgresadapter.ErrActiveRun) {
		t.Fatalf("needs_human Run が残る Issue の再 claim error = %v, want ErrActiveRun", err)
	}

	superseded := paused
	superseded.Phase = workflow.PhaseSuperseded
	if _, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: paused.Version,
		Event:           workflow.KindSemanticInputChanged,
		Run:             superseded,
	}); err != nil {
		t.Fatalf("paused Run を supersede できない: %v", err)
	}
	if _, err := store.Create(ctx,
		claimedRun("run-needs-human-replacement", 1350, secondObservation), secondObservation,
	); err != nil {
		t.Fatalf("supersede 後の置換 Run を claim できない: %v", err)
	}
}

func TestRunStoreDetectsCorruptTransitionHistory(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{
			name:   "missing version",
			mutate: "DELETE FROM run_transitions WHERE run_id = $1 AND version = 2",
		},
		{
			name:   "broken phase chain",
			mutate: "UPDATE run_transitions SET from_phase = 'implementing' WHERE run_id = $1 AND version = 2",
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := migrateTestDatabase(t)
			store := postgresadapter.NewRunStore(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			observation := contract.IssueObservationRef{
				Schema: contract.IssueObservationSchemaV1Alpha1,
				Digest: contract.SHA256([]byte(fmt.Sprintf("transition-corruption-%d", i))),
			}
			created, err := store.Create(ctx,
				claimedRun(fmt.Sprintf("run-transition-corruption-%d", i), 1360+i, observation),
				observation,
			)
			if err != nil {
				t.Fatalf("Run を作成できない: %v", err)
			}
			authoring := created
			authoring.Phase = workflow.PhaseAuthoringTests
			authoring, err = store.Transition(ctx, postgresadapter.Transition{
				ExpectedVersion: created.Version,
				Event:           workflow.KindOperationStarted,
				Run:             authoring,
			})
			if err != nil {
				t.Fatalf("version 2 を保存できない: %v", err)
			}
			publishing := authoring
			publishing.Phase = workflow.PhasePublishingTestHead
			publishing.FixedHead = "1111111111111111111111111111111111111111"
			if _, err := store.Transition(ctx, postgresadapter.Transition{
				ExpectedVersion: authoring.Version,
				Event:           workflow.KindTestsAuthored,
				Run:             publishing,
			}); err != nil {
				t.Fatalf("version 3 を保存できない: %v", err)
			}

			if _, err := pool.Exec(ctx,
				"ALTER TABLE run_transitions DISABLE TRIGGER run_transitions_append_only",
			); err != nil {
				t.Fatalf("corruption fixture 用に append-only trigger を無効化できない: %v", err)
			}
			if _, err := pool.Exec(ctx, test.mutate, created.ID); err != nil {
				t.Fatalf("corruption fixture を作成できない: %v", err)
			}
			if _, err := pool.Exec(ctx,
				"ALTER TABLE run_transitions ENABLE TRIGGER run_transitions_append_only",
			); err != nil {
				t.Fatalf("append-only trigger を再有効化できない: %v", err)
			}

			if _, err := store.TransitionHistory(ctx, created.ID); !errors.Is(err, postgresadapter.ErrCorruptRun) {
				t.Fatalf("壊れた transition history の error = %v, want ErrCorruptRun", err)
			}
		})
	}
}

func TestRunTransitionSchemaRejectsAmbiguousMissingFromPhase(t *testing.T) {
	for name, test := range map[string]struct {
		from any
		want string
	}{
		"empty":            {from: "", want: "run_transitions_from_phase_valid"},
		"null after claim": {from: nil, want: "run_transitions_initial_shape"},
	} {
		t.Run(name, func(t *testing.T) {
			pool := migrateTestDatabase(t)
			store := postgresadapter.NewRunStore(pool)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			observation := contract.IssueObservationRef{
				Schema: contract.IssueObservationSchemaV1Alpha1,
				Digest: contract.SHA256([]byte("ambiguous-from-phase-" + name)),
			}
			created, err := store.Create(ctx,
				claimedRun("run-ambiguous-from-phase-"+name, 1370, observation), observation,
			)
			if err != nil {
				t.Fatalf("Run を作成できない: %v", err)
			}
			_, err = pool.Exec(ctx, `
				INSERT INTO run_transitions (run_id, version, event_kind, from_phase, to_phase)
				VALUES ($1, 2, $2, $3, $4)
			`, created.ID, workflow.KindAttemptFailed, test.from, workflow.PhaseClaimed)
			assertConstraintViolation(t, err, test.want)
		})
	}
}

// TestRunStoreSchemaGuardsNameTheirConstraints は、schema 側の書き込み禁止 guard が
// error へ載せる constraint 名を固定する。trigger が RAISE EXCEPTION の CONSTRAINT で名乗る
// 名前は DDL object として実在せず、migration 適用時にも Go の compile 時にも検証されない。
// error 面の contract として test でだけ固定できる。
func TestRunStoreSchemaGuardsNameTheirConstraints(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("schema-guard")),
	}
	created, err := store.Create(ctx, claimedRun("run-schema-guard", 1305, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}
	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.PublishedHead = "1111111111111111111111111111111111111111"
	next.Rounds.TestValidity = 1
	next.TotalRounds.TestValidity = 1
	review := workflow.ReviewCompleted{
		Kind:          contract.ReviewTestValidity,
		Verdict:       contract.VerdictRequestChanges,
		Head:          next.PublishedHead,
		RequestDigest: contract.SHA256([]byte("schema-guard-request")),
		ResultDigest:  contract.SHA256([]byte("schema-guard-result")),
	}
	if _, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             next,
		Review:          &review,
	}); err != nil {
		t.Fatalf("Review binding fixture を保存できない: %v", err)
	}

	// Store の事前判定を迂回し、schema 自身の guard へ到達させる。
	_, err = pool.Exec(ctx, `
		UPDATE runs
		SET context_manifest_digest = $2
		WHERE id = $1
	`, created.ID, contract.SHA256([]byte("rewritten-context")))
	assertConstraintViolation(t, err, "runs_input_immutable")

	for name, test := range map[string]struct {
		query string
		args  []any
		want  string
	}{
		"Issue identity": {
			query: "UPDATE runs SET issue_ref = $2 WHERE id = $1",
			args:  []any{created.ID, "github://mrbaron3/kudo/issues/9999"},
			want:  "runs_identity_immutable",
		},
		"gate budget limit": {
			query: "UPDATE runs SET round_limit_test_validity = 4 WHERE id = $1",
			args:  []any{created.ID},
			want:  "runs_gate_budget_immutable",
		},
		"escalation policy": {
			query: "UPDATE runs SET escalation_policy_digest = $2 WHERE id = $1",
			args:  []any{created.ID, contract.SHA256([]byte("rewritten-escalation-policy"))},
			want:  "runs_gate_budget_immutable",
		},
		"empty Pull Request": {
			query: "UPDATE runs SET pull_request_ref = '' WHERE id = $1",
			args:  []any{created.ID},
			want:  "runs_pull_request_ref_non_empty",
		},
		"rounds within total": {
			query: "UPDATE runs SET rounds_test_validity = 2 WHERE id = $1",
			args:  []any{created.ID},
			want:  "runs_rounds_within_total",
		},
		"approval result pair": {
			query: "UPDATE runs SET test_approval_head = $2, test_approval_request_digest = $3 WHERE id = $1",
			args: []any{
				created.ID,
				"1111111111111111111111111111111111111111",
				contract.SHA256([]byte("missing-approval-result")),
			},
			want: "runs_test_approval_pair",
		},
		"transition update": {
			query: "UPDATE run_transitions SET event_kind = $2 WHERE run_id = $1 AND version = 1",
			args:  []any{created.ID, workflow.KindAttemptFailed},
			want:  "run_transitions_append_only",
		},
		"transition delete": {
			query: "DELETE FROM run_transitions WHERE run_id = $1 AND version = 1",
			args:  []any{created.ID},
			want:  "run_transitions_append_only",
		},
		"review binding update": {
			query: "UPDATE run_review_bindings SET verdict = 'needs_human' WHERE run_id = $1",
			args:  []any{created.ID},
			want:  "run_review_bindings_append_only",
		},
		"review binding delete": {
			query: "DELETE FROM run_review_bindings WHERE run_id = $1",
			args:  []any{created.ID},
			want:  "run_review_bindings_append_only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := execAndRollback(ctx, t, pool, test.query, test.args...)
			assertConstraintViolation(t, err, test.want)
		})
	}

	_, err = pool.Exec(ctx, "DELETE FROM run_issue_observations WHERE run_id = $1", created.ID)
	assertConstraintViolation(t, err, "run_issue_observations_append_only")

	if _, err := pool.Exec(ctx, `
		UPDATE runs
		SET total_rounds_test_validity = 2
		WHERE id = $1
	`, created.ID); err != nil {
		t.Fatalf("生涯round counterを進められない: %v", err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE runs
		SET total_rounds_test_validity = 1
		WHERE id = $1
	`, created.ID)
	assertConstraintViolation(t, err, "runs_total_rounds_monotonic")
}

func execAndRollback(
	ctx context.Context,
	t *testing.T,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) error {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("schema guard 検証 transaction を開始できない: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, query, args...)
	return err
}

func assertConstraintViolation(t *testing.T, err error, want string) {
	t.Helper()

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error = %v, want *pgconn.PgError %q", err, want)
	}
	if postgresError.ConstraintName != want {
		t.Fatalf("constraint 名 = %q, want %q", postgresError.ConstraintName, want)
	}
}

func claimedRun(id string, issueNumber int, observation contract.IssueObservationRef) workflow.Run {
	return workflow.Run{
		ID:    id,
		Issue: contract.IssueRef{Owner: "mrbaron3", Repository: "kudo", Number: issueNumber},
		Phase: workflow.PhaseClaimed,
		Input: workflow.InputIdentity{
			ContextManifest: contract.ContextManifestRef{
				Schema: contract.ContextManifestSchemaV1Alpha1,
				Digest: contract.SHA256([]byte(id + "-context")),
			},
			ExecutionPolicy: contract.ExecutionPolicyRef{
				Schema: contract.ExecutionPolicySchemaV1Alpha1,
				Digest: contract.SHA256([]byte(id + "-policy")),
			},
		},
		EscalationPolicy: contract.EscalationPolicyRef{
			Schema: contract.EscalationPolicySchemaV1Alpha1,
			Digest: contract.SHA256([]byte(id + "-escalation")),
		},
		RoundLimits:           contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3},
		Observation:           observation,
		ObservationBodyDigest: contract.SHA256([]byte(id + "-body")),
	}
}

func persistEvent(
	t *testing.T,
	ctx context.Context,
	store *postgresadapter.RunStore,
	run workflow.Run,
	event workflow.Event,
	observation *contract.IssueObservationRef,
) workflow.Run {
	t.Helper()

	decision, err := workflow.Decide(run, event)
	if err != nil {
		t.Fatalf("workflow transition %s を決定できない: %v", event.EventKind(), err)
	}
	var review *workflow.ReviewCompleted
	if completed, ok := event.(workflow.ReviewCompleted); ok {
		review = &completed
	}
	stored, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: run.Version,
		Event:           event.EventKind(),
		Run:             decision.Run,
		Observation:     observation,
		Review:          review,
	})
	if err != nil {
		t.Fatalf("workflow transition %s を保存できない: %v", event.EventKind(), err)
	}
	return stored
}

type cancelAfterObservationDB struct {
	pgx.Tx
	cancel context.CancelFunc
}

func (db cancelAfterObservationDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &cancelAfterObservationTx{Tx: tx, cancel: db.cancel}, nil
}

type cancelAfterObservationTx struct {
	pgx.Tx
	cancel context.CancelFunc
}

func (tx *cancelAfterObservationTx) Exec(
	ctx context.Context,
	sql string,
	arguments ...any,
) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, arguments...)
	if err == nil && strings.Contains(sql, "INSERT INTO run_issue_observations") {
		tx.cancel()
	}
	return tag, err
}

// TestRunStoreAcceptsTestRevisionRequiredRoundConsumption は、implement lane 発の
// test 差し戻しが test_validity の round 予算を消費できることを検証する。
//
// この差し戻しは reviewer の verdict ではないため Review binding を持たないが、
// test gate を再び開いて無人 loop を継続させる以上、予算は消費する（ADR-0003 D2
// 2026-08-21 追記）。Store が「Review event 以外は counter を動かせない」と拒むと、
// implement→revise→approve→implement の往復がどの予算にも数えられなくなる。
func TestRunStoreAcceptsTestRevisionRequiredRoundConsumption(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("test-revision-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-test-revision", 1380, observation), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.Rounds.TestValidity++
	next.TotalRounds.TestValidity++
	stored, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: next.Version,
		Event:           workflow.KindTestRevisionRequired,
		Run:             next,
	})
	if err != nil {
		t.Fatalf("test_revision_required の round 消費を保存できない: %v", err)
	}

	restored, err := postgresadapter.NewRunStore(pool).Load(ctx, created.ID)
	if err != nil {
		t.Fatalf("Run を復元できない: %v", err)
	}
	if restored.Rounds != stored.Rounds || restored.TotalRounds != stored.TotalRounds {
		t.Fatalf("復元した counter = %+v / %+v, want %+v / %+v",
			restored.Rounds, restored.TotalRounds, stored.Rounds, stored.TotalRounds)
	}
	if restored.TotalRounds.TestValidity != 1 {
		t.Fatalf("生涯 test_validity counter = %d, want 1", restored.TotalRounds.TestValidity)
	}

	// round を消費しない event は counter を動かせない、という規律自体は保つ。
	drifting := restored
	drifting.TotalRounds.TestValidity++
	if _, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: restored.Version,
		Event:           workflow.KindOperationStarted,
		Run:             drifting,
	}); !errors.Is(err, postgresadapter.ErrInvalidRun) {
		t.Fatalf("round を消費しない event の counter 変更 error = %v, want ErrInvalidRun", err)
	}
}
