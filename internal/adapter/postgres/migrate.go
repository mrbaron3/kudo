package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// CurrentSchemaVersion はこの binary が検証・適用できる最新 schema version である。
const CurrentSchemaVersion = 1

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// versionTable は goose が履歴を記録する table である。
//
// 慣習名の `schema_migrations` を避ける実利がある。同一 schema に別 tool の
// `schema_migrations` があると、存在確認が true を返したうえで列の型が合わず、
// 「未初期化」でも「version 不一致」でもない診断不能な失敗になる。
const versionTable = "goose_db_version"

var (
	// ErrSchemaNotInitialized は migration 履歴 table が存在しない場合に返る。
	ErrSchemaNotInitialized = errors.New("PostgreSQL schema is not initialized")
	// ErrSchemaVersionMismatch は適用済み version がこの binary の最新 version と一致しない場合に返る。
	ErrSchemaVersionMismatch = errors.New("PostgreSQL schema version does not match this binary")
)

type schemaQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MigrateUp は未適用の migration を version 昇順で適用する。
//
// 適用は advisory lock で直列化するため、複数 container が同時に起動しても
// 同じ migration を二重に実行しない。既に最新なら何も適用せず nil を返す。
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) error {
	provider, release, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer release()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migration を適用する: %w", err)
	}
	return nil
}

// ValidateSchemaVersion は適用済み schema がこの binary の最新 version と一致することを
// 検証する。未初期化と version 不一致は区別可能な sentinel error で返す。
//
// goose の履歴 table は version 番号だけを持ち、適用済み migration の内容が後から
// 変わったことは検出できない。file 内容の固定は build 時の golden test が担う。
func ValidateSchemaVersion(ctx context.Context, db schemaQuerier) error {
	initialized, err := versionTableExists(ctx, db)
	if err != nil {
		return err
	}
	if !initialized {
		return ErrSchemaNotInitialized
	}

	var version int64
	if err := db.QueryRow(ctx,
		"SELECT COALESCE(max(version_id), 0) FROM "+versionTable+" WHERE is_applied",
	).Scan(&version); err != nil {
		return fmt.Errorf("migration 履歴を読む: %w", err)
	}
	if version != CurrentSchemaVersion {
		return schemaVersionMismatch("schema version %d, want %d", version, CurrentSchemaVersion)
	}
	return nil
}

// newProvider は embed 済み migration を読む goose provider と、その解放関数を返す。
func newProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	root, err := fs.Sub(migrationsFS, migrationsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("migration filesystem を開く: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, nil, fmt.Errorf("migration lock を構成する: %w", err)
	}

	// goose は database/sql を要求するため pgx pool を bridge する。返る *sql.DB は
	// pool の所有者ではないので、close しても pool は生き続ける。
	db := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, root,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migration provider を構成する: %w", err)
	}
	return provider, func() { _ = db.Close() }, nil
}

func versionTableExists(ctx context.Context, db schemaQuerier) (bool, error) {
	var exists bool
	if err := db.QueryRow(ctx,
		// 実際の version 読み出しと同じ search_path 解決を使う。current_schema() へ
		// 固定すると、履歴 table が後続 schema にある構成だけ存在確認と読取先がずれる。
		"SELECT to_regclass($1::text) IS NOT NULL",
		versionTable,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("migration 履歴 table を確認する: %w", err)
	}
	return exists, nil
}

func schemaVersionMismatch(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSchemaVersionMismatch, fmt.Sprintf(format, args...))
}
