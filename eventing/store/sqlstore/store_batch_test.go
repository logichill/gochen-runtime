package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/store"
	"strings"
	"testing"
)

func TestSQLEventStoreAtomicBatchRollbackAndRetry(t *testing.T) {
	database := setupTestDB(t)
	s := newTestStore(t, database, "event_store")
	ctx := context.Background()
	existing := makeEvent(2, "TestAggregate", "existing", 1, nil)
	if err := s.AppendEvents(ctx, "TestAggregate", 2, []eventing.IStorableEvent[int64]{&existing}, 0); err != nil {
		t.Fatal(err)
	}
	first := makeEvent(1, "TestAggregate", "first", 1, nil)
	conflict := makeEvent(2, "TestAggregate", "conflict", 1, nil)
	batch := []store.EventAppend[int64]{{AggregateType: "TestAggregate", AggregateID: 1, Events: []eventing.IStorableEvent[int64]{&first}, ExpectedVersion: 0}, {AggregateType: "TestAggregate", AggregateID: 2, Events: []eventing.IStorableEvent[int64]{&conflict}, ExpectedVersion: 0}}
	if err := s.AppendEventsBatch(ctx, batch); !errors.Is(err, errors.Concurrency) {
		t.Fatalf("want conflict: %v", err)
	}
	if version, err := s.GetAggregateVersion(ctx, "TestAggregate", 1); err != nil || version != 0 {
		t.Fatalf("partial append: %d %v", version, err)
	}
	updated := makeEvent(2, "TestAggregate", "updated", 2, nil)
	batch[1] = store.EventAppend[int64]{AggregateType: "TestAggregate", AggregateID: 2, Events: []eventing.IStorableEvent[int64]{&updated}, ExpectedVersion: 1}
	for range 2 {
		if err := s.AppendEventsBatch(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[int64]uint64{1: 1, 2: 2} {
		if version, err := s.GetAggregateVersion(ctx, "TestAggregate", id); err != nil || version != want {
			t.Fatalf("version: %d %v", version, err)
		}
	}
	third := makeEvent(3, "TestAggregate", "third", 1, nil)
	if err := s.AppendEventsBatch(ctx, []store.EventAppend[int64]{{AggregateType: "TestAggregate", AggregateID: 3, Events: []eventing.IStorableEvent[int64]{&third}}, {AggregateType: "TestAggregate", AggregateID: 2, ExpectedVersion: 1}}); !errors.Is(err, errors.Concurrency) {
		t.Fatalf("stale read guard: %v", err)
	}
	if version, err := s.GetAggregateVersion(ctx, "TestAggregate", 3); err != nil || version != 0 {
		t.Fatalf("guard failure persisted earlier write: %d %v", version, err)
	}

}

type cancelBatchDB struct {
	db.IDatabase
	cancel context.CancelFunc
}

func (d *cancelBatchDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	if opts == nil || opts.Isolation != sql.LevelSerializable {
		return nil, fmt.Errorf("atomic batch must request serializable isolation")
	}
	tx, err := d.IDatabase.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &cancelBatchTx{ITransaction: tx, cancel: d.cancel}, nil
}

type cancelBatchTx struct {
	db.ITransaction
	cancel context.CancelFunc
}

func (tx *cancelBatchTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := tx.ITransaction.Exec(ctx, query, args...)
	if err == nil && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "INSERT") {
		tx.cancel()
	}
	return result, err
}
func TestSQLEventStoreAtomicBatchCancellationRollsBack(t *testing.T) {
	database := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newTestStore(t, &cancelBatchDB{IDatabase: database, cancel: cancel}, "event_store")
	first := makeEvent(1, "TestAggregate", "first", 1, nil)
	second := makeEvent(2, "TestAggregate", "second", 1, nil)
	err := s.AppendEventsBatch(ctx, []store.EventAppend[int64]{{AggregateType: "TestAggregate", AggregateID: 1, Events: []eventing.IStorableEvent[int64]{&first}, ExpectedVersion: 0}, {AggregateType: "TestAggregate", AggregateID: 2, Events: []eventing.IStorableEvent[int64]{&second}, ExpectedVersion: 0}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	for _, id := range []int64{1, 2} {
		if version, err := s.GetAggregateVersion(context.Background(), "TestAggregate", id); err != nil || version != 0 {
			t.Fatalf("partial canceled batch: %d %v", version, err)
		}
	}
}
