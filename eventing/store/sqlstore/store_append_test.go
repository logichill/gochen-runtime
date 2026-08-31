package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/messaging"
)

type beginCountingDB struct {
	db.IDatabase
	beginCalls int
}

func (d *beginCountingDB) Begin(ctx context.Context) (db.ITransaction, error) {
	d.beginCalls++
	return d.IDatabase.Begin(ctx)
}

type beginFailingDB struct {
	db.IDatabase
}

func (d *beginFailingDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, fmt.Errorf("begin failed")
}

// TestSQLEventStore_AppendEvents 验证 SQLEventStore AppendEvents。
func TestSQLEventStore_AppendEvents(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(123)

	events := []eventing.Event[int64]{
		makeEvent(aggregateID, "TestAggregate", "event-1", 1, map[string]any{"value": 100}),
		makeEvent(aggregateID, "TestAggregate", "event-2", 2, map[string]any{"value": 200}),
	}

	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 2)
	assert.Equal(t, "event-1", loaded[0].ID)
	assert.Equal(t, "event-2", loaded[1].ID)
}

func TestSQLEventStore_AppendEventsRejectsNilEvents(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")
	var typedNil *eventing.Event[int64]
	for _, event := range []eventing.IStorableEvent[int64]{nil, typedNil} {
		err := store.AppendEvents(context.Background(), "TestAggregate", 1, []eventing.IStorableEvent[int64]{event}, 0)
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.InvalidInput))
	}
}

func TestSQLEventStore_AppendEventsRejectsEmptyAggregateTypeForEmptyBatch(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	for _, aggregateType := range []string{"", "   "} {
		err := store.AppendEvents(context.Background(), aggregateType, 1, nil, 0)
		require.Error(t, err)
		require.Truef(t, errors.Is(err, errors.InvalidInput), "aggregateType %q: %v", aggregateType, err)
	}
	require.NoError(t, store.AppendEvents(context.Background(), "TestAggregate", 1, nil, 0))
}

func TestSQLEventStore_AppendEventsChunksByParameterBudget(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(123)
	eventCount := appendBatchChunkSize(9) + 2
	events := make([]eventing.Event[int64], 0, eventCount)
	for i := 1; i <= eventCount; i++ {
		events = append(events, makeEvent(aggregateID, "TestAggregate", fmt.Sprintf("event-chunk-%d", i), uint64(i), nil))
	}

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0))

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	require.NoError(t, err)
	require.Len(t, loaded, eventCount)
	require.Equal(t, uint64(eventCount), loaded[eventCount-1].Version)
}

func TestSQLEventStore_AppendEventsChunksByParameterBudgetWithGlobalPosition(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(123)
	eventCount := appendBatchChunkSize(10) + 2
	events := make([]eventing.Event[int64], 0, eventCount)
	for i := 1; i <= eventCount; i++ {
		events = append(events, makeEvent(aggregateID, "TestAggregate", fmt.Sprintf("event-global-chunk-%d", i), uint64(i), nil))
	}

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0))

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	require.NoError(t, err)
	require.Len(t, loaded, eventCount)

	var minPosition, maxPosition int64
	require.NoError(t, database.QueryRow(ctx, `SELECT MIN(global_position), MAX(global_position) FROM event_store`).Scan(&minPosition, &maxPosition))
	require.Equal(t, int64(1), minPosition)
	require.Equal(t, int64(eventCount), maxPosition)

	var nextPosition int64
	require.NoError(t, database.QueryRow(ctx, `SELECT next_position FROM event_store_positions WHERE store_name = ?`, "event_store").Scan(&nextPosition))
	require.Equal(t, int64(eventCount+1), nextPosition)
}

