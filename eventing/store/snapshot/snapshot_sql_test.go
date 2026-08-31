package snapshot

import (
	"context"
	"database/sql"
	stderrors "errors"
	"testing"

	snapcore "gochen/eventing/store/snapshot"
	"time"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/errors"
)

func TestSQLStore_RejectsUnsafeTableName(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()

	store := NewSQLStore(database, "event_snapshots; DROP TABLE event_snapshots")
	err = store.SaveSnapshot(context.Background(), snapcore.Snapshot[int64]{
		AggregateID:   1,
		AggregateType: "counter",
		Version:       1,
		Data:          []byte(`{}`),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestIsSnapshotDuplicateKeyError_UsesStructuredUniqueViolationClassifier(t *testing.T) {
	require.True(t, isSnapshotDuplicateKeyError(stderrors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")))
	require.False(t, isSnapshotDuplicateKeyError(stderrors.New("NOT NULL constraint failed: event_snapshots.data")))
}

type rowsAffectedErrSnapshotDB struct {
	db.IDatabase
}

func (d rowsAffectedErrSnapshotDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.IDatabase.Exec(ctx, query, args...)
	if err == nil && query != "" {
		return rowsAffectedErrSnapshotResult{Result: result}, nil
	}
	return result, err
}

type rowsAffectedErrSnapshotResult struct {
	sql.Result
}

func (r rowsAffectedErrSnapshotResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unavailable")
}

func TestSQLStore_SaveSnapshot_FailsWhenRowsAffectedUnavailable(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()

	_, err = database.Exec(context.Background(), `
		CREATE TABLE event_snapshots (
			aggregate_type TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			data BLOB NOT NULL,
			timestamp DATETIME NOT NULL,
			metadata TEXT,
			PRIMARY KEY (aggregate_type, aggregate_id)
		)
	`)
	require.NoError(t, err)

	store := NewSQLStore(rowsAffectedErrSnapshotDB{IDatabase: database}, "event_snapshots")
	err = store.SaveSnapshot(context.Background(), snapcore.Snapshot[int64]{
		AggregateID:   1,
		AggregateType: "counter",
		Version:       1,
		Data:          []byte(`{}`),
		Timestamp:     time.Now(),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Database))
}

func TestSQLStore_CleanupSnapshots_FailsWhenRowsAffectedUnavailable(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()

	_, err = database.Exec(context.Background(), `
		CREATE TABLE event_snapshots (
			aggregate_type TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			data BLOB NOT NULL,
			timestamp DATETIME NOT NULL,
			metadata TEXT,
			PRIMARY KEY (aggregate_type, aggregate_id)
		)
	`)
	require.NoError(t, err)
	_, err = database.Exec(context.Background(), `
		INSERT INTO event_snapshots (aggregate_type, aggregate_id, version, data, timestamp, metadata)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "counter", "1", 1, []byte(`{}`), time.Now().Add(-2*time.Hour), `{}`)
	require.NoError(t, err)

	store := NewSQLStore(rowsAffectedErrSnapshotDB{IDatabase: database}, "event_snapshots")
	err = store.CleanupSnapshots(context.Background(), time.Hour)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Database))
}

func TestSQLStore_FindSnapshotReturnsInvalidMetadataError(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()
	createSnapshotTable(t, database)

	_, err = database.Exec(context.Background(), `
		INSERT INTO event_snapshots (aggregate_type, aggregate_id, version, data, timestamp, metadata)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "counter", "1", 1, []byte(`{}`), time.Now(), `{invalid`)
	require.NoError(t, err)

	store := NewSQLStore(database, "event_snapshots")
	_, err = store.FindSnapshot(context.Background(), "counter", 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Internal))
}

func TestSQLStore_SaveSnapshot_DoesNotOverwriteNewerVersion(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()
	createSnapshotTable(t, database)

	store := NewSQLStore(database, "event_snapshots")
	now := time.Now()
	require.NoError(t, store.SaveSnapshot(context.Background(), snapcore.Snapshot[int64]{
		AggregateID:   1,
		AggregateType: "counter",
		Version:       10,
		Data:          []byte(`{"version":10}`),
		Timestamp:     now,
	}))
	require.NoError(t, store.SaveSnapshot(context.Background(), snapcore.Snapshot[int64]{
		AggregateID:   1,
		AggregateType: "counter",
		Version:       9,
		Data:          []byte(`{"version":9}`),
		Timestamp:     now.Add(time.Second),
	}))

	got, err := store.FindSnapshot(context.Background(), "counter", 1)
	require.NoError(t, err)
	require.Equal(t, uint64(10), got.Version)
	require.JSONEq(t, `{"version":10}`, string(got.Data))
}

func TestSQLStore_ListSnapshotsReturnsInvalidMetadataError(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:"})
	require.NoError(t, err)
	defer database.Close()
	createSnapshotTable(t, database)

	_, err = database.Exec(context.Background(), `
		INSERT INTO event_snapshots (aggregate_type, aggregate_id, version, data, timestamp, metadata)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "counter", "1", 1, []byte(`{}`), time.Now(), `{invalid`)
	require.NoError(t, err)

	store := NewSQLStore(database, "event_snapshots")
	_, err = store.ListSnapshots(context.Background(), "counter", 10)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Internal))
}

func createSnapshotTable(t *testing.T, database db.IDatabase) {
	t.Helper()
	_, err := database.Exec(context.Background(), `
		CREATE TABLE event_snapshots (
			aggregate_type TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			data BLOB NOT NULL,
			timestamp DATETIME NOT NULL,
			metadata TEXT,
			PRIMARY KEY (aggregate_type, aggregate_id)
		)
	`)
	require.NoError(t, err)
}
