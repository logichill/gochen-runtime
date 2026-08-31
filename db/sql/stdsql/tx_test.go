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
