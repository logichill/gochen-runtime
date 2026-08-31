package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"gochen/testkit/require"

	"gochen/codec/idcodec"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
)

type abortingTxEvent struct {
	aggregateID   int64
	aggregateType string
	version       uint64
}

type abortingTx struct {
	reportedCurrentVersion uint64
	eventsByID             map[string]abortingTxEvent
	eventIDsByVersion      map[string]string
	aborted                bool
	savepoints             map[string]bool
	rollbackToCount        int
}

func newAbortingTx() *abortingTx {
	tx := &abortingTx{
		eventsByID:        make(map[string]abortingTxEvent),
		eventIDsByVersion: make(map[string]string),
		savepoints:        make(map[string]bool),
	}
	tx.seed("event-existing", 1, "TestAggregate", 2)
	return tx
}

func (t *abortingTx) seed(eventID string, aggregateID int64, aggregateType string, version uint64) {
	t.eventsByID[eventID] = abortingTxEvent{aggregateID: aggregateID, aggregateType: aggregateType, version: version}
	t.eventIDsByVersion[t.versionKey(aggregateID, aggregateType, version)] = eventID
}

func (t *abortingTx) versionKey(aggregateID int64, aggregateType string, version uint64) string {
	return fmt.Sprintf("%d|%s|%d", aggregateID, aggregateType, version)
}

func (t *abortingTx) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "WHERE 1 = 0") {
		return noGlobalPositionRows{}, nil
	}
	return nil, errors.New("not implemented")
}

func (t *abortingTx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if t.aborted {
		return abortingRow{err: errors.New("current transaction is aborted")}
	}
	switch {
	case strings.Contains(query, "SELECT aggregate_type FROM"):
		aggregateID, _ := args[0].(int64)
		for _, evt := range t.eventsByID {
			if evt.aggregateID == aggregateID {
				return abortingRow{values: []any{evt.aggregateType}}
			}
		}
		return abortingRow{err: sql.ErrNoRows}
	case strings.Contains(query, "COALESCE(MAX(version), 0)"):
		return abortingRow{values: []any{t.reportedCurrentVersion}}
	case strings.Contains(query, "SELECT id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata") &&
		strings.Contains(query, "WHERE id = ?"):
		eventID, _ := args[0].(string)
		evt, ok := t.eventsByID[eventID]
		if !ok {
			return abortingRow{err: sql.ErrNoRows}
		}
		return abortingRow{values: []any{eventID, "TestEvent", evt.aggregateID, evt.aggregateType, evt.version, 1, time.Now(), `{}`, `{}`}}
	case strings.Contains(query, "SELECT aggregate_id, version"):
		eventID, _ := args[0].(string)
		evt, ok := t.eventsByID[eventID]
		if !ok {
			return abortingRow{err: sql.ErrNoRows}
		}
		return abortingRow{values: []any{evt.aggregateID, evt.version}}
	case strings.Contains(query, "SELECT id FROM"):
		aggregateID, _ := args[0].(int64)
		aggregateType, _ := args[1].(string)
		version, _ := args[2].(uint64)
		eventID, ok := t.eventIDsByVersion[t.versionKey(aggregateID, aggregateType, version)]
		if !ok {
			return abortingRow{err: sql.ErrNoRows}
		}
		return abortingRow{values: []any{eventID}}
	default:
		return abortingRow{err: fmt.Errorf("unsupported query: %s", query)}
	}
}

