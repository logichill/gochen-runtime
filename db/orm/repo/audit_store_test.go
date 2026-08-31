package repo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gochen/testkit/require"
	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/safeident"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/errors"
	"gochen/gen"
)

func testAuditIDGenerator(tb testing.TB) gen.IGenerator[int64] {
	tb.Helper()
	generator, err := gen.NewSnowflakeGenerator(1, 1)
	require.NoError(tb, err)
	return generator
}

func setupAuditStore(tb testing.TB, tableName string) (*AuditStore, db.IDatabase) {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	_, err = database.Exec(ctx, `
CREATE TABLE `+safeAuditTestTableName(tb, tableName)+` (
  id INTEGER PRIMARY KEY,
  resource_kind TEXT,
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  operator TEXT NOT NULL,
  timestamp DATETIME NOT NULL,
  changes TEXT NOT NULL,
  metadata TEXT NOT NULL
);`)
	require.NoError(tb, err)

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)
	store, err := NewAuditStore(ormEngine, tableName, testAuditIDGenerator(tb))
	require.NoError(tb, err)
	return store, database
}

func setupLegacyAuditTable(tb testing.TB, tableName string) (orm.IOrm, db.IDatabase) {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	_, err = database.Exec(ctx, `
CREATE TABLE `+safeAuditTestTableName(tb, tableName)+` (
  id INTEGER PRIMARY KEY,
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  operator TEXT NOT NULL,
  timestamp DATETIME NOT NULL,
  changes TEXT NOT NULL,
  metadata TEXT NOT NULL
);`)
	require.NoError(tb, err)

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)
	return ormEngine, database
}

func setupScopedAuditStore(tb testing.TB, tableName string) (*AuditStore, db.IDatabase) {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	_, err = database.Exec(ctx, `
CREATE TABLE `+safeAuditTestTableName(tb, tableName)+` (
  id INTEGER PRIMARY KEY,
  resource_kind TEXT,
  entity_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  operator TEXT NOT NULL,
  timestamp DATETIME NOT NULL,
  changes TEXT NOT NULL,
  metadata TEXT NOT NULL
);`)
	require.NoError(tb, err)

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)
	store, err := NewAuditStore(ormEngine, tableName, testAuditIDGenerator(tb))
	require.NoError(tb, err)
	return store, database
}

func safeAuditTestTableName(tb testing.TB, tableName string) string {
	tb.Helper()
	if !safeident.IsSafeIdentifier(tableName) {
		tb.Fatalf("unsafe audit test table name: %q", tableName)
	}
	return tableName
}

func TestNewAuditStore_RejectsUnsafeTableName(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	ormEngine, err := lite.New(database)
	require.NoError(t, err)

	_, err = NewAuditStore(ormEngine, `audit_records; DROP TABLE users`, testAuditIDGenerator(t))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestNewAuditStore_RequiresResourceKindColumn(t *testing.T) {
	ormEngine, _ := setupLegacyAuditTable(t, "audit_records")

	_, err := NewAuditStore(ormEngine, "audit_records", testAuditIDGenerator(t))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

type stubInt64Generator struct{}

func (s *stubInt64Generator) Next() (int64, error) { return 1, nil }

func TestNewAuditStore_IDGeneratorBoundaries(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	ormEngine, err := lite.New(database)
	require.NoError(t, err)

	cases := map[string]gen.IGenerator[int64]{
		"nil":       nil,
		"typed nil": (*stubInt64Generator)(nil),
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewAuditStore(ormEngine, "audit_records", g)
			require.Error(t, err)
			require.True(t, errors.Is(err, errors.InvalidInput))
		})
	}
}

