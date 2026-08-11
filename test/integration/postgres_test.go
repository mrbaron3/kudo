//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// connect は KUDO_TEST_DATABASE_URL が指す PostgreSQL へ接続する。
// opt-in は build tag で表明済みのため、必須入力の欠落は skip ではなく failure にする。
func connect(t *testing.T) *pgx.Conn {
	t.Helper()

	url := os.Getenv("KUDO_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("KUDO_TEST_DATABASE_URL を設定して実行する（例: docker compose run --build --rm integration）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("PostgreSQL へ接続できない: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = conn.Close(closeCtx)
	})
	return conn
}

// TestPostgreSQLServerVersion は development stack の PostgreSQL が契約どおり
// 18.4 系であることを internal network 経由で検証する。
func TestPostgreSQLServerVersion(t *testing.T) {
	conn := connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		t.Fatalf("server_version を取得できない: %v", err)
	}
	if !strings.HasPrefix(version, "18.4") {
		t.Fatalf("server_version = %q, want prefix \"18.4\"", version)
	}
}

// TestPostgreSQLRoundTrip は healthy な database へ書き込みと読み出しができることを
// 検証する。temporary table を使い、永続 state を残さない。
func TestPostgreSQLRoundTrip(t *testing.T) {
	conn := connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := conn.Exec(ctx, "CREATE TEMPORARY TABLE kudo_smoke (id int PRIMARY KEY, note text NOT NULL)"); err != nil {
		t.Fatalf("temporary table を作成できない: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO kudo_smoke (id, note) VALUES ($1, $2)", 1, "m0"); err != nil {
		t.Fatalf("INSERT に失敗した: %v", err)
	}

	var note string
	if err := conn.QueryRow(ctx, "SELECT note FROM kudo_smoke WHERE id = $1", 1).Scan(&note); err != nil {
		t.Fatalf("SELECT に失敗した: %v", err)
	}
	if note != "m0" {
		t.Fatalf("note = %q, want \"m0\"", note)
	}
}