func (t *abortingTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(trimmed, "SAVEPOINT ") {
		name := strings.TrimPrefix(trimmed, "SAVEPOINT ")
		t.savepoints[name] = t.aborted
		return abortingResult(0), nil
	}
	if strings.HasPrefix(trimmed, "ROLLBACK TO SAVEPOINT ") {
		name := strings.TrimPrefix(trimmed, "ROLLBACK TO SAVEPOINT ")
		aborted, ok := t.savepoints[name]
		if !ok {
			return nil, fmt.Errorf("unknown savepoint %s", name)
		}
		t.aborted = aborted
		t.rollbackToCount++
		return abortingResult(0), nil
	}
	if strings.HasPrefix(trimmed, "RELEASE SAVEPOINT ") {
		name := strings.TrimPrefix(trimmed, "RELEASE SAVEPOINT ")
		delete(t.savepoints, name)
		return abortingResult(0), nil
	}
	if t.aborted {
		return nil, errors.New("current transaction is aborted")
	}
	if !strings.Contains(trimmed, "INSERT INTO event_store") {
		return nil, fmt.Errorf("unsupported exec: %s", query)
	}
	if len(args) > 9 {
		t.aborted = true
		return nil, errors.New("duplicate key value violates unique constraint \"event_store_aggregate_id_aggregate_type_version_key\"")
	}

	eventID, _ := args[0].(string)
	aggregateID, _ := args[2].(int64)
	aggregateType, _ := args[3].(string)
	version, _ := args[4].(uint64)
	if _, ok := t.eventsByID[eventID]; ok {
		t.aborted = true
		return nil, errors.New("duplicate key value violates unique constraint \"event_store_pkey\"")
	}
	if _, ok := t.eventIDsByVersion[t.versionKey(aggregateID, aggregateType, version)]; ok {
		t.aborted = true
		return nil, errors.New("duplicate key value violates unique constraint \"event_store_aggregate_id_aggregate_type_version_key\"")
	}
	t.seed(eventID, aggregateID, aggregateType, version)
	return abortingResult(1), nil
}

func (t *abortingTx) CreateSavepoint(ctx context.Context, name string) error {
	_, err := t.Exec(ctx, "SAVEPOINT "+name)
	return err
}

func (t *abortingTx) RollbackToSavepoint(ctx context.Context, name string) error {
	_, err := t.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
	return err
}

func (t *abortingTx) ReleaseSavepoint(ctx context.Context, name string) error {
	_, err := t.Exec(ctx, "RELEASE SAVEPOINT "+name)
	return err
}

func (t *abortingTx) Begin(ctx context.Context) (db.ITransaction, error) {
	return nil, errors.New("nested tx unsupported")
}
func (t *abortingTx) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	return nil, errors.New("nested tx unsupported")
}
func (t *abortingTx) Ping(ctx context.Context) error { return nil }
func (t *abortingTx) Close() error                   { return nil }
func (t *abortingTx) Commit() error                  { return nil }
func (t *abortingTx) Rollback() error                { t.aborted = false; return nil }

type abortingRow struct {
	values []any
	err    error
}

func (r abortingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan arg mismatch: got %d want %d", len(dest), len(r.values))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *uint64:
			*d = r.values[i].(uint64)
		case *int:
			*d = r.values[i].(int)
		case *time.Time:
			*d = r.values[i].(time.Time)
		case *string:
			*d = r.values[i].(string)
		case *any:
			*d = r.values[i]
		default:
			return fmt.Errorf("unsupported scan type %T", dest[i])
		}
	}
	return nil
}

func (r abortingRow) Err() error { return r.err }

type abortingResult int64

func (r abortingResult) LastInsertId() (int64, error) { return 0, errors.New("unsupported") }
func (r abortingResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestSQLEventStore_AppendEventsWithDB_RecoversAbortedTxWithSavepoint(t *testing.T) {
	tx := newAbortingTx()
	store, err := NewSQLEventStoreWithCodec[int64](tx, "event_store", idcodec.NewInt64[int64]())
	require.NoError(t, err)

	events := []eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, map[string]any{"step": 1}),
		makeEvent(1, "TestAggregate", "event-2", 2, map[string]any{"step": 2}),
	}

	err = store.AppendEventsWithDB(context.Background(), tx, "TestAggregate", int64(1), toStorableEvents(events), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.Concurrency), "expected concurrency classification, got %v", err)
	require.Falsef(t, errors.Is(err, errors.Database), "expected savepoint rollback to avoid aborted-tx database error, got %v", err)
	require.False(t, tx.aborted, "savepoint rollback should clear transaction aborted state")
	require.GreaterOrEqual(t, tx.rollbackToCount, 2, "expected batch and duplicate single insert to rollback to savepoint")

	stored, ok := tx.eventsByID["event-1"]
	require.True(t, ok, "first event should be inserted during fallback")
	require.Equal(t, uint64(1), stored.version)
}

