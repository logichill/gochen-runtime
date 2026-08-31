package sqlstore

import (
	"context"
	"database/sql"
	stderrors "errors"
	"strings"
	"testing"
	"time"

	"gochen/testkit/require"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
	"gochen/eventing/projection"
)

func TestSQLCheckpointStore_InvalidTableNameRejectedAcrossSQLPaths(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints;DROP")
	checkpoint := projection.NewCheckpoint("invalid-table", 1, "event-1", time.Now())

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "Load",
			run: func() error {
				_, err := store.Load(ctx, checkpoint.ProjectionName)
				return err
			},
		},
		{
			name: "Save",
			run: func() error {
				return store.SaveWithDB(ctx, database, checkpoint.Clone())
			},
		},
		{
			name: "SaveWithDB",
			run: func() error {
				return store.SaveWithDB(ctx, database, checkpoint.Clone())
			},
		},
		{
			name: "Delete",
			run: func() error {
				return store.Delete(ctx, checkpoint.ProjectionName)
			},
		},
		{
			name: "CreateTable",
			run: func() error {
				return store.CreateTable(ctx)
			},
		},
		{
			name: "List",
			run: func() error {
				_, err := store.List(ctx)
				return err
			},
		},
		{
			name: "SaveBatch",
			run: func() error {
				return store.SaveBatch(ctx, []*projection.Checkpoint{checkpoint.Clone()})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.Error(t, err)
			require.Equal(t, errors.InvalidInput, errors.Code(err))
		})
	}
}

func TestSQLCheckpointStore_InvalidQualifiedTableNames(t *testing.T) {
	tests := []string{
		".projection_checkpoints",
		"projection_checkpoints.",
		"projection..checkpoints",
		"1projection_checkpoints",
		"a.b.c",
	}

	for _, tableName := range tests {
		t.Run(tableName, func(t *testing.T) {
			err := validateCheckpointTableName(tableName)
			require.Error(t, err)
			require.Equal(t, errors.InvalidInput, errors.Code(err))
		})
	}
}

func TestSQLCheckpointStore_ValidQualifiedTableName(t *testing.T) {
	require.NoError(t, validateCheckpointTableName("public.projection_checkpoints"))
}

func TestSQLCheckpointStore_CreateTableWithSQLiteQualifiedTableName(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	_, err := database.Exec(ctx, `ATTACH DATABASE ':memory:' AS checkpoint_schema`)
	require.NoError(t, err)

	store := NewSQLCheckpointStore(database, "checkpoint_schema.projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	var indexCount int
	err = database.QueryRow(ctx, `
		SELECT COUNT(1)
		FROM checkpoint_schema.sqlite_master
		WHERE type = 'index' AND name = ?
	`, "idx_projection_checkpoints_updated_at").Scan(&indexCount)
	require.NoError(t, err)
	require.Equal(t, 1, indexCount)
}

func TestSQLCheckpointStore_NilDBReturnsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store := NewSQLCheckpointStore(nil, "projection_checkpoints")
	checkpoint := projection.NewCheckpoint("nil-db", 1, "event-1", time.Now())

	err := store.CreateTable(ctx)
	require.Error(t, err)
	require.Equal(t, errors.InvalidInput, errors.Code(err))

	err = store.SaveBatch(ctx, []*projection.Checkpoint{checkpoint})
	require.Error(t, err)
	require.Equal(t, errors.InvalidInput, errors.Code(err))
}

func TestSQLCheckpointStore_ValidTableNameBehavior(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	checkpoint := projection.NewCheckpoint("valid-table", 1, "event-1", time.Now())

	require.NoError(t, store.CreateTable(ctx))
	require.NoError(t, store.SaveWithDB(ctx, database, checkpoint))

	loaded, err := store.Load(ctx, checkpoint.ProjectionName)
	require.NoError(t, err)
	require.Equal(t, checkpoint.ProjectionName, loaded.ProjectionName)
	require.Equal(t, checkpoint.Position, loaded.Position)

	listed, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	next := projection.NewCheckpoint("valid-table-batch", 2, "event-2", time.Now())
	require.NoError(t, store.SaveBatch(ctx, []*projection.Checkpoint{next}))
	require.NoError(t, store.SaveBatch(ctx, nil))

	require.NoError(t, store.Delete(ctx, checkpoint.ProjectionName))
	_, err = store.Load(ctx, checkpoint.ProjectionName)
	require.Equal(t, errors.NotFound, errors.Code(err))
}

func TestSQLCheckpointStore_SaveWithDB_RowsAffectedErrorKeepsSuccessfulProgress(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	require.NoError(t, store.SaveWithDB(ctx, database, projection.NewCheckpoint("rows-affected-error", 1, "event-1", time.Now())))

	next := projection.NewCheckpoint("rows-affected-error", 2, "event-2", time.Now())
	require.NoError(t, store.SaveWithDB(ctx, rowsAffectedErrDB{IDatabase: database}, next))

	loaded, err := store.Load(ctx, next.ProjectionName)
	require.NoError(t, err)
	require.Equal(t, int64(2), loaded.Position)
	require.Equal(t, "event-2", loaded.LastEventID)
}

