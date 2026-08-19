package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"
)

// migrationDigests は適用済み migration の内容を build 時に固定する。
//
// goose の履歴 table は version 番号だけを持ち、適用済み migration の中身が後から
// 変わったことを runtime では検出できない。適用済み file の書き換えは、稼働中の
// database と binary の期待が無言で食い違う唯一の経路なので、ここで固定して CI が落とす。
//
// 新しい migration を足すときだけ entry を追加する。既存 entry の値を書き換えることは
// 「適用済みの migration を改変した」ことを意味し、その database は再現できなくなる。
var migrationDigests = map[string]string{
	"0001_run_store.sql": "sha256:e634b8fb5a13b9edf994ddeb522f1c5ab88b46be42e259a2fbd57eed77532c5b",
}

func TestMigrationFileContentsAreFixed(t *testing.T) {
	root, err := fs.Sub(migrationsFS, migrationsDir)
	if err != nil {
		t.Fatalf("migration filesystem を開けない: %v", err)
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatalf("migration を列挙できない: %v", err)
	}

	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		seen[name] = true

		want, ok := migrationDigests[name]
		if !ok {
			t.Errorf("migration %s が migrationDigests に無い。新規追加なら entry を足す", name)
			continue
		}

		data, err := fs.ReadFile(root, name)
		if err != nil {
			t.Errorf("migration %s を読めない: %v", name, err)
			continue
		}
		sum := sha256.Sum256(data)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if got != want {
			t.Errorf("migration %s の内容が変わっている\n got %s\nwant %s\n"+
				"適用済み migration を改変すると、既存 database を再現できなくなる。"+
				"意図した変更なら新しい migration を足す", name, got, want)
		}
	}

	for name := range migrationDigests {
		if !seen[name] {
			t.Errorf("migrationDigests の %s が migrations/ に無い", name)
		}
	}
}

func TestMigrationCountMatchesCurrentSchemaVersion(t *testing.T) {
	if len(migrationDigests) != CurrentSchemaVersion {
		t.Fatalf("migration 数 = %d, CurrentSchemaVersion = %d", len(migrationDigests), CurrentSchemaVersion)
	}
}

func TestValidateSchemaVersionRejectsUninitializedSchema(t *testing.T) {
	querier := &stubQuerier{rows: []pgx.Row{
		scanRow(func(dest ...any) {
			*(dest[0].(*bool)) = false
		}),
	}}

	err := ValidateSchemaVersion(context.Background(), querier)
	if !errors.Is(err, ErrSchemaNotInitialized) {
		t.Fatalf("error = %v, want ErrSchemaNotInitialized", err)
	}
}

func TestValidateSchemaVersionRejectsVersionDrift(t *testing.T) {
	tests := map[string]int64{
		"未適用":    0,
		"future": CurrentSchemaVersion + 1,
	}

	for name, version := range tests {
		t.Run(name, func(t *testing.T) {
			querier := &stubQuerier{rows: []pgx.Row{
				scanRow(func(dest ...any) { *(dest[0].(*bool)) = true }),
				scanRow(func(dest ...any) { *(dest[0].(*int64)) = version }),
			}}

			err := ValidateSchemaVersion(context.Background(), querier)
			if !errors.Is(err, ErrSchemaVersionMismatch) {
				t.Fatalf("error = %v, want ErrSchemaVersionMismatch", err)
			}
		})
	}
}

func TestValidateSchemaVersionAcceptsCurrentVersion(t *testing.T) {
	querier := &stubQuerier{rows: []pgx.Row{
		scanRow(func(dest ...any) { *(dest[0].(*bool)) = true }),
		scanRow(func(dest ...any) { *(dest[0].(*int64)) = CurrentSchemaVersion }),
	}}

	if err := ValidateSchemaVersion(context.Background(), querier); err != nil {
		t.Fatalf("error = %v", err)
	}
}

type stubQuerier struct {
	rows []pgx.Row
	next int
}

func (q *stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	row := q.rows[q.next]
	q.next++
	return row
}

type scanRow func(dest ...any)

func (f scanRow) Scan(dest ...any) error {
	f(dest...)
	return nil
}
