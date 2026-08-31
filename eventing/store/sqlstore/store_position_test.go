package sqlstore

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gochen/testkit/require"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
)

func TestSQLEventStore_AppendEvents_RejectsGlobalPositionWithoutUniqueIndex(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	_, err := database.Exec(ctx, `ALTER TABLE event_store ADD COLUMN global_position INTEGER`)
	require.NoError(t, err)
	store := newTestStore(t, database, "event_store")

	err = store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestSQLEventStore_AppendEvents_RejectsCompositeGlobalPositionUniqueIndex(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	_, err := database.Exec(ctx, `ALTER TABLE event_store ADD COLUMN global_position INTEGER`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `CREATE UNIQUE INDEX idx_event_store_global_position_composite ON event_store(global_position, aggregate_type)`)
	require.NoError(t, err)
	store := newTestStore(t, database, "event_store")

	err = store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestSQLEventStore_AppendEvents_RejectsNonUniqueGlobalPositionIndexNamedUnique(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	_, err := database.Exec(ctx, `ALTER TABLE event_store ADD COLUMN global_position INTEGER`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `CREATE INDEX idx_event_store_global_position_unique ON event_store(global_position)`)
	require.NoError(t, err)
	store := newTestStore(t, database, "event_store")

	err = store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestSQLEventStore_SupportsGlobalPositionRequiresUniqueIndex(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	store := newTestStore(t, database, "event_store")
	ok, err := store.supportsGlobalPositionWithError(context.Background(), database)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSQLEventStore_AppendEvents_RejectsGlobalPositionWithoutAllocator(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	_, err := database.Exec(ctx, `ALTER TABLE event_store ADD COLUMN global_position INTEGER`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `CREATE UNIQUE INDEX idx_event_store_global_position_unique ON event_store(global_position)`)
	require.NoError(t, err)
	store := newTestStore(t, database, "event_store")

	err = store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestSQLEventStore_AppendEvents_AcceptsQualifiedSQLiteGlobalPositionUniqueIndex(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	addGlobalPositionColumnAndIndex(t, database, "main.event_store")
	store := newTestStore(t, database, "main.event_store")

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-qualified-position", 1, nil),
	}), 0))

	row := database.QueryRow(ctx, `SELECT global_position FROM event_store WHERE id = ?`, "event-qualified-position")
	var position int64
	require.NoError(t, row.Scan(&position))
	require.Positive(t, position)
}

func TestSQLEventStore_GlobalPositionUniqueIndexProbeUsesQualifiedTableParts(t *testing.T) {
	store := &SQLEventStore[int64]{tableName: "public.event_store"}

	mysqlDB := &globalPositionIndexProbeDB{dialect: "mysql"}
	ok, err := store.hasMySQLGlobalPositionUniqueIndex(context.Background(), mysqlDB)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []any{"public", "event_store"}, mysqlDB.args)

	postgresDB := &globalPositionIndexProbeDB{dialect: "postgres"}
	ok, err = store.hasPostgresGlobalPositionUniqueIndex(context.Background(), postgresDB)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []any{"event_store", "public"}, postgresDB.args)
}

type globalPositionIndexProbeDB struct {
	db.IDatabase
	dialect string
	args    []any
}

func (d *globalPositionIndexProbeDB) DialectName() string { return d.dialect }

func (d *globalPositionIndexProbeDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	d.args = append([]any(nil), args...)
	return globalPositionProbeRow{}
}

type globalPositionProbeRow struct{}

func (globalPositionProbeRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if count, ok := dest[0].(*int); ok {
			*count = 1
		}
	}
	return nil
}

func (globalPositionProbeRow) Err() error { return nil }

type globalPositionProbeErrorDB struct {
	db.IDatabase
	err error
}

func (d globalPositionProbeErrorDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "WHERE 1 = 0") {
		return nil, d.err
	}
	return d.IDatabase.Query(ctx, query, args...)
}

type globalPositionProbeCountingDB struct {
	db.IDatabase
	probes atomic.Int64
}

func (d *globalPositionProbeCountingDB) DialectName() string {
	if provider, ok := d.IDatabase.(db.IDialectNameProvider); ok {
		return provider.DialectName()
	}
	return ""
}

func (d *globalPositionProbeCountingDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "WHERE 1 = 0") {
		d.probes.Add(1)
	}
	return d.IDatabase.Query(ctx, query, args...)
}

