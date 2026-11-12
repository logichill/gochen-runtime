package sqlstore_test

import (
	"context"
	"database/sql"
	stderrors "errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/auth/sqlstore"
	"gochen-runtime/db/sql/stdsql"
	auth "gochen/auth"
	"gochen/db"
	"gochen/errors"
)

func TestPolicySnapshotStore_SaveLoadListCleanup(t *testing.T) {
	database := setupAuthzSQLDB(t)
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	require.NoError(t, err)

	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v1",
		Timestamp: time.Now().Add(-2 * time.Hour),
		Metadata:  map[string]any{"source": "seed"},
	}))
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "tenant:1",
		Version:   "v2",
		Timestamp: time.Now(),
	}))

	loaded, err := store.LoadPolicySnapshot(context.Background(), "default")
	require.NoError(t, err)
	require.Equal(t, "v1", loaded.Version)
	require.Equal(t, "seed", loaded.Metadata["source"])

	list, err := store.ListPolicySnapshots(context.Background(), "tenant:", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "tenant:1", list[0].Key)

	require.NoError(t, store.CleanupPolicySnapshots(context.Background(), time.Hour))
	_, err = store.LoadPolicySnapshot(context.Background(), "default")
	require.True(t, errors.Is(err, errors.NotFound))
}

func TestPolicySnapshotStore_RejectsUnsafeTableName(t *testing.T) {
	database := setupAuthzSQLDB(t)
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots; DROP TABLE authz_policy_snapshots")
	require.Error(t, err)
	require.Nil(t, store)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

type rowsAffectedErrPolicyDB struct {
	db.IDatabase
}

func (d rowsAffectedErrPolicyDB) DialectName() string {
	if provider, ok := d.IDatabase.(db.IDialectNameProvider); ok {
		return provider.DialectName()
	}
	return ""
}

func (d rowsAffectedErrPolicyDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.IDatabase.Exec(ctx, query, args...)
	if err == nil {
		return rowsAffectedErrPolicyResult{Result: result}, nil
	}
	return result, err
}

type rowsAffectedErrPolicyResult struct {
	sql.Result
}

func (r rowsAffectedErrPolicyResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unavailable")
}

func TestPolicySnapshotStore_SavePolicySnapshot_DoesNotRequireRowsAffected(t *testing.T) {
	database := setupAuthzSQLDB(t)
	store, err := sqlstore.NewPolicySnapshotStore(rowsAffectedErrPolicyDB{IDatabase: database}, "authz_policy_snapshots")
	require.NoError(t, err)

	err = store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v1",
		Timestamp: time.Now(),
	})
	require.NoError(t, err)
}

func TestPolicySnapshotStore_SavePolicySnapshot_RowsAffectedUnavailableUpdatesExisting(t *testing.T) {
	database := &unknownDialectPolicyDB{IDatabase: rowsAffectedErrPolicyDB{IDatabase: setupAuthzSQLDB(t)}}
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v1",
		Timestamp: now,
	}))
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v2",
		Timestamp: now.Add(time.Minute),
	}))

	loaded, err := store.LoadPolicySnapshot(context.Background(), "default")
	require.NoError(t, err)
	require.Equal(t, "v2", loaded.Version)
}

type unknownDialectPolicyDB struct {
	db.IDatabase
	mu      sync.Mutex
	queries []string
}

func (d *unknownDialectPolicyDB) DialectName() string { return "unknown" }

func (d *unknownDialectPolicyDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.mu.Lock()
	d.queries = append(d.queries, query)
	d.mu.Unlock()
	return d.IDatabase.Exec(ctx, query, args...)
}

func (d *unknownDialectPolicyDB) hasExec(fragment string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, query := range d.queries {
		if strings.Contains(query, fragment) {
			return true
		}
	}
	return false
}

