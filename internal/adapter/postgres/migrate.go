package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CurrentSchemaVersion はこの binary が検証・適用できる最新 schema version である。
const CurrentSchemaVersion = 1

const migrationLockKey = int64(0x4b55444f00000001)

var (
	// ErrSchemaNotInitialized は migration 履歴 table が存在しない場合に返る。
	ErrSchemaNotInitialized = errors.New("PostgreSQL schema is not initialized")
	// ErrSchemaVersionMismatch は適用履歴がこの binary の migration 列と一致しない場合に返る。
	ErrSchemaVersionMismatch = errors.New("PostgreSQL schema version does not match this binary")
)

//go:embed migrations/0001_run_store.sql
var migration0001SQL string

var migrations = []migration{
	newMigration(1, "0001_run_store.sql", migration0001SQL),
}

type migration struct {
	version  int
	name     string
	checksum string
	sql      string
}

type appliedMigration struct {
	version  int
	name     string
	checksum string
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type schemaQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MigrateUp は全 migration を一つの transaction と advisory lock の内側で適用する。
// 適用済みの version は name と checksum も一致する必要があり、履歴の改変や
// この binary より新しい schema を検出した場合は何も追加適用しない。
func MigrateUp(ctx context.Context, db beginner) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration transaction を開始する: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migration lock を取得する: %w", err)
	}

	initialized, err := schemaMigrationTableExists(ctx, tx)
	if err != nil {
		return err
	}
	start := 0
	if initialized {
		applied, err := loadAppliedMigrations(ctx, tx)
		if err != nil {
			return err
		}
		if err := validateAppliedMigrations(applied, false); err != nil {
			return err
		}
		start = len(applied)
	}

	for _, item := range migrations[start:] {
		if _, err := tx.Exec(ctx, item.sql); err != nil {
			return fmt.Errorf("migration %d (%s) を適用する: %w", item.version, item.name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kudo_schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
		`, item.version, item.name, item.checksum); err != nil {
			return fmt.Errorf("migration %d の履歴を記録する: %w", item.version, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration transaction を commit する: %w", err)
	}
	return nil
}

// ValidateSchemaVersion は schema がこの binary の全 migration と正確に一致することを
// 検証する。未初期化と、future・gap・name/checksum drift は区別可能な sentinel error で返す。
func ValidateSchemaVersion(ctx context.Context, db schemaQuerier) error {
	initialized, err := schemaMigrationTableExists(ctx, db)
	if err != nil {
		return err
	}
	if !initialized {
		return ErrSchemaNotInitialized
	}

	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	return validateAppliedMigrations(applied, true)
}

func schemaMigrationTableExists(ctx context.Context, db schemaQuerier) (bool, error) {
	var exists bool
	if err := db.QueryRow(ctx, `
		SELECT to_regclass(format('%I.%I', current_schema(), 'kudo_schema_migrations')) IS NOT NULL
	`).Scan(&exists); err != nil {
		return false, fmt.Errorf("migration 履歴 table を確認する: %w", err)
	}
	return exists, nil
}

func loadAppliedMigrations(ctx context.Context, db schemaQuerier) ([]appliedMigration, error) {
	var versions []int32
	var names, checksums []string
	if err := db.QueryRow(ctx, `
		SELECT
			COALESCE(array_agg(version ORDER BY version), ARRAY[]::integer[]),
			COALESCE(array_agg(name ORDER BY version), ARRAY[]::text[]),
			COALESCE(array_agg(checksum ORDER BY version), ARRAY[]::text[])
		FROM kudo_schema_migrations
	`).Scan(&versions, &names, &checksums); err != nil {
		return nil, fmt.Errorf("migration 履歴を読む: %w", err)
	}
	if len(versions) != len(names) || len(versions) != len(checksums) {
		return nil, fmt.Errorf("migration 履歴の列数が一致しない")
	}

	applied := make([]appliedMigration, len(versions))
	for i := range versions {
		applied[i] = appliedMigration{
			version:  int(versions[i]),
			name:     names[i],
			checksum: checksums[i],
		}
	}
	return applied, nil
}

func validateAppliedMigrations(applied []appliedMigration, requireCurrent bool) error {
	if len(applied) == 0 {
		return schemaVersionMismatch("migration 履歴が空です")
	}
	if len(applied) > len(migrations) {
		return schemaVersionMismatch("schema version %d は current version %d より新しいです", applied[len(applied)-1].version, CurrentSchemaVersion)
	}

	for i, got := range applied {
		want := migrations[i]
		if got.version != want.version {
			return schemaVersionMismatch("version %d が欠落しています", want.version)
		}
		if got.name != want.name {
			return schemaVersionMismatch("version %d の name = %q, want %q", want.version, got.name, want.name)
		}
		if got.checksum != want.checksum {
			return schemaVersionMismatch("version %d (%s) の checksum が一致しません", want.version, want.name)
		}
	}
	if requireCurrent && len(applied) != len(migrations) {
		return schemaVersionMismatch("schema version %d, want %d", applied[len(applied)-1].version, CurrentSchemaVersion)
	}
	return nil
}

func newMigration(version int, name, sql string) migration {
	sum := sha256.Sum256([]byte(sql))
	return migration{
		version:  version,
		name:     name,
		checksum: "sha256:" + hex.EncodeToString(sum[:]),
		sql:      sql,
	}
}

func schemaVersionMismatch(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	return fmt.Errorf("%w: %s", ErrSchemaVersionMismatch, detail)
}
