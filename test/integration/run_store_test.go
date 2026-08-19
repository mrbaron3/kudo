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

func TestRunStoreRestoresOpaqueBindingsAndObservationLineage(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initialObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("observation-1")),
	}
	run := claimedRun("run-round-trip", 1301, initialObservation.Digest)
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
	}, nil)

	secondObservation := contract.IssueObservationRef{
		Schema: "kudo.issue-observation/v1alpha2",
		Digest: contract.SHA256([]byte("observation-2")),
	}
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: secondObservation.Digest,
	}, &secondObservation)

	// digest が同じでも schema が変われば別の opaque ref であり、lineage に残す。
	thirdObservation := contract.IssueObservationRef{
		Schema: "kudo.issue-observation/v1alpha3",
		Digest: secondObservation.Digest,
	}
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: thirdObservation.Digest,
	}, &thirdObservation)
	// 同一refの再観測はRun履歴には残るが、Observation lineageを重複させない。
	stored = persistEvent(t, ctx, store, stored, workflow.ObservationRecorded{
		Observation: thirdObservation.Digest,
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
		{RunVersion: 1, Ref: initialObservation},
		{RunVersion: 9, Ref: secondObservation},
		{RunVersion: 10, Ref: thirdObservation},
	}
	if !reflect.DeepEqual(lineage, wantLineage) {
		t.Fatalf("Observation lineage = %#v, want %#v", lineage, wantLineage)
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
	created, err := store.Create(ctx, claimedRun("run-rounds", 1303, observation.Digest), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	next := created
	next.Phase = workflow.PhaseAuthoringTests
	next.Rounds = workflow.ReviewRounds{TestValidity: 2}
	next.TotalRounds = workflow.ReviewRounds{TestValidity: 5, FinalImplementation: 1}
	if _, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: created.Version,
		Event:           workflow.KindReviewCompleted,
		Run:             next,
	}); err != nil {
		t.Fatalf("round counter を進められない: %v", err)
	}

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

// TestRunStoreRejectsRoundsAboveTotal は無人区間 counter が生涯 counter を超える保存を
// database が拒むことを検証する。reset 忘れと総数の取りこぼしはどちらもこの形で現れる。
func TestRunStoreRejectsRoundsAboveTotal(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	observation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("rounds-invariant-observation")),
	}
	created, err := store.Create(ctx, claimedRun("run-rounds-invariant", 1304, observation.Digest), observation)
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
	if err == nil {
		t.Fatal("rounds > total_rounds が保存できてしまった")
	}
	if !strings.Contains(err.Error(), "runs_rounds_within_total") {
		t.Fatalf("error = %v, want runs_rounds_within_total 違反", err)
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
	created, err := store.Create(ctx, claimedRun("run-cas", 1302, observation.Digest), observation)
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

func TestRunStoreAllowsOnlyOneWriterPerIssue(t *testing.T) {
	pool := migrateTestDatabase(t)
	store := postgresadapter.NewRunStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("writer-1")),
	}
	if _, err := store.Create(ctx, claimedRun("run-writer-1", 1303, firstObservation.Digest), firstObservation); err != nil {
		t.Fatalf("最初の writer Run を作成できない: %v", err)
	}
	secondObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("writer-2")),
	}
	_, err := store.Create(ctx, claimedRun("run-writer-2", 1303, secondObservation.Digest), secondObservation)
	if !errors.Is(err, postgresadapter.ErrActiveRun) {
		t.Fatalf("同じ Issue の二つ目の writer error = %v, want ErrActiveRun", err)
	}

	otherIssueObservation := contract.IssueObservationRef{
		Schema: contract.IssueObservationSchemaV1Alpha1,
		Digest: contract.SHA256([]byte("other-issue")),
	}
	if _, err := store.Create(ctx, claimedRun("run-other-issue", 1304, otherIssueObservation.Digest), otherIssueObservation); err != nil {
		t.Fatalf("別 Issue の独立 Run が global lock で拒否された: %v", err)
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
	created, err := store.Create(ctx, claimedRun("run-schema-guard", 1305, observation.Digest), observation)
	if err != nil {
		t.Fatalf("Run を作成できない: %v", err)
	}

	// Store の事前判定を迂回し、schema 自身の guard へ到達させる。
	_, err = pool.Exec(ctx, `
		UPDATE runs
		SET context_manifest_digest = $2
		WHERE id = $1
	`, created.ID, contract.SHA256([]byte("rewritten-context")))
	assertConstraintViolation(t, err, "runs_input_immutable")

	_, err = pool.Exec(ctx, "DELETE FROM run_issue_observations WHERE run_id = $1", created.ID)
	assertConstraintViolation(t, err, "run_issue_observations_append_only")
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

func claimedRun(id string, issueNumber int, observation contract.Digest) workflow.Run {
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
		RoundLimits: contract.ReviewRoundLimits{TestValidity: 3, FinalImplementation: 3},
		Observation: observation,
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
	stored, err := store.Transition(ctx, postgresadapter.Transition{
		ExpectedVersion: run.Version,
		Event:           event.EventKind(),
		Run:             decision.Run,
		Observation:     observation,
	})
	if err != nil {
		t.Fatalf("workflow transition %s を保存できない: %v", event.EventKind(), err)
	}
	return stored
}