// TestAuditStore_SaveAuditRecord_ValidatesInput 验证 AuditStore SaveAuditRecord ValidatesInput。
func TestAuditStore_SaveAuditRecord_ValidatesInput(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")

	t.Run("empty entity id", func(t *testing.T) {
		_, err := store.SaveAuditRecord(context.Background(), audited.AuditRecord{
			EntityID:  "",
			Operation: audited.AuditOpCreate,
			Operator:  "alice",
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.InvalidInput))
	})

	t.Run("empty operation", func(t *testing.T) {
		_, err := store.SaveAuditRecord(context.Background(), audited.AuditRecord{
			EntityID:  "1",
			Operation: "",
			Operator:  "alice",
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.InvalidInput))
	})

	t.Run("empty operator", func(t *testing.T) {
		_, err := store.SaveAuditRecord(context.Background(), audited.AuditRecord{
			EntityID:  "1",
			Operation: audited.AuditOpCreate,
			Operator:  "",
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.InvalidInput))
	})
}

// TestAuditStore_SaveAuditRecord_AndList 验证 AuditStore SaveAuditRecord AndList。
func TestAuditStore_SaveAuditRecord_AndList(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")

	ctx := context.Background()
	id, err := store.SaveAuditRecord(ctx, audited.AuditRecord{
		EntityID:  "1",
		Operation: audited.AuditOpCreate,
		Operator:  "alice",
		Changes:   json.RawMessage(`{"title":"hello"}`),
		Metadata:  json.RawMessage(`{"source":"unit_test"}`),
	})
	require.NoError(t, err)
	require.NotZero(t, id)

	records, err := store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, id, records[0].ID)
	require.Equal(t, "1", records[0].EntityID)
	require.Equal(t, audited.AuditOpCreate, records[0].Operation)
	require.Equal(t, "alice", records[0].Operator)
	require.False(t, records[0].Timestamp.IsZero())
	var changes map[string]string
	require.NoError(t, json.Unmarshal(records[0].Changes, &changes))
	require.Equal(t, "hello", changes["title"])
	var metadata map[string]string
	require.NoError(t, json.Unmarshal(records[0].Metadata, &metadata))
	require.Equal(t, "unit_test", metadata["source"])
}

func TestAuditStore_ListAuditRecordsByEntity_FiltersByResourceKindWhenColumnExists(t *testing.T) {
	store, _ := setupScopedAuditStore(t, "audit_records")

	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	_, err := store.SaveAuditRecord(ctx, audited.AuditRecord{
		ID:           10,
		ResourceKind: "sales_orders",
		EntityID:     "1",
		Operation:    audited.AuditOpCreate,
		Operator:     "alice",
		Timestamp:    t1,
	})
	require.NoError(t, err)
	_, err = store.SaveAuditRecord(ctx, audited.AuditRecord{
		ID:           20,
		ResourceKind: "customers",
		EntityID:     "1",
		Operation:    audited.AuditOpCreate,
		Operator:     "bob",
		Timestamp:    t2,
	})
	require.NoError(t, err)

	all, err := store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, all, 2)

	scopedCtx := audited.WithAuditResourceKind(ctx, "sales_orders")
	records, err := store.ListAuditRecordsByEntity(scopedCtx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(10), records[0].ID)
	require.Equal(t, "sales_orders", records[0].ResourceKind)
}

// TestAuditStore_ListAuditRecordsByEntity_PaginationAndOrdering 验证 AuditStore ListAuditRecordsByEntity PaginationAndOrdering。
func TestAuditStore_ListAuditRecordsByEntity_PaginationAndOrdering(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")

	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	// timestamp desc, id desc
	_, err := store.SaveAuditRecord(ctx, audited.AuditRecord{ID: 10, EntityID: "1", Operation: "A", Operator: "alice", Timestamp: t1})
	require.NoError(t, err)
	_, err = store.SaveAuditRecord(ctx, audited.AuditRecord{ID: 20, EntityID: "1", Operation: "B", Operator: "alice", Timestamp: t2})
	require.NoError(t, err)
	_, err = store.SaveAuditRecord(ctx, audited.AuditRecord{ID: 30, EntityID: "1", Operation: "C", Operator: "alice", Timestamp: t2})
	require.NoError(t, err)

	all, err := store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, int64(30), all[0].ID)
	require.Equal(t, int64(20), all[1].ID)
	require.Equal(t, int64(10), all[2].ID)

	page, err := store.ListAuditRecordsByEntity(ctx, "1", 1, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, int64(20), page[0].ID)

	// negative offset coerces to 0; non-positive limit defaults to 100.
	page2, err := store.ListAuditRecordsByEntity(ctx, "1", -10, 0)
	require.NoError(t, err)
	require.Len(t, page2, 3)
}

