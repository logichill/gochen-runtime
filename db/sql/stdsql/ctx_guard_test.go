package stdsql

import (
	"context"
	"testing"

	"gochen/db"
	"gochen/errors"
	"gochen/testkit/require"

	_ "modernc.org/sqlite"
)

func TestDB_ContextNilGuard(t *testing.T) {
	ctx := context.Background()
	db, err := NewWithContext(ctx, db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Query(nil, "SELECT 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = db.Exec(nil, "SELECT 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	var v int
	err = db.QueryRow(nil, "SELECT 1").Scan(&v)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestTx_ContextNilGuard(t *testing.T) {
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
	defer func() { _ = tx.Close() }()

	_, err = tx.Query(nil, "SELECT 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = tx.Exec(nil, "SELECT 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	var v int
	err = tx.QueryRow(nil, "SELECT 1").Scan(&v)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}