func TestSQLEventStore_AppendEvents_FailsWhenGlobalPositionProbeFails(t *testing.T) {
	base := setupTestDB(t)
	store := newTestStore(t, base, "event_store")
	probeErr := stderrors.New("probe failed")

	err := store.AppendEventsWithDB(context.Background(), globalPositionProbeErrorDB{IDatabase: base, err: probeErr}, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.Database), "expected database error, got %v", err)
}

func TestSQLEventStore_StreamGlobalPositionMissingProbeUsesShortNegativeCache(t *testing.T) {
	base := setupTestDB(t)
	countingDB := &globalPositionProbeCountingDB{IDatabase: base}
	store := newTestStore(t, countingDB, "event_store")
	ctx := context.Background()

	ok, err := store.supportsGlobalPositionForStream(ctx, countingDB)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.supportsGlobalPositionForStream(ctx, countingDB)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, int64(1), countingDB.probes.Load())

	addGlobalPositionColumnAndIndex(t, base)

	ok, err = store.supportsGlobalPositionWithError(ctx, countingDB)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(2), countingDB.probes.Load())

	store.globalPositionSupport.Store(0)
	ok, err = store.supportsGlobalPositionWithError(ctx, countingDB)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(3), countingDB.probes.Load())
}

func TestSQLEventStore_AppendGlobalPositionMissingProbeUsesShortNegativeCache(t *testing.T) {
	base := setupTestDB(t)
	countingDB := &globalPositionProbeCountingDB{IDatabase: base}
	store := newTestStore(t, countingDB, "event_store")
	ctx := context.Background()

	require.NoError(t, store.AppendEventsWithDB(ctx, countingDB, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-no-position-1", 1, nil),
	}), 0))
	require.NoError(t, store.AppendEventsWithDB(ctx, countingDB, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{
		makeEvent(2, "TestAggregate", "event-no-position-2", 1, nil),
	}), 0))
	require.Equal(t, int64(1), countingDB.probes.Load())

	expireGlobalPositionProbe(store)
	addGlobalPositionColumnAndIndex(t, base)

	require.NoError(t, store.AppendEventsWithDB(ctx, countingDB, "TestAggregate", 3, toStorableEvents([]eventing.Event[int64]{
		makeEvent(3, "TestAggregate", "event-positioned", 1, nil),
	}), 0))
	require.Equal(t, int64(2), countingDB.probes.Load())

	row := base.QueryRow(ctx, `SELECT global_position FROM event_store WHERE id = ?`, "event-positioned")
	var position int64
	require.NoError(t, row.Scan(&position))
	require.Positive(t, position)
}

func TestGlobalPositionRetryDelay_AddsJitter(t *testing.T) {
	old := globalPositionRetryFloat64
	t.Cleanup(func() { globalPositionRetryFloat64 = old })

	globalPositionRetryFloat64 = func() float64 { return 0 }
	minDelay := globalPositionRetryDelay(3)

	globalPositionRetryFloat64 = func() float64 { return 1 }
	maxDelay := globalPositionRetryDelay(3)

	require.Equal(t, 4*time.Millisecond, minDelay)
	require.Equal(t, 12*time.Millisecond, maxDelay)
}

func TestSQLEventStore_StreamHorizonCachesContiguousResultAndInvalidatesOnAppend(t *testing.T) {
	base := setupTestDB(t)
	ctx := context.Background()
	addGlobalPositionColumnAndIndex(t, base)

	countingDB := &horizonCountingDB{IDatabase: base}
	store := newTestStore(t, countingDB, "event_store")

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, nil),
	}), 0))

	_, err := store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	_, err = store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), countingDB.horizonQueries.Load())

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{
		makeEvent(2, "TestAggregate", "event-2", 1, nil),
	}), 0))

	_, err = store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), countingDB.horizonQueries.Load())
	require.Equal(t, int64(0), countingDB.countQueries.Load())
}

func TestSQLEventStore_StreamHorizonCachesGapResult(t *testing.T) {
	base := setupTestDB(t)
	ctx := context.Background()
	addGlobalPositionColumnAndIndex(t, base)

	countingDB := &horizonCountingDB{IDatabase: base}
	store := newTestStore(t, countingDB, "event_store")
	ts := time.Now()

	_, err := base.Exec(ctx,
		`INSERT INTO event_store (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"evt-pos-1", "TestEvent", 1, "TestAggregate", 1, 1, 1, ts, "{}", "{}")
	require.NoError(t, err)
	_, err = base.Exec(ctx,
		`INSERT INTO event_store (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"evt-pos-3", "TestEvent", 3, "TestAggregate", 1, 1, 3, ts, "{}", "{}")
	require.NoError(t, err)

	_, err = store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	_, err = store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), countingDB.horizonQueries.Load())
	require.Equal(t, int64(1), countingDB.gapQueries.Load())
	require.Equal(t, int64(0), countingDB.countQueries.Load())
}