type plainTx struct {
	insertCount int
}

func (t *plainTx) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "WHERE 1 = 0") {
		return noGlobalPositionRows{}, nil
	}
	return nil, errors.New("not implemented")
}
func (t *plainTx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if strings.Contains(query, "SELECT aggregate_type FROM") {
		return abortingRow{err: sql.ErrNoRows}
	}
	if strings.Contains(query, "COALESCE(MAX(version), 0)") {
		return abortingRow{values: []any{uint64(0)}}
	}
	return abortingRow{err: sql.ErrNoRows}
}
func (t *plainTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "SAVEPOINT") {
		return nil, errors.New("savepoint should not be used")
	}
	if strings.Contains(query, "INSERT INTO event_store") {
		t.insertCount++
		return abortingResult(1), nil
	}
	return abortingResult(0), nil
}
func (t *plainTx) Begin(ctx context.Context) (db.ITransaction, error) {
	return nil, errors.New("nested tx unsupported")
}
func (t *plainTx) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	return nil, errors.New("nested tx unsupported")
}
func (t *plainTx) Ping(ctx context.Context) error { return nil }
func (t *plainTx) Close() error                   { return nil }
func (t *plainTx) Commit() error                  { return nil }
func (t *plainTx) Rollback() error                { return nil }

type disabledSavepointTx struct {
	plainTx
	savepointCalls int
}

func (t *disabledSavepointTx) CreateSavepoint(ctx context.Context, name string) error {
	t.savepointCalls++
	return errors.New("savepoint should not be used when capability is disabled")
}
func (t *disabledSavepointTx) RollbackToSavepoint(ctx context.Context, name string) error {
	t.savepointCalls++
	return errors.New("savepoint should not be used when capability is disabled")
}
func (t *disabledSavepointTx) ReleaseSavepoint(ctx context.Context, name string) error {
	t.savepointCalls++
	return errors.New("savepoint should not be used when capability is disabled")
}
func (t *disabledSavepointTx) SupportsSavepoints() bool { return false }

func TestSQLEventStore_AppendEventsWithDB_SkipsSavepointWhenCapabilityDisabled(t *testing.T) {
	tx := &disabledSavepointTx{}
	store, err := NewSQLEventStoreWithCodec[int64](tx, "event_store", idcodec.NewInt64[int64]())
	require.NoError(t, err)

	err = store.AppendEventsWithDB(context.Background(), tx, "TestAggregate", int64(1), toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, map[string]any{"step": 1}),
	}), 0)
	require.NoError(t, err)
	require.Zero(t, tx.savepointCalls)
	require.Equal(t, 1, tx.insertCount)
}
func TestSQLEventStore_AppendEventsWithDB_AllowsTransactionsWithoutSavepointCapability(t *testing.T) {
	tx := &plainTx{}
	store, err := NewSQLEventStoreWithCodec[int64](tx, "event_store", idcodec.NewInt64[int64]())
	require.NoError(t, err)

	err = store.AppendEventsWithDB(context.Background(), tx, "TestAggregate", int64(1), toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-1", 1, map[string]any{"step": 1}),
	}), 0)
	require.NoError(t, err)
	require.Equal(t, 1, tx.insertCount)
}

var _ db.ISavepointTransaction = (*abortingTx)(nil)
var _ db.ITransaction = (*plainTx)(nil)
var _ db.IRow = abortingRow{}
var _ db.IRows = noGlobalPositionRows{}
var _ sql.Result = abortingResult(0)

type noGlobalPositionRows struct{}

func (noGlobalPositionRows) Next() bool                              { return false }
func (noGlobalPositionRows) Scan(dest ...any) error                  { return nil }
func (noGlobalPositionRows) Close() error                            { return nil }
func (noGlobalPositionRows) Err() error                              { return nil }
func (noGlobalPositionRows) Columns() ([]string, error)              { return []string{"id"}, nil }
func (noGlobalPositionRows) ColumnTypes() ([]*sql.ColumnType, error) { return nil, nil }
