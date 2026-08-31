package sql

import (
	"context"
	databasesql "database/sql"
	stderrors "errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	gerrors "gochen/errors"
	_ "modernc.org/sqlite"
)

// setupTestDB t：测试上下文（testing.T）（类型：*testing.T）。
//
// 参数：
//
// 返回：
// - result：测试返回值（类型：db.IDatabase）
func setupTestDB(t *testing.T) db.IDatabase {
	t.Helper()
	path := t.TempDir() + "/locks.db"
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     path,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// TestNewSQLLockProvider_ValidationAndDefaults 验证 NewSQLLockProvider ValidationAndDefaults。
func TestNewSQLLockProvider_ValidationAndDefaults(t *testing.T) {
	_, err := NewSQLLockProvider(nil, &SQLLockProviderConfig{Owner: "a"})
	if err == nil {
		t.Fatalf("expected error for nil database")
	}

	database := setupTestDB(t)

	_, err = NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: ""})
	if err == nil {
		t.Fatalf("expected error for empty owner")
	}

	_, err = NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a", TableName: "bad-name;"})
	if err == nil {
		t.Fatalf("expected error for invalid table name")
	}

	_, err = NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a", TableName: "1locks"})
	if err == nil {
		t.Fatalf("expected error for unsafe table name")
	}

	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a"})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	if p.tableName != "distributed_locks" {
		t.Fatalf("expected default table name, got %q", p.tableName)
	}
	if p.ttl != 30*time.Second {
		t.Fatalf("expected default ttl, got %v", p.ttl)
	}
	if p.pollInterval != 50*time.Millisecond {
		t.Fatalf("expected default poll interval, got %v", p.pollInterval)
	}
	if p.renewInterval != 15*time.Second {
		t.Fatalf("expected default renew interval, got %v", p.renewInterval)
	}
	if p.logger == nil {
		t.Fatalf("expected logger")
	}
}