func TestSQLEventStore_StreamHorizonRescansFromPreviousHorizon(t *testing.T) {
	base := setupTestDB(t)
	ctx := context.Background()
	addGlobalPositionColumnAndIndex(t, base)
	countingDB := &horizonCountingDB{IDatabase: base}
	store := newTestStore(t, countingDB, "event_store")

	for i := int64(1); i <= 3; i++ {
		require.NoError(t, store.AppendEvents(ctx, "TestAggregate", i, toStorableEvents([]eventing.Event[int64]{
			makeEvent(i, "TestAggregate", fmt.Sprintf("event-%d", i), 1, nil),
		}), 0))
	}
	_, err := store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), countingDB.incrementalGapQueries.Load())

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 4, toStorableEvents([]eventing.Event[int64]{
		makeEvent(4, "TestAggregate", "event-4", 1, nil),
	}), 0))
	_, err = store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), countingDB.incrementalGapQueries.Load())
}

func TestSQLEventStore_StreamHorizonDoesNotSkipLeadingGap(t *testing.T) {
	base := setupTestDB(t)
	ctx := context.Background()
	addGlobalPositionColumnAndIndex(t, base)
	store := newTestStore(t, base, "event_store")
	ts := time.Now()

	_, err := base.Exec(ctx,
		`INSERT INTO event_store (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"evt-pos-2", "TestEvent", 2, "TestAggregate", 1, 1, 2, ts, "{}", "{}")
	require.NoError(t, err)

	hasHorizon, horizon, err := store.streamHorizon(ctx, base)
	require.NoError(t, err)
	require.True(t, hasHorizon)
	require.Equal(t, int64(0), horizon)

	result, err := store.StreamEvents(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, result.Events)
	require.False(t, result.HasMore)
	require.Empty(t, result.NextCursor)
}

type horizonCountingDB struct {
	db.IDatabase
	horizonQueries        atomic.Int64
	gapQueries            atomic.Int64
	incrementalGapQueries atomic.Int64
	countQueries          atomic.Int64
}

func (d *horizonCountingDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if strings.Contains(query, "MIN(global_position)") &&
		strings.Contains(query, "MAX(global_position)") {
		d.horizonQueries.Add(1)
	}
	if strings.Contains(query, "COUNT(global_position)") {
		d.countQueries.Add(1)
	}
	if strings.Contains(query, "NOT EXISTS") &&
		strings.Contains(query, "global_position = t1.global_position + 1") {
		d.gapQueries.Add(1)
		if strings.Contains(query, "t1.global_position >= ?") {
			d.incrementalGapQueries.Add(1)
		}
	}
	return d.IDatabase.QueryRow(ctx, query, args...)
}

func (d *horizonCountingDB) DialectName() string {
	if provider, ok := d.IDatabase.(db.IDialectNameProvider); ok {
		return provider.DialectName()
	}
	return ""
}

func addGlobalPositionColumnAndIndex(t *testing.T, database db.IDatabase, storeNames ...string) {
	t.Helper()
	ctx := context.Background()
	_, err := database.Exec(ctx, `ALTER TABLE event_store ADD COLUMN global_position INTEGER`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `CREATE UNIQUE INDEX idx_event_store_global_position_unique ON event_store(global_position)`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS event_store_positions (
			store_name TEXT PRIMARY KEY,
			next_position INTEGER NOT NULL CHECK (next_position > 0)
		)
	`)
	require.NoError(t, err)
	if len(storeNames) == 0 {
		storeNames = []string{"event_store"}
	}
	for _, storeName := range storeNames {
		_, err = database.Exec(ctx, `INSERT OR IGNORE INTO event_store_positions (store_name, next_position) VALUES (?, 1)`, storeName)
		require.NoError(t, err)
	}
}

func expireGlobalPositionProbe[ID comparable](store *SQLEventStore[ID]) {
	store.globalPositionMu.Lock()
	store.globalPositionProbe.expiresAt = time.Now().Add(-time.Millisecond)
	store.globalPositionMu.Unlock()
}