func TestSQLCheckpointStore_SaveBatch_DoesNotMoveCheckpointBackwards(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	require.NoError(t, store.SaveWithDB(ctx, database, projection.NewCheckpoint("batch-forward-only", 5, "event-5", time.Now())))
	require.NoError(t, store.SaveBatch(ctx, []*projection.Checkpoint{
		projection.NewCheckpoint("batch-forward-only", 3, "event-3", time.Now()),
	}))

	loaded, err := store.Load(ctx, "batch-forward-only")
	require.NoError(t, err)
	require.Equal(t, int64(5), loaded.Position)
	require.Equal(t, "event-5", loaded.LastEventID)
}

func TestSQLCheckpointStore_SaveBatch_RejectsInvalidCheckpoint(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	err := store.SaveBatch(ctx, []*projection.Checkpoint{
		projection.NewCheckpoint("ok", 1, "event-1", time.Now()),
		nil,
	})
	require.Error(t, err)
	require.Equal(t, errors.InvalidInput, errors.Code(err))

	var appErr *errors.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, 1, appErr.Details()["index"])
}

func TestSQLCheckpointStore_BuildForwardSaveQuery_MySQLUsesOldPositionForDependentFields(t *testing.T) {
	store := &SQLCheckpointStore{
		tableName: "projection_checkpoints",
		dialect:   dialect.New("mysql"),
	}
	checkpoint := projection.NewCheckpoint("orders", 10, "event-10", time.Now())

	query, _, err := store.buildForwardSaveQuery(checkpoint)
	require.NoError(t, err)
	lastEventIdx := strings.Index(query, "last_event_id = IF(? > position, ?, last_event_id)")
	positionIdx := strings.Index(query, "position = IF(? > position, ?, position)")
	require.NotEqual(t, -1, lastEventIdx)
	require.NotEqual(t, -1, positionIdx)
	require.Less(t, lastEventIdx, positionIdx)
}

func TestSQLCheckpointStore_UnknownDialectFailsFast(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(explicitUnknownCheckpointDB{IDatabase: database}, "projection_checkpoints")
	checkpoint := projection.NewCheckpoint("unknown-dialect", 1, "event-1", time.Now())

	err := store.CreateTable(ctx)
	require.Error(t, err)
	require.Equal(t, errors.Unsupported, errors.Code(err))

	err = store.SaveWithDB(ctx, database, checkpoint.Clone())
	require.Error(t, err)
	require.Equal(t, errors.Unsupported, errors.Code(err))

	err = store.forceSaveWithDB(ctx, database, checkpoint.Clone())
	require.Error(t, err)
	require.Equal(t, errors.Unsupported, errors.Code(err))
}

// TestSQLCheckpointStore_ForceSaveMovesBackward 验证 ForceSave 可把游标无条件回退到重建位置。
func TestSQLCheckpointStore_ForceSaveMovesBackward(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	require.NoError(t, store.SaveWithDB(ctx, database, projection.NewCheckpoint("force", 100, "event-100", time.Now())))

	// 只前进的 Save 不回退。
	require.NoError(t, store.SaveWithDB(ctx, database, projection.NewCheckpoint("force", 80, "event-80", time.Now())))
	loaded, err := store.Load(ctx, "force")
	require.NoError(t, err)
	require.Equal(t, int64(100), loaded.Position)

	// ForceSave 无条件回退到 80。
	require.NoError(t, store.forceSaveWithDB(ctx, database, projection.NewCheckpoint("force", 80, "event-80", time.Now())))
	loaded, err = store.Load(ctx, "force")
	require.NoError(t, err)
	require.Equal(t, int64(80), loaded.Position)
	require.Equal(t, "event-80", loaded.LastEventID)
}

// TestSQLCheckpointStore_ForceSaveInsertsWhenAbsent 验证目标行不存在时 ForceSave 走 INSERT。
func TestSQLCheckpointStore_ForceSaveInsertsWhenAbsent(t *testing.T) {
	ctx := context.Background()
	database := newProjectionCheckpointTestDB(t)
	store := NewSQLCheckpointStore(database, "projection_checkpoints")
	require.NoError(t, store.CreateTable(ctx))

	require.NoError(t, store.forceSaveWithDB(ctx, database, projection.NewCheckpoint("fresh", 7, "event-7", time.Now())))
	loaded, err := store.Load(ctx, "fresh")
	require.NoError(t, err)
	require.Equal(t, int64(7), loaded.Position)
}

type rowsAffectedErrDB struct {
	db.IDatabase
}

type explicitUnknownCheckpointDB struct {
	db.IDatabase
}

func (d explicitUnknownCheckpointDB) DialectName() string { return "unknown" }

func (d rowsAffectedErrDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.IDatabase.Exec(ctx, query, args...)
	if err == nil && strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO projection_checkpoints") {
		return rowsAffectedErrResult{Result: result}, nil
	}
	return result, err
}

type rowsAffectedErrResult struct {
	sql.Result
}

func (r rowsAffectedErrResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unavailable")
}

func newProjectionCheckpointTestDB(tb testing.TB) db.IDatabase {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(), `
		CREATE TABLE projection_read_model (
			id TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	require.NoError(tb, err)
	return database
}