// TestAuditStore_ListAuditRecordsByEntity_InvalidJSONReturnsInternal 验证 AuditStore ListAuditRecordsByEntity InvalidJSONReturnsInternal。
func TestAuditStore_ListAuditRecordsByEntity_InvalidJSONReturnsInternal(t *testing.T) {
	store, database := setupAuditStore(t, "audit_records")

	ctx := context.Background()
	_, err := database.Exec(ctx,
		"INSERT INTO audit_records (id, resource_kind, entity_id, operation, operator, timestamp, changes, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		int64(1), "", "1", "CREATE", "alice", time.Now(), "{bad_json", "{}",
	)
	require.NoError(t, err)

	_, err = store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Internal))
}

func TestAuditStore_SaveAuditRecord_UsesTxSessionFromContext(t *testing.T) {
	store, database := setupAuditStore(t, "audit_records")

	ormEngine, err := lite.New(database)
	require.NoError(t, err)
	session, err := ormEngine.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = session.Rollback() }()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	txCtx, err := orm.WithTxSession(ctx, session, true)
	require.NoError(t, err)
	_, err = store.SaveAuditRecord(txCtx, audited.AuditRecord{
		EntityID:  "1",
		Operation: audited.AuditOpCreate,
		Operator:  "alice",
	})
	require.NoError(t, err)

	require.NoError(t, session.Rollback())

	records, err := store.ListAuditRecordsByEntity(context.Background(), "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, records, 0, "rollback should discard audit record written in tx")
}

// --- 批量写入（IBatchAuditStore） ---

func TestAuditStore_SaveAuditRecords_WritesWholeBatch(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")
	ctx := context.Background()

	records := make([]audited.AuditRecord, 0, 3)
	for i := 0; i < 3; i++ {
		records = append(records, audited.AuditRecord{
			EntityID:  "1",
			Operation: audited.AuditOpCreate,
			Operator:  "alice",
			Changes:   json.RawMessage(`{"title":"hello"}`),
		})
	}
	require.NoError(t, store.SaveAuditRecords(ctx, records))

	stored, err := store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, stored, 3)
	for _, record := range stored {
		require.NotZero(t, record.ID, "批量写入同样要由存储层补齐记录 ID")
		require.False(t, record.Timestamp.IsZero())
		require.Equal(t, "alice", record.Operator)
	}
}

func TestAuditStore_SaveAuditRecords_EmptyBatchIsNoop(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")
	require.NoError(t, store.SaveAuditRecords(context.Background(), nil))
}

// 审计是安全证据：任一条不合法即整批失败，绝不部分留痕。
func TestAuditStore_SaveAuditRecords_RejectsWholeBatchOnInvalidRecord(t *testing.T) {
	store, _ := setupAuditStore(t, "audit_records")
	ctx := context.Background()

	err := store.SaveAuditRecords(ctx, []audited.AuditRecord{
		{EntityID: "1", Operation: audited.AuditOpCreate, Operator: "alice"},
		{EntityID: "2", Operation: audited.AuditOpCreate, Operator: ""}, // operator 缺失
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	stored, err := store.ListAuditRecordsByEntity(ctx, "1", 0, 10)
	require.NoError(t, err)
	require.Len(t, stored, 0, "整批拒绝后不得留下任何一条记录")
}