func TestSQLEventStore_AppendPreparedBatchAllocatesFromEventStorePositions(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	store := newTestStore(t, database, "event_store")

	prepared := []preparedEvent{
		{
			id:            "event-allocated-1",
			typ:           "TestEvent",
			aggregateType: "ConcurrentAggregate",
			version:       1,
			schemaVersion: 1,
			timestamp:     time.Now(),
			payloadJSON:   "{}",
			metadataJSON:  "{}",
		},
		{
			id:            "event-allocated-2",
			typ:           "TestEvent",
			aggregateType: "ConcurrentAggregate",
			version:       2,
			schemaVersion: 1,
			timestamp:     time.Now(),
			payloadJSON:   "{}",
			metadataJSON:  "{}",
		},
	}

	require.NoError(t, store.appendPreparedBatch(ctx, database, int64(1), int64(1), prepared, time.Now(), true))

	var minPosition, maxPosition, nextPosition int64
	require.NoError(t, database.QueryRow(ctx, `SELECT MIN(global_position), MAX(global_position) FROM event_store`).Scan(&minPosition, &maxPosition))
	require.NoError(t, database.QueryRow(ctx, `SELECT next_position FROM event_store_positions WHERE store_name = ?`, "event_store").Scan(&nextPosition))
	require.Equal(t, int64(1), minPosition)
	require.Equal(t, int64(2), maxPosition)
	require.Equal(t, int64(3), nextPosition)
}

// TestSQLEventStore_VersionConflict 验证 SQLEventStore VersionConflict。
func TestSQLEventStore_VersionConflict(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(456)

	events1 := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil)}
	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events1), 0)
	assert.NoError(t, err)

	events2 := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-2", 2, nil)}
	err = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events2), 0)
	assert.Error(t, err)
}

func TestSQLEventStore_AllowsSameAggregateIDAcrossTypes(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(42)

	require.NoError(t, store.AppendEvents(ctx, "User", aggregateID, toStorableEvents([]eventing.Event[int64]{
		makeEvent(aggregateID, "User", "user-created", 1, nil),
	}), 0))
	require.NoError(t, store.AppendEvents(ctx, "Order", aggregateID, toStorableEvents([]eventing.Event[int64]{
		makeEvent(aggregateID, "Order", "order-created", 1, nil),
	}), 0))

	userEvents, err := store.LoadEvents(ctx, "User", aggregateID, 0)
	require.NoError(t, err)
	require.Len(t, userEvents, 1)
	require.Equal(t, uint64(1), userEvents[0].Version)
	require.Equal(t, "User", userEvents[0].AggregateType)

	orderEvents, err := store.LoadEvents(ctx, "Order", aggregateID, 0)
	require.NoError(t, err)
	require.Len(t, orderEvents, 1)
	require.Equal(t, uint64(1), orderEvents[0].Version)
	require.Equal(t, "Order", orderEvents[0].AggregateType)
}

// TestSQLEventStore_Idempotency 验证 SQLEventStore Idempotency。
func TestSQLEventStore_Idempotency(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(789)

	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil)}

	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.NoError(t, err)

	err = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 1)
}

// TestSQLEventStore_AppendEventsWithDB 验证 SQLEventStore AppendEventsWithDB。
func TestSQLEventStore_AppendEventsWithDB(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(100)

	tx, err := database.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-tx-1", 1, nil)}
	err = store.AppendEventsWithDB(ctx, tx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.NoError(t, err)

	err = tx.Commit()
	assert.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 1)
}

func TestSQLEventStore_AppendEventsWithDB_SingleEventRollsBackOnInsertFailure(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")
	ctx := context.Background()

	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-existing", 1, nil),
	}), 0))

	err := store.AppendEventsWithDB(ctx, database, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{
		makeEvent(2, "TestAggregate", "event-existing", 1, nil),
	}), 0)
	require.Error(t, err)

	loaded, loadErr := store.LoadEvents(ctx, "TestAggregate", 2, 0)
	require.NoError(t, loadErr)
	require.Empty(t, loaded)
}

func TestSQLEventStore_AppendEvents_RejectsAggregateIDMismatch(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	evt := makeEvent(2, "TestAggregate", "event-mismatch", 1, nil)
	err := store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{evt}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)

	loaded, loadErr := store.LoadEvents(ctx, "TestAggregate", 1, 0)
	require.NoError(t, loadErr)
	require.Empty(t, loaded)
}

