package lite

import (
	"context"
	"database/sql"
	"testing"

	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/errors"
)

func TestSaveRejectsUpdateWithoutWhere(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	err = model.Save(t.Context(), scanTestEntity{ID: 1, Name: "changed"})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	var name string
	sqlDB := scanTestSQLDB(t, liteORM)
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT name FROM scan_test_entities WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("query name: %v", err)
	}
	if name != "alpha" {
		t.Fatalf("update without where should not execute, got name %q", name)
	}
}

func TestSaveRejectsEmptyConditionWithoutExecutingUpdate(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	err := model.Save(t.Context(), scanTestEntity{ID: 1, Name: "changed"}, appendEmptyWhere)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
	if db.execCalls != 0 {
		t.Fatalf("expected no UPDATE execution, got %d", db.execCalls)
	}
}

func TestUpdateValuesRejectsUpdateWithoutWhere(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	err = model.UpdateValues(t.Context(), map[string]any{"name": "changed"})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}

	sqlDB := scanTestSQLDB(t, liteORM)
	rows, err := sqlDB.QueryContext(t.Context(), `SELECT name FROM scan_test_entities ORDER BY id`)
	if err != nil {
		t.Fatalf("query names: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("update without where should not execute, got names %#v", names)
	}
}

func TestUpdateValuesRejectsEmptyConditionWithoutExecutingUpdate(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	err := model.UpdateValues(t.Context(), map[string]any{"name": "changed"}, appendEmptyWhere)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
	if db.execCalls != 0 {
		t.Fatalf("expected no UPDATE execution, got %d", db.execCalls)
	}
}

func TestUpdateValuesWithResultRejectsUpdateWithoutWhere(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	resultModel, ok := model.(orm.IModelWithResult)
	if !ok {
		t.Fatal("expected model with result")
	}
	res, err := resultModel.UpdateValuesWithResult(t.Context(), map[string]any{"name": "changed"})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got result=%v err=%v", res, err)
	}
	if res != nil {
		t.Fatalf("expected nil result, got %v", res)
	}
}

func TestUpdateValuesWithResultRejectsEmptyConditionWithoutExecutingUpdate(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	resultModel, ok := model.(orm.IModelWithResult)
	if !ok {
		t.Fatal("expected model with result")
	}
	res, err := resultModel.UpdateValuesWithResult(t.Context(), map[string]any{"name": "changed"}, appendEmptyWhere)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got result=%v err=%v", res, err)
	}
	if res != nil {
		t.Fatalf("expected nil result, got %v", res)
	}
	if db.execCalls != 0 {
		t.Fatalf("expected no UPDATE execution, got %d", db.execCalls)
	}
}

func TestDeleteRejectsEmptyConditionWithoutExecutingDelete(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	err := model.Delete(t.Context(), appendEmptyWhere)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
	if db.execCalls != 0 {
		t.Fatalf("expected no DELETE execution, got %d", db.execCalls)
	}
}

func TestDeleteWithResultRejectsEmptyConditionWithoutExecutingDelete(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	resultModel, ok := model.(orm.IModelWithResult)
	if !ok {
		t.Fatal("expected model with result")
	}
	res, err := resultModel.DeleteWithResult(t.Context(), appendEmptyWhere)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got result=%v err=%v", res, err)
	}
	if res != nil {
		t.Fatalf("expected nil result, got %v", res)
	}
	if db.execCalls != 0 {
		t.Fatalf("expected no DELETE execution, got %d", db.execCalls)
	}
}

