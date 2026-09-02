package pgxadapter_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pgxadapter "github.com/yardrail/casbin-pgx-adapter"
)

// TestNewAdapterWithDB_sharesTransaction is the reason this fork exists:
// an adapter bound to a caller's pgx.Tx (via NewAdapterWithDB) must have its
// policy writes committed or rolled back together with the other work in
// that transaction. The upstream adapter routes every query through a
// database/sql *sql.DB (stdlib.OpenDBFromPool), which hands out its own
// connections and cannot join a pgx-native transaction.
func TestNewAdapterWithDB_sharesTransaction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, getTestDBURL())
	if err != nil {
		t.Skipf("Could not connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	const table = "casbin_test_tx_share"

	// The adapter does not create tables for a bare DB, so set up the
	// schema (and a companion "domain" table) up front, then clean up.
	quoted := pgx.Identifier{table}.Sanitize()
	mustExec(t, pool, "DROP TABLE IF EXISTS "+quoted+" CASCADE")
	mustExec(t, pool, "DROP TABLE IF EXISTS casbin_test_tx_domain CASCADE")
	mustExec(t, pool, `CREATE TABLE `+quoted+` (
		id SERIAL PRIMARY KEY, ptype VARCHAR(100) NOT NULL,
		v0 VARCHAR(100), v1 VARCHAR(100), v2 VARCHAR(100),
		v3 VARCHAR(100), v4 VARCHAR(100), v5 VARCHAR(100))`)
	mustExec(t, pool, `CREATE TABLE casbin_test_tx_domain (id SERIAL PRIMARY KEY, name TEXT NOT NULL)`)
	t.Cleanup(func() {
		mustExec(t, pool, "DROP TABLE IF EXISTS "+quoted+" CASCADE")
		mustExec(t, pool, "DROP TABLE IF EXISTS casbin_test_tx_domain CASCADE")
	})

	countPolicies := func(q pgxadapter.DB) int {
		t.Helper()
		var n int
		if err := q.QueryRow(ctx, "SELECT count(*) FROM "+quoted).Scan(&n); err != nil {
			t.Fatalf("count policies: %v", err)
		}
		return n
	}
	countDomain := func(q pgxadapter.DB) int {
		t.Helper()
		var n int
		if err := q.QueryRow(ctx, "SELECT count(*) FROM casbin_test_tx_domain").Scan(&n); err != nil {
			t.Fatalf("count domain: %v", err)
		}
		return n
	}

	// writeInTx inserts a domain row and a policy row, both through tx.
	writeInTx := func(tx pgx.Tx) {
		t.Helper()

		if _, err := tx.Exec(ctx, "INSERT INTO casbin_test_tx_domain (name) VALUES ('acme')"); err != nil {
			t.Fatalf("insert domain row: %v", err)
		}

		adapter, err := pgxadapter.NewAdapterWithDB(tx, pgxadapter.WithTableName(table))
		if err != nil {
			t.Fatalf("NewAdapterWithDB(tx): %v", err)
		}

		if err := adapter.AddPolicy("p", "p", []string{"alice", "data1", "read"}); err != nil {
			t.Fatalf("AddPolicy: %v", err)
		}

		if got := countPolicies(tx); got != 1 {
			t.Fatalf("policies visible inside tx = %d, want 1", got)
		}
		if got := countDomain(tx); got != 1 {
			t.Fatalf("domain rows visible inside tx = %d, want 1", got)
		}
	}

	t.Run("rollback discards both", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		writeInTx(tx)

		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("rollback: %v", err)
		}

		if got := countPolicies(pool); got != 0 {
			t.Errorf("policies after rollback = %d, want 0", got)
		}
		if got := countDomain(pool); got != 0 {
			t.Errorf("domain rows after rollback = %d, want 0", got)
		}
	})

	t.Run("commit persists both", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}

		writeInTx(tx)

		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}

		if got := countPolicies(pool); got != 1 {
			t.Errorf("policies after commit = %d, want 1", got)
		}
		if got := countDomain(pool); got != 1 {
			t.Errorf("domain rows after commit = %d, want 1", got)
		}
	})
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