func TestSQLEventStore_AppendEvents_RejectsAggregateIDMismatchBeforeVersionCheck(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-existing", 1, nil),
	}), 0))

	evt := makeEvent(2, "TestAggregate", "event-mismatch", 1, nil)
	err := store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{evt}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input before concurrency check, got %v", err)
	require.Falsef(t, errors.Is(err, errors.Concurrency), "aggregate id mismatch should not be hidden by version conflict: %v", err)
}

func TestSQLEventStore_AppendEvents_RejectsAggregateTypeMismatchBeforeVersionCheck(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "Order", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "Order", "event-existing", 1, nil),
	}), 0))

	evt := makeEvent(1, "Invoice", "event-mismatch", 1, nil)
	err := store.AppendEvents(ctx, "Order", 1, toStorableEvents([]eventing.Event[int64]{evt}), 0)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input before concurrency check, got %v", err)
	require.Falsef(t, errors.Is(err, errors.Concurrency), "aggregate type mismatch should not be hidden by version conflict: %v", err)
}

func TestSQLEventStore_AppendEventsWithDB_GlobalPositionMultiEventRollbackOnFailure(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	first := makeEvent(1, "TestAggregate", "event-dup", 1, nil)
	second := makeEvent(1, "TestAggregate", "event-dup", 2, nil)

	err := store.AppendEventsWithDB(ctx, database, "TestAggregate", int64(1), toStorableEvents([]eventing.Event[int64]{first, second}), 0)
	require.Error(t, err)

	loaded, loadErr := store.LoadEvents(ctx, "TestAggregate", 1, 0)
	require.NoError(t, loadErr)
	require.Empty(t, loaded)

	var nextPosition int64
	require.NoError(t, database.QueryRow(ctx, `SELECT next_position FROM event_store_positions WHERE store_name = ?`, "event_store").Scan(&nextPosition))
	require.Equal(t, int64(1), nextPosition)
}

func TestSQLEventStore_AppendEventsWithDB_GlobalPositionSingleEventFailureRollsBackAllocator(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-dup-id", 1, nil),
	}), 0))

	err := store.AppendEventsWithDB(ctx, database, "TestAggregate", int64(2), toStorableEvents([]eventing.Event[int64]{
		makeEvent(2, "TestAggregate", "event-dup-id", 1, nil),
	}), 0)
	require.Error(t, err)

	var nextPosition int64
	require.NoError(t, database.QueryRow(ctx, `SELECT next_position FROM event_store_positions WHERE store_name = ?`, "event_store").Scan(&nextPosition))
	require.Equal(t, int64(2), nextPosition)
}

func TestSQLEventStore_AppendEventsWithDB_MultiEventRollbackOnFailure(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	first := makeEvent(1, "TestAggregate", "event-dup", 1, nil)
	second := makeEvent(1, "TestAggregate", "event-dup", 2, nil)

	err := store.AppendEventsWithDB(ctx, database, "TestAggregate", int64(1), toStorableEvents([]eventing.Event[int64]{first, second}), 0)
	require.Error(t, err)

	loaded, loadErr := store.LoadEvents(ctx, "TestAggregate", 1, 0)
	require.NoError(t, loadErr)
	require.Empty(t, loaded)
}