func TestDeleteAllowsEffectiveConditionWithBlankSibling(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	if err := model.Delete(t.Context(), appendEmptyWhere, orm.WithWhere("id = ?", 7)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if db.execCalls != 1 {
		t.Fatalf("expected one DELETE execution, got %d", db.execCalls)
	}
	if db.lastQuery != "DELETE FROM `scan_test_entities` WHERE id = ?" {
		t.Fatalf("unexpected query: %s", db.lastQuery)
	}
	if len(db.lastArgs) != 1 || db.lastArgs[0] != 7 {
		t.Fatalf("unexpected args: %#v", db.lastArgs)
	}
}

func TestCreateSplitsLargeBatchByParameterBudget(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	colsPerRow := 2
	maxRowsPerExec := dialect.DefaultMaxBindParameters / colsPerRow
	entities := make([]any, maxRowsPerExec+1)
	for i := range entities {
		entities[i] = scanTestEntity{ID: int64(i + 1), Name: "alpha"}
	}

	if err := model.Create(t.Context(), entities...); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if db.execCalls != 2 {
		t.Fatalf("expected Create to split into 2 executions, got %d", db.execCalls)
	}
	if db.beginCalls != 1 || db.commitCalls != 1 || db.rollbackCalls != 0 {
		t.Fatalf("expected one committed transaction, got begin=%d commit=%d rollback=%d", db.beginCalls, db.commitCalls, db.rollbackCalls)
	}
	if len(db.execArgCounts) != 2 {
		t.Fatalf("expected 2 arg count records, got %#v", db.execArgCounts)
	}
	if db.execArgCounts[0] > dialect.DefaultMaxBindParameters {
		t.Fatalf("first chunk exceeded parameter budget: %d", db.execArgCounts[0])
	}
	if db.execArgCounts[1] != colsPerRow {
		t.Fatalf("expected final chunk to contain one row, got %d args", db.execArgCounts[1])
	}
}

func TestCreateLargeBatchRollsBackAllChunksOnLaterFailure(t *testing.T) {
	liteORM := setupScanTestOrm(t)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}

	colsPerRow := 2
	maxRowsPerExec := dialect.DefaultMaxBindParameters / colsPerRow
	entities := make([]any, maxRowsPerExec+1)
	for i := 0; i < maxRowsPerExec; i++ {
		entities[i] = scanTestEntity{ID: int64(i + 3), Name: "new"}
	}
	entities[maxRowsPerExec] = scanTestEntity{ID: 1, Name: "duplicate"}

	if err := model.Create(t.Context(), entities...); err == nil {
		t.Fatal("expected duplicate key error")
	}

	sqlDB := scanTestSQLDB(t, liteORM)
	var count int
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM scan_test_entities`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected rollback to keep original rows only, got count=%d", count)
	}
}

func TestFirstForUpdateForwardsLockingClause(t *testing.T) {
	db := newLiteCaptureDB("mysql")
	model := newCaptureModel(t, db)

	var row scanTestEntity
	if err := model.First(t.Context(), &row, orm.WithWhere("id = ?", 7), orm.WithForUpdate()); err != nil {
		t.Fatalf("First: %v", err)
	}

	if db.lastQuery != "SELECT * FROM `scan_test_entities` WHERE id = ? LIMIT ? FOR UPDATE" {
		t.Fatalf("unexpected query: %s", db.lastQuery)
	}
	if len(db.lastArgs) != 2 || db.lastArgs[0] != 7 || db.lastArgs[1] != 1 {
		t.Fatalf("unexpected args: %#v", db.lastArgs)
	}
}

func TestFindForUpdateForwardsLockingClause(t *testing.T) {
	db := newLiteCaptureDB("postgres")
	model := newCaptureModel(t, db)

	var rows []scanTestEntity
	if err := model.Find(t.Context(), &rows, orm.WithWhere("name = ?", "alpha"), orm.WithForUpdate()); err != nil {
		t.Fatalf("Find: %v", err)
	}

	if db.lastQuery != `SELECT * FROM "scan_test_entities" WHERE name = ? FOR UPDATE` {
		t.Fatalf("unexpected query: %s", db.lastQuery)
	}
	if len(db.lastArgs) != 1 || db.lastArgs[0] != "alpha" {
		t.Fatalf("unexpected args: %#v", db.lastArgs)
	}
}

func scanTestSQLDB(t *testing.T, liteORM *Orm) *sql.DB {
	t.Helper()

	sqlDB, ok := stdsql.DBOf(liteORM.Database())
	if !ok {
		t.Fatal("expected stdsql database")
	}
	return sqlDB
}

func newCaptureModel(t *testing.T, db *liteCaptureDB) orm.IModel {
	t.Helper()

	ormEngine, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	liteORM := ormEngine.(*Orm)
	model, err := liteORM.Model(modelMetaForScanTest())
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	return model
}

func appendEmptyWhere(opts *orm.QueryOptions) {
	opts.Where = append(opts.Where, orm.Condition{})
}

type liteCaptureDB struct {
	dialectName   string
	lastQuery     string
	lastArgs      []any
	execCalls     int
	execArgCounts []int
	beginCalls    int
	commitCalls   int
	rollbackCalls int
}

func newLiteCaptureDB(dialectName string) *liteCaptureDB {
	return &liteCaptureDB{dialectName: dialectName}
}

func (db *liteCaptureDB) DialectName() string { return db.dialectName }

func (db *liteCaptureDB) Query(_ context.Context, query string, args ...any) (db.IRows, error) {
	db.lastQuery = query
	db.lastArgs = append([]any(nil), args...)
	return &liteCaptureRows{
		columns: []string{"id", "name"},
		values:  []any{int64(7), "alpha"},
	}, nil
}

func (db *liteCaptureDB) QueryRow(context.Context, string, ...any) db.IRow {
	panic("liteCaptureDB.QueryRow: not implemented")
}

func (db *liteCaptureDB) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	db.lastQuery = query
	db.lastArgs = append([]any(nil), args...)
	db.execCalls++
	db.execArgCounts = append(db.execArgCounts, len(args))
	return noopResult(1), nil
}

func (db *liteCaptureDB) Begin(context.Context) (db.ITransaction, error) {
	db.beginCalls++
	return &liteCaptureTx{liteCaptureDB: db}, nil
}

func (db *liteCaptureDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	db.beginCalls++
	return &liteCaptureTx{liteCaptureDB: db}, nil
}

func (db *liteCaptureDB) Ping(context.Context) error { return nil }

func (db *liteCaptureDB) Close() error { return nil }

type liteCaptureTx struct {
	*liteCaptureDB
}

func (tx *liteCaptureTx) Commit() error {
	tx.commitCalls++
	return nil
}

func (tx *liteCaptureTx) Rollback() error {
	tx.rollbackCalls++
	return nil
}

type liteCaptureRows struct {
	columns []string
	values  []any
	read    bool
}

func (r *liteCaptureRows) Next() bool {
	if r.read {
		return false
	}
	r.read = true
	return true
}

func (r *liteCaptureRows) Scan(dest ...any) error {
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = r.values[i].(int64)
		case *string:
			*d = r.values[i].(string)
		case *any:
			*d = r.values[i]
		default:
			return errors.NewCode(errors.InvalidInput, "unsupported scan destination")
		}
	}
	return nil
}

func (r *liteCaptureRows) Close() error { return nil }

func (r *liteCaptureRows) Err() error { return nil }

func (r *liteCaptureRows) Columns() ([]string, error) {
	return append([]string(nil), r.columns...), nil
}

func (r *liteCaptureRows) ColumnTypes() ([]*sql.ColumnType, error) { return nil, nil }
