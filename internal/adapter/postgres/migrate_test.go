package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

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

func TestValidateAppliedMigrationsRejectsVersionAndChecksumDrift(t *testing.T) {
	want := migrations[0]
	tests := map[string][]appliedMigration{
		"gap": {
			{version: 2, name: want.name, checksum: want.checksum},
		},
		"future": {
			{version: 1, name: want.name, checksum: want.checksum},
			{version: 2, name: "0002_future", checksum: "sha256:future"},
		},
		"name drift": {
			{version: 1, name: "0001_renamed", checksum: want.checksum},
		},
		"checksum drift": {
			{version: 1, name: want.name, checksum: "sha256:changed"},
		},
	}

	for name, applied := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateAppliedMigrations(applied, true)
			if !errors.Is(err, ErrSchemaVersionMismatch) {
				t.Fatalf("error = %v, want ErrSchemaVersionMismatch", err)
			}
		})
	}
}

func TestValidateAppliedMigrationsAcceptsExactCurrentVersion(t *testing.T) {
	if len(migrations) != CurrentSchemaVersion {
		t.Fatalf("migration count = %d, CurrentSchemaVersion = %d", len(migrations), CurrentSchemaVersion)
	}

	want := migrations[0]
	err := validateAppliedMigrations([]appliedMigration{{
		version:  want.version,
		name:     want.name,
		checksum: want.checksum,
	}}, true)
	if err != nil {
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
