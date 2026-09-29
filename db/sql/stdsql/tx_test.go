package stdsql

import (
	"context"
	"testing"

	"gochen/db"
	"gochen/errors"
	"gochen/testkit/assert"
	"gochen/testkit/require"

	_ "modernc.org/sqlite"
)

func TestTx_Close_RollbacksWhenNotCommitted(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)

	tx, err := db.Begin(ctx)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, "INSERT INTO t(v) VALUES (?)", "x")
	require.NoError(t, err)

	require.NoError(t, tx.Close())

	var count int
	require.NoError(t, db.QueryRow(ctx, "SELECT COUNT(*) FROM t").Scan(&count))
	assert.Equal(t, 0, count)
}

func TestTx_Close_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)

	tx, err := db.Begin(ctx)
	require.NoError(t, err)

	require.NoError(t, tx.Close())
	require.NoError(t, tx.Close())
}

func TestTx_Close_AfterCommit_IsNoop(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)

	tx, err := db.Begin(ctx)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, "INSERT INTO t(v) VALUES (?)", "x")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, tx.Close())

	var count int
	require.NoError(t, db.QueryRow(ctx, "SELECT COUNT(*) FROM t").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestTx_SavepointRejectsUnsafeIdentifier(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	tx, err := db.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	savepointTx, ok := tx.(interface {
		CreateSavepoint(context.Context, string) error
	})
	require.True(t, ok)

	err = savepointTx.CreateSavepoint(ctx, "sp;drop")
	require.Error(t, err)
	assert.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestTx_CommitRejectsNilTx(t *testing.T) {
	var tx *Tx
	err := tx.Commit()
	require.Error(t, err)
	assert.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestTx_SavepointRejectsQualifiedIdentifier(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	tx, err := db.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	savepointTx, ok := tx.(interface {
		CreateSavepoint(context.Context, string) error
	})
	require.True(t, ok)

	err = savepointTx.CreateSavepoint(ctx, "schema.sp")
	require.Error(t, err)
	assert.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestDB_QueryRowsAndSavepoints(t *testing.T) {
	ctx := context.Background()
	database, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	_, err = database.Exec(ctx, "CREATE TABLE rows (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)
	tx, err := database.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Close() }()

	_, err = tx.Exec(ctx, "INSERT INTO rows(value) VALUES (?)", "first")
	require.NoError(t, err)
	sp := tx.(interface {
		CreateSavepoint(context.Context, string) error
		RollbackToSavepoint(context.Context, string) error
		ReleaseSavepoint(context.Context, string) error
	})
	require.NoError(t, sp.CreateSavepoint(ctx, "before_second"))
	_, err = tx.Exec(ctx, "INSERT INTO rows(value) VALUES (?)", "second")
	require.NoError(t, err)
	require.NoError(t, sp.RollbackToSavepoint(ctx, "before_second"))
	require.NoError(t, sp.ReleaseSavepoint(ctx, "before_second"))
	require.NoError(t, tx.Commit())

	rows, err := database.Query(ctx, "SELECT id, value FROM rows ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	require.NoError(t, err)
	if len(columns) != 2 {
		t.Fatalf("columns = %v, want two columns", columns)
	}
	if !rows.Next() {
		t.Fatal("expected first row")
	}
	var id int
	var value string
	require.NoError(t, rows.Scan(&id, &value))
	if id != 1 || value != "first" {
		t.Fatalf("row = (%d, %q), want (1, first)", id, value)
	}
	if rows.Next() {
		t.Fatal("savepoint rollback should remove second row")
	}
	require.NoError(t, rows.Err())

	if err := database.QueryRow(ctx, "SELECT value FROM rows WHERE id = ?", 1).Scan(&value); err != nil {
		t.Fatalf("QueryRow.Scan: %v", err)
	}
	if value != "first" {
		t.Fatalf("QueryRow value = %q, want first", value)
	}
	if err := database.QueryRow(ctx, "SELECT * FROM missing_table").Err(); err == nil {
		t.Fatal("expected QueryRow.Err to expose deferred query error")
	}
}

func TestTx_NestedTransactionsAreRejected(t *testing.T) {
	ctx := context.Background()
	database, err := NewWithContext(ctx, db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	tx, err := database.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Close() }()
	if _, err := tx.Begin(ctx); !errors.Is(err, errors.Unsupported) {
		t.Fatalf("nested Begin error = %v, want Unsupported", err)
	}
	if _, err := tx.BeginTx(ctx, nil); !errors.Is(err, errors.Unsupported) {
		t.Fatalf("nested BeginTx error = %v, want Unsupported", err)
	}
}
