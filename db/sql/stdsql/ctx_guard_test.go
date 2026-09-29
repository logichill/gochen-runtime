package stdsql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"gochen/db"
	"gochen/errors"
	"gochen/testkit/require"

	_ "modernc.org/sqlite"
)

// newContextGuardDB 为 ctx 契约用例提供独立的 sqlite 内存库。
func newContextGuardDB(t *testing.T) db.IDatabase {
	t.Helper()
	database, err := NewWithContext(context.Background(), db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// ctxCase 描述取消与超时两类已失效上下文及其期望的底层错误。
type ctxCase struct {
	name string
	ctx  context.Context
	want error
}

func newContextCases(t *testing.T) []ctxCase {
	t.Helper()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	expired, expire := context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(expire)
	<-expired.Done()

	return []ctxCase{
		{name: "canceled", ctx: canceled, want: context.Canceled},
		{name: "expired", ctx: expired, want: context.DeadlineExceeded},
	}
}

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

	err = db.QueryRow(nil, "SELECT 1").Err()
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = db.Begin(nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = db.BeginTx(nil, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	err = db.Ping(nil)
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

	err = tx.QueryRow(nil, "SELECT 1").Err()
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	// 嵌套事务规则优先于取消语义：nil ctx 先判 InvalidInput，
	// 非 nil ctx 一律返回 Unsupported，即使该 ctx 已取消或超时。
	_, err = tx.Begin(nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = tx.BeginTx(nil, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	err = tx.Ping(nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestDB_ContextCancelAndTimeout(t *testing.T) {
	database := newContextGuardDB(t)
	for _, tc := range newContextCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.Query(tc.ctx, "SELECT 1"); !errors.Is(err, tc.want) {
				t.Fatalf("Query error = %v, want %v", err, tc.want)
			}
			if _, err := database.Exec(tc.ctx, "SELECT 1"); !errors.Is(err, tc.want) {
				t.Fatalf("Exec error = %v, want %v", err, tc.want)
			}
			row := database.QueryRow(tc.ctx, "SELECT 1")
			if err := row.Scan(new(int)); !errors.Is(err, tc.want) {
				t.Fatalf("QueryRow.Scan error = %v, want %v", err, tc.want)
			}
			if err := row.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("QueryRow.Err error = %v, want %v", err, tc.want)
			}
			if _, err := database.Begin(tc.ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Begin error = %v, want %v", err, tc.want)
			}
			if _, err := database.BeginTx(tc.ctx, nil); !errors.Is(err, tc.want) {
				t.Fatalf("BeginTx error = %v, want %v", err, tc.want)
			}
			if err := database.Ping(tc.ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Ping error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTx_ContextCancelAndTimeout(t *testing.T) {
	database := newContextGuardDB(t)
	for _, tc := range newContextCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := database.Begin(context.Background())
			require.NoError(t, err)
			defer func() { _ = tx.Close() }()

			if _, err := tx.Query(tc.ctx, "SELECT 1"); !errors.Is(err, tc.want) {
				t.Fatalf("Query error = %v, want %v", err, tc.want)
			}
			if _, err := tx.Exec(tc.ctx, "SELECT 1"); !errors.Is(err, tc.want) {
				t.Fatalf("Exec error = %v, want %v", err, tc.want)
			}
			row := tx.QueryRow(tc.ctx, "SELECT 1")
			if err := row.Scan(new(int)); !errors.Is(err, tc.want) {
				t.Fatalf("QueryRow.Scan error = %v, want %v", err, tc.want)
			}
			if err := row.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("QueryRow.Err error = %v, want %v", err, tc.want)
			}
			if err := tx.Ping(tc.ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Ping error = %v, want %v", err, tc.want)
			}

			if _, err := tx.Begin(tc.ctx); !errors.Is(err, errors.Unsupported) {
				t.Fatalf("nested Begin error = %v, want Unsupported", err)
			}
			if _, err := tx.BeginTx(tc.ctx, nil); !errors.Is(err, errors.Unsupported) {
				t.Fatalf("nested BeginTx error = %v, want Unsupported", err)
			}
		})
	}
}

func TestRow_ScanAndErrContract(t *testing.T) {
	database := newContextGuardDB(t)
	ctx := context.Background()

	if v := database.QueryRow(ctx, "SELECT 1"); v.Scan(new(int)) != nil {
		t.Fatal("normal QueryRow should scan without error")
	}

	badSQL := database.QueryRow(ctx, "SELECT * FROM missing_table")
	if err := badSQL.Scan(new(int)); err == nil {
		t.Fatal("expected bad SQL Scan error")
	}
	if err := badSQL.Err(); err == nil {
		t.Fatal("expected bad SQL Err to expose the deferred error")
	}

	empty := database.QueryRow(ctx, "SELECT 1 WHERE 0")
	if err := empty.Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty result Scan error = %v, want sql.ErrNoRows", err)
	}
	if err := empty.Err(); err != nil {
		t.Fatalf("empty result Err = %v, want nil", err)
	}

	detached := &Row{}
	if err := detached.Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("nil raw row Scan error = %v, want sql.ErrNoRows", err)
	}
	if err := detached.Err(); err != nil {
		t.Fatalf("nil raw row Err = %v, want nil", err)
	}
}
