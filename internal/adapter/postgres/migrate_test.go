package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mrbaron3/kudo/internal/workflow"
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
	"0001_run_store.sql": "sha256:91d9bbef3f1b4bf7bfa0ca24eb40d72ffc48127c9cee999ff6ccb1be468d5a89",
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

func TestMigrationVocabularyMatchesWorkflow(t *testing.T) {
	data, err := migrationsFS.ReadFile(migrationsDir + "/0001_run_store.sql")
	if err != nil {
		t.Fatalf("migration を読めない: %v", err)
	}

	phases := make(map[string]bool)
	activePhases := make(map[string]bool)
	for _, phase := range workflow.Phases() {
		phases[string(phase)] = true
		if phase.Active() {
			activePhases[string(phase)] = true
		}
	}
	events := make(map[string]bool)
	for _, event := range workflow.EventKinds() {
		events[string(event)] = true
	}

	for _, test := range []struct {
		name    string
		pattern string
		want    map[string]bool
	}{
		{"Run phase", `(?s)\n    phase text NOT NULL CHECK \(phase IN \((.*?)\n    \)\),`, phases},
		{"writer-capable phase", `(?s)writer_capable boolean GENERATED ALWAYS AS \(phase IN \((.*?)\n    \)\) STORED`, activePhases},
		{"transition event", `(?s)event_kind text NOT NULL CHECK \(event_kind IN \((.*?)\n    \)\),`, events},
		{"transition to phase", `(?s)to_phase text NOT NULL CHECK \(to_phase IN \((.*?)\n    \)\),`, phases},
		{"transition from phase", `(?s)from_phase IS NULL OR from_phase IN \((.*?)\n        \)`, phases},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := migrationLiteralSet(t, data, test.pattern)
			if !maps.Equal(got, test.want) {
				t.Fatalf("migration vocabulary = %v, workflow vocabulary = %v", got, test.want)
			}
		})
	}

	if matched, err := regexp.Match(
		`(?s)CREATE UNIQUE INDEX runs_one_writer_per_issue.*WHERE writer_capable OR phase = 'needs_human';`,
		data,
	); err != nil || !matched {
		t.Fatalf("writer-capable index が non-terminal phase を塞ぐ形でない: matched=%t err=%v", matched, err)
	}
}

func migrationLiteralSet(t *testing.T, data []byte, pattern string) map[string]bool {
	t.Helper()
	match := regexp.MustCompile(pattern).FindSubmatch(data)
	if len(match) != 2 {
		t.Fatalf("migration の語彙 list を抽出できない: %s", pattern)
	}
	literals := regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(match[1], -1)
	values := make(map[string]bool, len(literals))
	for _, literal := range literals {
		values[string(literal[1])] = true
	}
	return values
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