func TestSQLLockProvider_DDLUsesDialectSafeColumnTypes(t *testing.T) {
	tests := []struct {
		name          string
		dialect       string
		wantCreate    []string
		wantNoCreate  []string
		wantAlter     []string
		wantNoAlter   []string
		wantTableExpr string
	}{
		{
			name:          "mysql",
			dialect:       "mysql",
			wantCreate:    []string{"CREATE TABLE IF NOT EXISTS `distributed_locks`", "lock_key VARCHAR(255) PRIMARY KEY", "owner VARCHAR(255) NOT NULL", "lease_token VARCHAR(64) NOT NULL DEFAULT ''", "expires_at_ms BIGINT NOT NULL"},
			wantNoCreate:  []string{"TEXT PRIMARY KEY", "lease_token TEXT"},
			wantAlter:     []string{"ALTER TABLE `distributed_locks` ADD COLUMN lease_token VARCHAR(64) NOT NULL DEFAULT ''"},
			wantNoAlter:   []string{"lease_token TEXT"},
			wantTableExpr: "`distributed_locks`",
		},
		{
			name:          "postgres",
			dialect:       "postgres",
			wantCreate:    []string{`CREATE TABLE IF NOT EXISTS "distributed_locks"`, "lock_key TEXT PRIMARY KEY", "lease_token TEXT NOT NULL DEFAULT ''", "expires_at_ms BIGINT NOT NULL"},
			wantAlter:     []string{`ALTER TABLE "distributed_locks" ADD COLUMN lease_token TEXT NOT NULL DEFAULT ''`},
			wantTableExpr: `"distributed_locks"`,
		},
		{
			name:          "sqlite",
			dialect:       "sqlite",
			wantCreate:    []string{`CREATE TABLE IF NOT EXISTS "distributed_locks"`, "lock_key TEXT PRIMARY KEY", "lease_token TEXT NOT NULL DEFAULT ''", "expires_at_ms BIGINT NOT NULL"},
			wantAlter:     []string{`ALTER TABLE "distributed_locks" ADD COLUMN lease_token TEXT NOT NULL DEFAULT ''`},
			wantTableExpr: `"distributed_locks"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewSQLLockProvider(&dialectDB{dialect: tc.dialect}, &SQLLockProviderConfig{Owner: "a"})
			if err != nil {
				t.Fatalf("NewSQLLockProvider: %v", err)
			}
			createDDL := p.lockTableDDL()
			alterDDL := p.addLeaseTokenColumnDDL()
			for _, want := range tc.wantCreate {
				if !strings.Contains(createDDL, want) {
					t.Fatalf("create ddl missing %q:\n%s", want, createDDL)
				}
			}
			for _, unwanted := range tc.wantNoCreate {
				if strings.Contains(createDDL, unwanted) {
					t.Fatalf("create ddl should not contain %q:\n%s", unwanted, createDDL)
				}
			}
			for _, want := range tc.wantAlter {
				if !strings.Contains(alterDDL, want) {
					t.Fatalf("alter ddl missing %q:\n%s", want, alterDDL)
				}
			}
			for _, unwanted := range tc.wantNoAlter {
				if strings.Contains(alterDDL, unwanted) {
					t.Fatalf("alter ddl should not contain %q:\n%s", unwanted, alterDDL)
				}
			}
			if got := p.quotedTableName(); got != tc.wantTableExpr {
				t.Fatalf("quoted table = %q, want %q", got, tc.wantTableExpr)
			}
		})
	}
}

func TestSQLLockProvider_HasLeaseTokenColumnUsesDialectCatalog(t *testing.T) {
	tests := []struct {
		name      string
		dialect   string
		tableName string
		wantQuery string
		wantArgs  []any
	}{
		{
			name:      "mysql",
			dialect:   "mysql",
			wantQuery: "information_schema.columns",
			wantArgs:  []any{"distributed_locks", "lease_token"},
		},
		{
			name:      "postgres",
			dialect:   "postgres",
			wantQuery: "pg_catalog.pg_attribute",
			wantArgs:  []any{"distributed_locks", "lease_token"},
		},
		{
			name:      "postgres uppercase table",
			dialect:   "postgres",
			tableName: "DistributedLocks",
			wantQuery: "pg_catalog.pg_attribute",
			wantArgs:  []any{"DistributedLocks", "lease_token"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			database := &dialectDB{dialect: tc.dialect, queryRows: 1}
			cfg := &SQLLockProviderConfig{Owner: "a"}
			if tc.tableName != "" {
				cfg.TableName = tc.tableName
			}
			p, err := NewSQLLockProvider(database, cfg)
			if err != nil {
				t.Fatalf("NewSQLLockProvider: %v", err)
			}

			got, err := p.hasLeaseTokenColumn(context.Background())
			if err != nil {
				t.Fatalf("hasLeaseTokenColumn: %v", err)
			}
			if !got {
				t.Fatalf("expected column to be detected")
			}
			if !strings.Contains(database.lastQuery, tc.wantQuery) {
				t.Fatalf("query = %q, want to contain %q", database.lastQuery, tc.wantQuery)
			}
			if len(database.lastQueryArgs) != len(tc.wantArgs) {
				t.Fatalf("query args = %#v, want %#v", database.lastQueryArgs, tc.wantArgs)
			}
			for i := range tc.wantArgs {
				if database.lastQueryArgs[i] != tc.wantArgs[i] {
					t.Fatalf("query args = %#v, want %#v", database.lastQueryArgs, tc.wantArgs)
				}
			}
		})
	}
}

// TestSQLLockProvider_AcquireTimeout 验证 SQLLockProvider AcquireTimeout。
func TestSQLLockProvider_AcquireTimeout(t *testing.T) {
	database := setupTestDB(t)
	p1, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "a",
		TTL:          5 * time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	p2, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "b",
		TTL:          5 * time.Second,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	release, err := p1.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = p2.Acquire(ctx, "k")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

type failingExecDB struct {
	insertErr error
}

type dialectDB struct {
	failingExecDB
	dialect       string
	queryRows     int
	lastQuery     string
	lastQueryArgs []any
}

func (d *dialectDB) DialectName() string { return d.dialect }

func (d *dialectDB) Query(_ context.Context, query string, args ...any) (db.IRows, error) {
	d.lastQuery = query
	d.lastQueryArgs = append([]any(nil), args...)
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}, count: d.queryRows}, nil
}

func (d *failingExecDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *failingExecDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *failingExecDB) Exec(_ context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "INSERT INTO"):
		return nil, d.insertErr
	case strings.Contains(query, "UPDATE"):
		return fakeSQLResult(0), nil
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *failingExecDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *failingExecDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *failingExecDB) Ping(context.Context) error { return nil }

func (d *failingExecDB) Close() error { return nil }

type fakeSQLResult int64

func (r fakeSQLResult) LastInsertId() (int64, error) { return 0, nil }

func (r fakeSQLResult) RowsAffected() (int64, error) { return int64(r), nil }

type rowsAffectedErrResult struct{}

func (rowsAffectedErrResult) LastInsertId() (int64, error) { return 0, nil }

func (rowsAffectedErrResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unsupported")
}

type sqliteUniqueViolationLikeError struct {
	Code         int64
	ExtendedCode int64
	msg          string
}

func (e sqliteUniqueViolationLikeError) Error() string { return e.msg }

type fakeRows struct {
	columns []string
	count   int
	read    int
}

func (r *fakeRows) Next() bool {
	if r.read >= r.count {
		return false
	}
	r.read++
	return true
}
func (r *fakeRows) Scan(...any) error {
	return nil
}
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Err() error   { return nil }
func (r *fakeRows) Columns() ([]string, error) {
	return append([]string(nil), r.columns...), nil
}
func (r *fakeRows) ColumnTypes() ([]*databasesql.ColumnType, error) {
	return nil, nil
}

type alterRaceDB struct {
	queries int
}

func (d *alterRaceDB) Query(context.Context, string, ...any) (db.IRows, error) {
	d.queries++
	if d.queries == 1 {
		return &fakeRows{columns: []string{"lock_key", "owner", "expires_at_ms"}}, nil
	}
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *alterRaceDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *alterRaceDB) Exec(_ context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "ALTER TABLE"):
		return nil, stderrors.New("driver-specific duplicate column wording")
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *alterRaceDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *alterRaceDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *alterRaceDB) Ping(context.Context) error { return nil }

func (d *alterRaceDB) Close() error { return nil }

type createFailsOnceDB struct {
	*failingExecDB
	createCalls int
}

func (d *createFailsOnceDB) Exec(ctx context.Context, query string, args ...any) (databasesql.Result, error) {
	if strings.Contains(query, "CREATE TABLE") {
		d.createCalls++
		if d.createCalls == 1 {
			return nil, stderrors.New("temporary create failure")
		}
		return fakeSQLResult(0), nil
	}
	return d.failingExecDB.Exec(ctx, query, args...)
}

type rowsAffectedErrAcquireDB struct{}

func (d *rowsAffectedErrAcquireDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *rowsAffectedErrAcquireDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *rowsAffectedErrAcquireDB) Exec(_ context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "INSERT INTO"):
		return nil, sqliteUniqueViolationLikeError{
			Code:         19,
			ExtendedCode: 2067,
			msg:          "UNIQUE constraint failed: distributed_locks.lock_key",
		}
	case strings.Contains(query, "UPDATE"):
		return rowsAffectedErrResult{}, nil
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *rowsAffectedErrAcquireDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *rowsAffectedErrAcquireDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *rowsAffectedErrAcquireDB) Ping(context.Context) error { return nil }

func (d *rowsAffectedErrAcquireDB) Close() error { return nil }

type ctxErrOnAcquireUpdateDB struct{}

func (d *ctxErrOnAcquireUpdateDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *ctxErrOnAcquireUpdateDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *ctxErrOnAcquireUpdateDB) Exec(ctx context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "INSERT INTO"):
		return nil, sqliteUniqueViolationLikeError{
			Code:         19,
			ExtendedCode: 2067,
			msg:          "UNIQUE constraint failed: distributed_locks.lock_key",
		}
	case strings.Contains(query, "UPDATE"):
		return nil, ctx.Err()
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *ctxErrOnAcquireUpdateDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *ctxErrOnAcquireUpdateDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *ctxErrOnAcquireUpdateDB) Ping(context.Context) error { return nil }

func (d *ctxErrOnAcquireUpdateDB) Close() error { return nil }

type blockingRenewDB struct {
	renewStarted chan struct{}
	renewDone    chan struct{}
	renewOnce    sync.Once
}

func newBlockingRenewDB() *blockingRenewDB {
	return &blockingRenewDB{
		renewStarted: make(chan struct{}),
		renewDone:    make(chan struct{}),
	}
}

func (d *blockingRenewDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *blockingRenewDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *blockingRenewDB) Exec(ctx context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "INSERT INTO"):
		return fakeSQLResult(1), nil
	case strings.Contains(query, "UPDATE") && strings.Contains(query, "expires_at_ms = ?"):
		d.renewOnce.Do(func() { close(d.renewStarted) })
		<-ctx.Done()
		close(d.renewDone)
		return nil, ctx.Err()
	case strings.Contains(query, "DELETE FROM"):
		return fakeSQLResult(1), nil
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *blockingRenewDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *blockingRenewDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *blockingRenewDB) Ping(context.Context) error { return nil }

func (d *blockingRenewDB) Close() error { return nil }

type unresponsiveRenewDB struct {
	renewStarted chan struct{}
	allowRenew   chan struct{}
	renewOnce    sync.Once
}

func newUnresponsiveRenewDB() *unresponsiveRenewDB {
	return &unresponsiveRenewDB{
		renewStarted: make(chan struct{}),
		allowRenew:   make(chan struct{}),
	}
}

func (d *unresponsiveRenewDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *unresponsiveRenewDB) QueryRow(context.Context, string, ...any) db.IRow {
	return nil
}

func (d *unresponsiveRenewDB) Exec(_ context.Context, query string, _ ...any) (databasesql.Result, error) {
	switch {
	case strings.Contains(query, "CREATE TABLE"):
		return fakeSQLResult(0), nil
	case strings.Contains(query, "INSERT INTO"):
		return fakeSQLResult(1), nil
	case strings.Contains(query, "UPDATE") && strings.Contains(query, "expires_at_ms = ?"):
		d.renewOnce.Do(func() { close(d.renewStarted) })
		<-d.allowRenew
		return fakeSQLResult(1), nil
	case strings.Contains(query, "DELETE FROM"):
		return fakeSQLResult(1), nil
	default:
		return fakeSQLResult(0), nil
	}
}

func (d *unresponsiveRenewDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *unresponsiveRenewDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *unresponsiveRenewDB) Ping(context.Context) error { return nil }

func (d *unresponsiveRenewDB) Close() error { return nil }

func TestSQLLockProvider_AcquireLeaseReturnsDatabaseErrorForInsertFailure(t *testing.T) {
	p, err := NewSQLLockProvider(&failingExecDB{insertErr: stderrors.New("permission denied")}, &SQLLockProviderConfig{
		Owner: "a",
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	_, err = p.AcquireLease(context.Background(), "k")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !gerrors.Is(err, gerrors.Database) {
		t.Fatalf("expected database error, got %v", err)
	}
}

func TestSQLLockProvider_AcquireLeaseReturnsDatabaseErrorForRowsAffectedFailure(t *testing.T) {
	p, err := NewSQLLockProvider(&rowsAffectedErrAcquireDB{}, &SQLLockProviderConfig{
		Owner: "a",
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	_, err = p.AcquireLease(context.Background(), "k")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !gerrors.Is(err, gerrors.Database) {
		t.Fatalf("expected database error, got %v", err)
	}
	if !strings.Contains(err.Error(), "rows affected") {
		t.Fatalf("expected rows affected context, got %v", err)
	}
}

func TestSQLLockProvider_AcquireLeaseMapsContextErrorFromUpdate(t *testing.T) {
	p, err := NewSQLLockProvider(&ctxErrOnAcquireUpdateDB{}, &SQLLockProviderConfig{
		Owner: "a",
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()

	_, err = p.AcquireLease(ctx, "k")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !gerrors.Is(err, gerrors.Timeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if gerrors.Is(err, gerrors.Database) {
		t.Fatalf("expected timeout classification, got database error: %v", err)
	}
}

func TestIsDuplicateLockInsertError_UsesStructuredUniqueViolation(t *testing.T) {
	if !isDuplicateLockInsertError(sqliteUniqueViolationLikeError{
		Code:         19,
		ExtendedCode: 2067,
		msg:          "constraint failed",
	}) {
		t.Fatalf("expected structured sqlite unique violation to be treated as duplicate insert")
	}
	if isDuplicateLockInsertError(sqliteUniqueViolationLikeError{
		Code:         19,
		ExtendedCode: 1299,
		msg:          "NOT NULL constraint failed",
	}) {
		t.Fatalf("expected non-unique sqlite constraint to be rejected")
	}
}

func TestSQLLockProvider_EnsureTableTreatsColumnAppearingAfterAlterErrorAsSuccess(t *testing.T) {
	database := &alterRaceDB{}
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a"})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	if err := p.ensureTable(context.Background()); err != nil {
		t.Fatalf("ensureTable returned error: %v", err)
	}
	if database.queries != 2 {
		t.Fatalf("expected initial probe and retry probe, got %d queries", database.queries)
	}
}

func TestSQLLockProvider_AcquireLeaseUpgradesLegacySQLiteTable(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	_, err := database.Exec(ctx, `
		CREATE TABLE distributed_locks (
			lock_key TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			expires_at_ms BIGINT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create legacy lock table: %v", err)
	}

	p1, err := NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a", RenewInterval: -1})
	if err != nil {
		t.Fatalf("NewSQLLockProvider p1: %v", err)
	}
	lease1, err := p1.AcquireLease(ctx, "legacy")
	if err != nil {
		t.Fatalf("AcquireLease legacy: %v", err)
	}
	hasLeaseToken, err := p1.hasLeaseTokenColumn(ctx)
	if err != nil {
		t.Fatalf("hasLeaseTokenColumn: %v", err)
	}
	if !hasLeaseToken {
		t.Fatalf("expected legacy lock table to be upgraded with lease_token")
	}
	var leaseToken string
	if err := database.QueryRow(ctx, `SELECT lease_token FROM distributed_locks WHERE lock_key = ?`, "legacy").Scan(&leaseToken); err != nil {
		t.Fatalf("query lease token: %v", err)
	}
	if leaseToken == "" {
		t.Fatalf("expected acquired legacy lock to store lease_token")
	}
	lease1.Release()

	p2, err := NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "b", RenewInterval: -1})
	if err != nil {
		t.Fatalf("NewSQLLockProvider p2: %v", err)
	}
	lease2, err := p2.AcquireLease(ctx, "legacy")
	if err != nil {
		t.Fatalf("AcquireLease after release: %v", err)
	}
	lease2.Release()
}