func TestSQLEventStore_AppendEventsWithDB_CrossChunkRollbackOnFailure(t *testing.T) {
	tests := []struct {
		name         string
		argsPerEvent int
		global       bool
	}{
		{name: "plain", argsPerEvent: 9},
		{name: "global_position", argsPerEvent: 10, global: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			database := setupTestDB(t)
			if tc.global {
				addGlobalPositionColumnAndIndex(t, database)
			}
			store := newTestStore(t, database, "event_store")

			ctx := context.Background()
			eventCount := appendBatchChunkSize(tc.argsPerEvent) + 2
			events := make([]eventing.Event[int64], 0, eventCount)
			for i := 1; i <= eventCount; i++ {
				eventID := fmt.Sprintf("event-cross-chunk-%d", i)
				if i > eventCount-2 {
					eventID = "event-cross-chunk-dup"
				}
				events = append(events, makeEvent(1, "TestAggregate", eventID, uint64(i), nil))
			}

			err := store.AppendEventsWithDB(ctx, database, "TestAggregate", int64(1), toStorableEvents(events), 0)
			require.Error(t, err)

			var count int
			require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_store`).Scan(&count))
			require.Equal(t, 0, count)
			if tc.global {
				var nextPosition int64
				require.NoError(t, database.QueryRow(ctx, `SELECT next_position FROM event_store_positions WHERE store_name = ?`, "event_store").Scan(&nextPosition))
				require.Equal(t, int64(1), nextPosition)
			}
		})
	}
}

// TestSQLEventStore_EmptyEvents 验证 SQLEventStore EmptyEvents。
func TestSQLEventStore_EmptyEvents(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(500)

	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, []eventing.IStorableEvent[int64]{}, 0)
	assert.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 0)
}

func TestSQLEventStore_AppendEventsValidatesBeforeBegin(t *testing.T) {
	tests := []struct {
		name          string
		aggregateType string
		aggregateID   int64
		events        func() []eventing.IStorableEvent[int64]
	}{
		{
			name:          "blank aggregate type with empty batch",
			aggregateType: "  ",
			aggregateID:   1,
			events:        func() []eventing.IStorableEvent[int64] { return nil },
		},
		{
			name:          "nil event",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events:        func() []eventing.IStorableEvent[int64] { return []eventing.IStorableEvent[int64]{nil} },
		},
		{
			name:          "typed nil event",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events: func() []eventing.IStorableEvent[int64] {
				var event *eventing.Event[int64]
				return []eventing.IStorableEvent[int64]{event}
			},
		},
		{
			name:          "aggregate ID mismatch",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events: func() []eventing.IStorableEvent[int64] {
				return toStorableEvents([]eventing.Event[int64]{makeEvent(2, "TestAggregate", "event-1", 1, nil)})
			},
		},
		{
			name:          "aggregate type mismatch",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events: func() []eventing.IStorableEvent[int64] {
				return toStorableEvents([]eventing.Event[int64]{makeEvent(1, "OtherAggregate", "event-1", 1, nil)})
			},
		},
		{
			name:          "event version mismatch",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events: func() []eventing.IStorableEvent[int64] {
				return toStorableEvents([]eventing.Event[int64]{makeEvent(1, "TestAggregate", "event-1", 2, nil)})
			},
		},
		{
			name:          "event validation failure",
			aggregateType: "TestAggregate",
			aggregateID:   1,
			events: func() []eventing.IStorableEvent[int64] {
				return toStorableEvents([]eventing.Event[int64]{makeEvent(1, "TestAggregate", "", 1, nil)})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := &beginCountingDB{IDatabase: setupTestDB(t)}
			store := newTestStore(t, database, "event_store")

			err := store.AppendEvents(context.Background(), tt.aggregateType, tt.aggregateID, tt.events(), 0)
			require.Truef(t, errors.Is(err, errors.InvalidInput), "error = %v", err)
			require.Zero(t, database.beginCalls)
		})
	}
}

func TestSQLEventStore_AppendEventsEmptyBatchDoesNotBegin(t *testing.T) {
	database := &beginCountingDB{IDatabase: setupTestDB(t)}
	store := newTestStore(t, database, "event_store")

	require.NoError(t, store.AppendEvents(context.Background(), " TestAggregate ", 1, nil, 0))
	require.Zero(t, database.beginCalls)

	event := makeEvent(1, "TestAggregate", "event-1", 1, nil)
	require.NoError(t, store.AppendEvents(context.Background(), " TestAggregate ", 1, toStorableEvents([]eventing.Event[int64]{event}), 0))
	require.Equal(t, 1, database.beginCalls)
}

func TestSQLEventStore_AppendEventsBeginFailureDoesNotMutateCallerEvent(t *testing.T) {
	database := &beginFailingDB{IDatabase: setupTestDB(t)}
	store := newTestStore(t, database, "event_store")
	event := makeEvent(1, "", "event-1", 1, nil)
	event.Message.Metadata = nil

	err := store.AppendEvents(context.Background(), " TestAggregate ", 1, []eventing.IStorableEvent[int64]{&event}, 0)
	require.Truef(t, errors.Is(err, errors.Database), "error = %v", err)
	require.Empty(t, event.AggregateType)
	require.Nil(t, event.Message.Metadata)
}

// TestSQLEventStore_MixedAggregateTypes 验证 SQLEventStore MixedAggregateTypes。
func TestSQLEventStore_MixedAggregateTypes(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(600)

	events := []eventing.Event[int64]{
		makeEvent(aggregateID, "TypeA", "event-1", 1, nil),
		makeEvent(aggregateID, "TypeB", "event-2", 2, nil),
	}
	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mixed aggregate types")
}

// TestSQLEventStore_VersionMismatch 验证 SQLEventStore VersionMismatch。
func TestSQLEventStore_VersionMismatch(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(700)

	events := []eventing.Event[int64]{
		makeEvent(aggregateID, "TestAggregate", "event-1", 5, nil),
	}
	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version mismatch")
}

// TestSQLEventStore_ConcurrencyConflict 验证 SQLEventStore ConcurrencyConflict。
func TestSQLEventStore_ConcurrencyConflict(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(999)

	events1 := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil)}
	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events1), 0)
	require.NoError(t, err)

	events2 := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-2", 1, nil)}
	err = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events2), 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "concurrency")
}

// TestSQLEventStore_ComplexPayload 验证 SQLEventStore ComplexPayload。
func TestSQLEventStore_ComplexPayload(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	type complexPayload struct {
		Total any `json:"total"`
	}

	ctx := context.Background()
	aggregateID := int64(800)

	payload := map[string]any{
		"user": map[string]any{
			"id":   123,
			"name": "Alice",
		},
		"items": []any{
			map[string]any{"id": 1, "qty": 5},
			map[string]any{"id": 2, "qty": 3},
		},
		"total": 99.99,
	}

	events := []eventing.Event[int64]{makeEvent(aggregateID, "OrderAggregate", "OrderCreated", 1, payload)}
	err := store.AppendEvents(ctx, "OrderAggregate", aggregateID, toStorableEvents(events), 0)
	assert.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "OrderAggregate", aggregateID, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 1)
	assert.NotNil(t, loaded[0].Payload)

	_, ok := messaging.PayloadAs[json.RawMessage](loaded[0].Payload)
	assert.True(t, ok)

	reg := registry.NewRegistry()
	require.NoError(t, reg.Register("TestEvent", func() any { return &complexPayload{} }))
	upgraders := upcast.NewUpgraderRegistry()
	_, err = upcast.UpgradeEventPayload(ctx, reg, upgraders, &loaded[0])
	assert.NoError(t, err)

	p, ok := messaging.PayloadAs[*complexPayload](loaded[0].Payload)
	require.True(t, ok)
	switch v := p.Total.(type) {
	case json.Number:
		f, err := v.Float64()
		assert.NoError(t, err)
		assert.InDelta(t, 99.99, f, 1e-9)
	default:
		t.Fatalf("unexpected type for payload.total: %T", p.Total)
	}
}

func TestSQLEventStore_ClassifyUniqueInsertError_Idempotent(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(1)

	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, map[string]any{"value": 100})}
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0))

	encoded, err := store.codec.Encode(aggregateID)
	require.NoError(t, err)

	idempotent, classified := store.classifyUniqueInsertError(ctx, database, aggregateID, encoded, preparedEvent{
		id:            events[0].ID,
		typ:           events[0].Type,
		aggregateType: events[0].AggregateType,
		version:       events[0].Version,
		schemaVersion: events[0].EventSchemaVersion(),
		timestamp:     events[0].Timestamp,
		payloadJSON:   `{"value":100}`,
		metadataJSON:  `{}`,
	}, errors.New("UNIQUE constraint failed: event_store.id"))
	require.True(t, idempotent)
	require.NoError(t, classified)
}

func TestMatchesPersistedTimestampAllowsDBPrecisionTruncation(t *testing.T) {
	ts := time.Date(2026, 6, 7, 12, 30, 45, 123456789, time.UTC)

	require.True(t, matchesPersistedTimestamp(ts.Truncate(time.Microsecond), ts))
	require.True(t, matchesPersistedTimestamp(ts.Truncate(time.Millisecond), ts))
	require.True(t, matchesPersistedTimestamp(ts.Truncate(time.Second), ts))
	require.False(t, matchesPersistedTimestamp(ts.Add(time.Second), ts))
}

func TestSQLEventStore_ClassifyUniqueInsertError_EventIDSameVersionDifferentPayload_Duplicate(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(1)
	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, map[string]any{"value": 100})}
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0))

	encoded, err := store.codec.Encode(aggregateID)
	require.NoError(t, err)

	idempotent, classified := store.classifyUniqueInsertError(ctx, database, aggregateID, encoded, preparedEvent{
		id:            events[0].ID,
		typ:           events[0].Type,
		aggregateType: events[0].AggregateType,
		version:       events[0].Version,
		schemaVersion: events[0].EventSchemaVersion(),
		timestamp:     events[0].Timestamp,
		payloadJSON:   `{"value":200}`,
		metadataJSON:  `{}`,
	}, errors.New("UNIQUE constraint failed: event_store.id"))
	require.False(t, idempotent)
	require.Error(t, classified)
	require.Truef(t, errors.Is(classified, errors.Duplicate), "expected Duplicate, got %v", classified)
}

func TestSQLEventStore_ClassifyUniqueInsertError_EventIDTakenBeatsAggregateVersionError(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(1)
	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, map[string]any{"value": 100})}
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0))

	encoded, err := store.codec.Encode(aggregateID)
	require.NoError(t, err)

	idempotent, classified := store.classifyUniqueInsertError(ctx, database, aggregateID, encoded, preparedEvent{
		id:            events[0].ID,
		typ:           events[0].Type,
		aggregateType: events[0].AggregateType,
		version:       events[0].Version,
		schemaVersion: events[0].EventSchemaVersion(),
		timestamp:     events[0].Timestamp,
		payloadJSON:   `{"value":200}`,
		metadataJSON:  `{}`,
	}, errors.New("UNIQUE constraint failed: event_store.aggregate_id, event_store.aggregate_type, event_store.version"))
	require.False(t, idempotent)
	require.Error(t, classified)
	require.Truef(t, errors.Is(classified, errors.Duplicate), "expected Duplicate, got %v", classified)
}

func TestSQLEventStore_ClassifyUniqueInsertError_EventIDTaken_Duplicate(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-dup", 1, nil),
	}), 0))

	encoded, err := store.codec.Encode(int64(2))
	require.NoError(t, err)

	idempotent, classified := store.classifyUniqueInsertError(ctx, database, int64(2), encoded, preparedEvent{
		id:            "event-dup",
		typ:           "TestEvent",
		aggregateType: "TestAggregate",
		version:       1,
	}, errors.New("UNIQUE constraint failed: event_store.id"))
	require.False(t, idempotent)
	require.Error(t, classified)
	require.Truef(t, errors.Is(classified, errors.Duplicate), "expected Duplicate, got %v", classified)
}

func TestSQLEventStore_ClassifyUniqueInsertError_AggregateVersionTaken_Concurrency(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TestAggregate", "event-existing", 1, nil),
	}), 0))

	encoded, err := store.codec.Encode(int64(1))
	require.NoError(t, err)

	idempotent, classified := store.classifyUniqueInsertError(ctx, database, int64(1), encoded, preparedEvent{
		id:            "event-new",
		typ:           "TestEvent",
		aggregateType: "TestAggregate",
		version:       1,
	}, errors.New("UNIQUE constraint failed: event_store.aggregate_id, event_store.aggregate_type, event_store.version"))
	require.False(t, idempotent)
	require.Error(t, classified)
	require.Truef(t, errors.Is(classified, errors.Concurrency), "expected Concurrency, got %v", classified)
}