func TestPolicySnapshotStore_SavePolicySnapshot_UnknownDialectUsesCompatUpsert(t *testing.T) {
	database := &unknownDialectPolicyDB{IDatabase: setupAuthzSQLDB(t)}
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v1",
		Timestamp: now,
	}))
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v2",
		Timestamp: now.Add(time.Minute),
	}))

	require.True(t, database.hasExec("UPDATE authz_policy_snapshots"))
	require.True(t, database.hasExec("INSERT INTO authz_policy_snapshots"))
	require.False(t, database.hasExec("ON DUPLICATE KEY UPDATE"))

	loaded, err := store.LoadPolicySnapshot(context.Background(), "default")
	require.NoError(t, err)
	require.Equal(t, "v2", loaded.Version)
}

func TestPolicySnapshotStore_SavePolicySnapshot_CompatUniqueViolationRetriesUpdate(t *testing.T) {
	database := &uniqueConflictPolicyDB{IDatabase: setupAuthzSQLDB(t)}
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, store.SavePolicySnapshot(context.Background(), auth.PolicySnapshot{
		Key:       "default",
		Version:   "v2",
		Timestamp: now,
	}))

	require.Equal(t, 1, database.insertConflicts)
	require.GreaterOrEqual(t, database.updateCalls, 2)
	loaded, err := store.LoadPolicySnapshot(context.Background(), "default")
	require.NoError(t, err)
	require.Equal(t, "v2", loaded.Version)
}

func setupAuthzSQLDB(t *testing.T) db.IDatabase {
	t.Helper()

	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)

	_, err = database.Exec(context.Background(), `
		CREATE TABLE authz_policy_snapshots (
			snapshot_key TEXT PRIMARY KEY,
			version TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			metadata TEXT NOT NULL
		);
	`)
	require.NoError(t, err)
	return database
}

type uniqueConflictPolicyDB struct {
	db.IDatabase
	insertConflicts int
	updateCalls     int
}

func (d *uniqueConflictPolicyDB) DialectName() string { return "unknown" }

func (d *uniqueConflictPolicyDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	normalized := strings.ToUpper(strings.TrimSpace(query))
	if strings.HasPrefix(normalized, "UPDATE AUTHZ_POLICY_SNAPSHOTS") {
		d.updateCalls++
		if d.updateCalls == 1 {
			return d.IDatabase.Exec(ctx, query, args...)
		}
	}
	if strings.HasPrefix(normalized, "INSERT INTO AUTHZ_POLICY_SNAPSHOTS") {
		d.insertConflicts++
		_, seedErr := d.IDatabase.Exec(ctx,
			`INSERT INTO authz_policy_snapshots (snapshot_key, version, timestamp, metadata) VALUES (?, ?, ?, ?)`,
			"default", "v1", time.Now().Add(-time.Minute), "{}",
		)
		if seedErr != nil {
			return nil, seedErr
		}
		return nil, stderrors.New("UNIQUE constraint failed: authz_policy_snapshots.snapshot_key")
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

func TestPolicySnapshotStore_ListPolicySnapshots_WildcardEscaping(t *testing.T) {
	database := setupAuthzSQLDB(t)
	store, err := sqlstore.NewPolicySnapshotStore(database, "authz_policy_snapshots")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, store.SavePolicySnapshot(ctx, auth.PolicySnapshot{
		Key:       "tenant_1:main",
		Version:   "v1",
		Timestamp: time.Now(),
	}))
	require.NoError(t, store.SavePolicySnapshot(ctx, auth.PolicySnapshot{
		Key:       "tenantA1:main",
		Version:   "v1",
		Timestamp: time.Now(),
	}))
	require.NoError(t, store.SavePolicySnapshot(ctx, auth.PolicySnapshot{
		Key:       "tenant%2:main",
		Version:   "v1",
		Timestamp: time.Now(),
	}))

	list, err := store.ListPolicySnapshots(ctx, "tenant_1", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "tenant_1:main", list[0].Key)

	list, err = store.ListPolicySnapshots(ctx, "tenant%", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "tenant%2:main", list[0].Key)
}