func TestSQLLockProvider_EnsureTableRetriesAfterFailure(t *testing.T) {
	database := &createFailsOnceDB{failingExecDB: &failingExecDB{insertErr: stderrors.New("permission denied")}}
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{Owner: "a"})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	_, err = p.AcquireLease(context.Background(), "k")
	if err == nil || !gerrors.Is(err, gerrors.Database) {
		t.Fatalf("first AcquireLease error = %v, want database error", err)
	}
	_, err = p.AcquireLease(context.Background(), "k")
	if err == nil || !gerrors.Is(err, gerrors.Database) {
		t.Fatalf("second AcquireLease error = %v, want database error", err)
	}
	if database.createCalls != 2 {
		t.Fatalf("expected ensure table to retry after first failure, createCalls=%d", database.createCalls)
	}
}

// TestSQLLockProvider_ExpiresAndCanBeReacquired 验证 SQLLockProvider ExpiresAndCanBeReacquired。
func TestSQLLockProvider_ExpiresAndCanBeReacquired(t *testing.T) {
	database := setupTestDB(t)

	p1, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:         "a",
		TTL:           30 * time.Millisecond,
		PollInterval:  5 * time.Millisecond,
		RenewInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	p2, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:         "b",
		TTL:           30 * time.Millisecond,
		PollInterval:  5 * time.Millisecond,
		RenewInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	release, err := p1.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// 故意不 release，等待 TTL 过期
	_ = release
	time.Sleep(80 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r2, err := p2.Acquire(ctx, "k")
	if err != nil {
		t.Fatalf("Acquire after expire: %v", err)
	}
	r2()
}

func TestSQLLockProvider_RenewsHeldLockUntilRelease(t *testing.T) {
	database := setupTestDB(t)

	p1, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "a",
		TTL:          40 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	p2, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "b",
		TTL:          40 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	release, err := p1.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	time.Sleep(120 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	_, err = p2.Acquire(ctx, "k")
	cancel()
	if err == nil {
		t.Fatalf("expected held lock to be renewed and block reacquire")
	}

	release()
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r2, err := p2.Acquire(ctx, "k")
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	r2()
}

func TestSQLLockProvider_RenewalOutlivesAcquireContext(t *testing.T) {
	database := setupTestDB(t)

	p1, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "a",
		TTL:          40 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	p2, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "b",
		TTL:          40 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 20*time.Millisecond)
	release, err := p1.Acquire(acquireCtx, "k")
	if err != nil {
		cancelAcquire()
		t.Fatalf("Acquire: %v", err)
	}
	cancelAcquire()
	defer release()

	time.Sleep(120 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	_, err = p2.Acquire(ctx, "k")
	cancel()
	if err == nil {
		t.Fatalf("expected lock to remain renewed after acquire context cancellation")
	}
}

func TestSQLLockProvider_ReleaseCancelsInFlightRenewal(t *testing.T) {
	database := newBlockingRenewDB()
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:         "a",
		TTL:           10 * time.Second,
		RenewInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	lease, err := p.AcquireLease(context.Background(), "k")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	select {
	case <-database.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("expected renewal to start")
	}

	done := make(chan struct{})
	go func() {
		lease.Release()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Release did not cancel in-flight renewal promptly")
	}
	select {
	case <-database.renewDone:
	case <-time.After(time.Second):
		t.Fatal("expected renewal exec to observe cancellation")
	}
}

func TestSQLLockProvider_ReleaseDoesNotWaitForeverForUnresponsiveRenewal(t *testing.T) {
	database := newUnresponsiveRenewDB()
	defer close(database.allowRenew)
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:         "a",
		TTL:           20 * time.Millisecond,
		RenewInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	lease, err := p.AcquireLease(context.Background(), "k")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	select {
	case <-database.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("expected renewal to start")
	}

	done := make(chan struct{})
	go func() {
		lease.Release()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Release waited on unresponsive renewal without a bound")
	}
}

// TestSQLLockProvider_ConcurrentAcquireSameKey 验证 SQLLockProvider ConcurrentAcquireSameKey。
func TestSQLLockProvider_ConcurrentAcquireSameKey(t *testing.T) {
	database := setupTestDB(t)
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:        "a",
		TTL:          200 * time.Millisecond,
		PollInterval: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	var inCS int
	var mu sync.Mutex

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			release, err := p.Acquire(ctx, "k")
			if err != nil {
				t.Errorf("Acquire error: %v", err)
				return
			}
			mu.Lock()
			inCS++
			if inCS != 1 {
				t.Errorf("expected mutual exclusion, inCS=%d", inCS)
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inCS--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
}

type renewStep struct {
	affected int64
	err      error
}

// programmableRenewDB 对续租 UPDATE 按预设序列返回结果，用于验证续租重试逻辑。
type programmableRenewDB struct {
	mu       sync.Mutex
	steps    []renewStep
	fallback renewStep
	calls    int
}

func (d *programmableRenewDB) renewCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *programmableRenewDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return &fakeRows{columns: []string{"lock_key", "owner", "lease_token", "expires_at_ms"}}, nil
}

func (d *programmableRenewDB) QueryRow(context.Context, string, ...any) db.IRow { return nil }

func (d *programmableRenewDB) Exec(_ context.Context, query string, _ ...any) (databasesql.Result, error) {
	if strings.Contains(query, "UPDATE") && strings.Contains(query, "expires_at_ms = ?") {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls++
		step := d.fallback
		if len(d.steps) > 0 {
			step = d.steps[0]
			d.steps = d.steps[1:]
		}
		if step.err != nil {
			return nil, step.err
		}
		return fakeSQLResult(step.affected), nil
	}
	return fakeSQLResult(1), nil
}

func (d *programmableRenewDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *programmableRenewDB) BeginTx(context.Context, *databasesql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("not implemented")
}

func (d *programmableRenewDB) Ping(context.Context) error { return nil }

func (d *programmableRenewDB) Close() error { return nil }

func newRetryTestProvider(t *testing.T, database db.IDatabase) *SQLLockProvider {
	t.Helper()
	p, err := NewSQLLockProvider(database, &SQLLockProviderConfig{
		Owner:         "a",
		TTL:           200 * time.Millisecond,
		RenewInterval: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSQLLockProvider: %v", err)
	}
	return p
}

func TestSQLLockProvider_RenewWithRetryRecoversFromTransientError(t *testing.T) {
	database := &programmableRenewDB{
		steps: []renewStep{
			{err: stderrors.New("connection reset")},
			{err: stderrors.New("connection reset")},
			{affected: 1},
		},
	}
	p := newRetryTestProvider(t, database)

	if err := p.renewWithRetry(context.Background(), "k", "token"); err != nil {
		t.Fatalf("expected transient errors to be retried to success, got %v", err)
	}
	if database.renewCalls() != 3 {
		t.Fatalf("expected 3 renew attempts, got %d", database.renewCalls())
	}
}

func TestSQLLockProvider_RenewWithRetryReportsLeaseLostImmediatelyOnConflict(t *testing.T) {
	database := &programmableRenewDB{
		steps:    []renewStep{{affected: 0}}, // owner/token 失配 → Conflict
		fallback: renewStep{affected: 1},
	}
	p := newRetryTestProvider(t, database)

	err := p.renewWithRetry(context.Background(), "k", "token")
	if err == nil {
		t.Fatal("expected lease lost error on owner mismatch")
	}
	if gerrors.Code(err) != gerrors.Conflict {
		t.Fatalf("expected Conflict error code, got %v", gerrors.Code(err))
	}
	if database.renewCalls() != 1 {
		t.Fatalf("expected conflict to fail fast without retry, got %d calls", database.renewCalls())
	}
}

func TestSQLLockProvider_RenewWithRetryGivesUpAfterPersistentTransientError(t *testing.T) {
	database := &programmableRenewDB{
		fallback: renewStep{err: stderrors.New("database unavailable")},
	}
	p := newRetryTestProvider(t, database)

	err := p.renewWithRetry(context.Background(), "k", "token")
	if err == nil {
		t.Fatal("expected lease lost after exhausting retries")
	}
	if gerrors.Code(err) == gerrors.Conflict {
		t.Fatalf("persistent transient error should not be reported as Conflict, got %v", err)
	}
	if database.renewCalls() != maxRenewAttempts {
		t.Fatalf("expected %d attempts, got %d", maxRenewAttempts, database.renewCalls())
	}
}
