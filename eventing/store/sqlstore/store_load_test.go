package sqlstore

import (
	"context"
	"fmt"
	"testing"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/store"
	"gochen/observe/logging"
)

// TestSQLEventStore_LoadEventsUsesAggregateType 验证聚合类型参与事件流身份定位。
func TestSQLEventStore_LoadEventsUsesAggregateType(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()

	events1 := []eventing.Event[int64]{makeEvent(1, "TypeA", "event-1", 1, nil)}
	events2 := []eventing.Event[int64]{makeEvent(2, "TypeB", "event-2", 1, nil)}

	assert.NoError(t, store.AppendEvents(ctx, "TypeA", 1, toStorableEvents(events1), 0))
	assert.NoError(t, store.AppendEvents(ctx, "TypeB", 2, toStorableEvents(events2), 0))

	loaded, err := store.LoadEvents(ctx, "TypeA", 1, 0)
	assert.NoError(t, err)
	assert.Len(t, loaded, 1)
	assert.Equal(t, "TypeA", loaded[0].AggregateType)
}

func TestSQLEventStore_AllowsReusingAggregateIDAcrossTypes(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	require.NoError(t, store.AppendEvents(ctx, "TypeA", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TypeA", "event-a", 1, nil),
	}), 0))
	require.NoError(t, store.AppendEvents(ctx, "TypeB", 1, toStorableEvents([]eventing.Event[int64]{
		makeEvent(1, "TypeB", "event-b", 1, nil),
	}), 0))

	typeA, err := store.LoadEvents(ctx, "TypeA", 1, 0)
	require.NoError(t, err)
	require.Len(t, typeA, 1)
	require.Equal(t, "event-a", typeA[0].ID)

	typeB, err := store.LoadEvents(ctx, "TypeB", 1, 0)
	require.NoError(t, err)
	require.Len(t, typeB, 1)
	require.Equal(t, "event-b", typeB[0].ID)

	exists, err := store.HasAggregate(ctx, "TypeA", 1)
	require.NoError(t, err)
	require.True(t, exists)
}

// TestSQLEventStore_LoadEventsAfterVersion 验证 SQLEventStore LoadEventsAfterVersion。
func TestSQLEventStore_LoadEventsAfterVersion(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(400)

	events := []eventing.Event[int64]{
		makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil),
		makeEvent(aggregateID, "TestAggregate", "event-2", 2, nil),
		makeEvent(aggregateID, "TestAggregate", "event-3", 3, nil),
		makeEvent(aggregateID, "TestAggregate", "event-4", 4, nil),
		makeEvent(aggregateID, "TestAggregate", "event-5", 5, nil),
	}
	err := store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	require.NoError(t, err)

	loaded, err := store.LoadEvents(ctx, "TestAggregate", aggregateID, 2)
	assert.NoError(t, err)
	assert.Len(t, loaded, 3)
	assert.Equal(t, uint64(3), loaded[0].Version)
	assert.Equal(t, uint64(5), loaded[2].Version)
}

// TestSQLEventStore_HasAggregate 验证 SQLEventStore HasAggregate。
func TestSQLEventStore_HasAggregate(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(200)

	exists, err := store.HasAggregate(ctx, "TestAggregate", aggregateID)
	assert.NoError(t, err)
	assert.False(t, exists)

	events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil)}
	err = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	require.NoError(t, err)

	exists, err = store.HasAggregate(ctx, "TestAggregate", aggregateID)
	assert.NoError(t, err)
	assert.True(t, exists)
}

// TestSQLEventStore_GetAggregateVersion 验证 SQLEventStore GetAggregateVersion。
func TestSQLEventStore_GetAggregateVersion(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	aggregateID := int64(300)

	version, err := store.GetAggregateVersion(ctx, "TestAggregate", aggregateID)
	assert.NoError(t, err)
	assert.Equal(t, uint64(0), version)

	events := []eventing.Event[int64]{
		makeEvent(aggregateID, "TestAggregate", "event-1", 1, nil),
		makeEvent(aggregateID, "TestAggregate", "event-2", 2, nil),
		makeEvent(aggregateID, "TestAggregate", "event-3", 3, nil),
	}
	err = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	require.NoError(t, err)

	version, err = store.GetAggregateVersion(ctx, "TestAggregate", aggregateID)
	assert.NoError(t, err)
	assert.Equal(t, uint64(3), version)
}

func TestSQLEventStore_StreamAggregate_ClampsOversizedLimit(t *testing.T) {
	database := setupTestDB(t)
	eventStore := newTestStore(t, database, "event_store")

	ctx := context.Background()
	for i := 1; i <= store.MaxStreamLimit+5; i++ {
		err := eventStore.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{
			makeEvent(1, "TestAggregate", fmt.Sprintf("event-%d", i), uint64(i), nil),
		}), uint64(i-1))
		require.NoError(t, err)
	}

	result, err := eventStore.StreamAggregate(ctx, &store.AggregateStreamOptions[int64]{
		AggregateType: "TestAggregate",
		AggregateID:   1,
		Limit:         store.MaxStreamLimit + 500,
	})
	require.NoError(t, err)
	require.Len(t, result.Events, store.MaxStreamLimit)
	require.True(t, result.HasMore)
}

func TestSQLEventStore_StreamAggregate_RequiresAggregateType(t *testing.T) {
	database := setupTestDB(t)
	eventStore := newTestStore(t, database, "event_store")

	_, err := eventStore.StreamAggregate(context.Background(), &store.AggregateStreamOptions[int64]{AggregateID: 1})
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
}

func TestSQLEventStore_GetAggregateVersionRequiresAggregateType(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	for _, aggregateType := range []string{"", "   "} {
		_, err := store.GetAggregateVersion(context.Background(), aggregateType, int64(1))
		require.Error(t, err)
		require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
	}
}

func TestSQLEventStore_NormalizesAggregateTypeAcrossOperations(t *testing.T) {
	database := setupTestDB(t)
	eventStore := newTestStore(t, database, "event_store")
	ctx := context.Background()
	events := []eventing.Event[int64]{makeEvent(1, " Aggregate ", "event-1", 1, nil)}

	require.NoError(t, eventStore.AppendEvents(ctx, " Aggregate ", 1, toStorableEvents(events), 0))

	loaded, err := eventStore.LoadEvents(ctx, " Aggregate ", 1, 0)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "Aggregate", loaded[0].GetAggregateType())

	version, err := eventStore.GetAggregateVersion(ctx, " Aggregate ", 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), version)

	streamed, err := eventStore.StreamAggregate(ctx, &store.AggregateStreamOptions[int64]{AggregateType: " Aggregate ", AggregateID: 1})
	require.NoError(t, err)
	require.Len(t, streamed.Events, 1)
}

// TestSQLEventStore_Init 验证 SQLEventStore Init。
func TestSQLEventStore_Init(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	ctx := context.Background()
	err := store.Init(ctx)
	assert.NoError(t, err)
}

// TestSQLEventStore_Getters 验证 SQLEventStore Getters。
func TestSQLEventStore_Getters(t *testing.T) {
	database := setupTestDB(t)
	store := newTestStore(t, database, "event_store")

	assert.Equal(t, "event_store", store.GetTableName())
	assert.NotNil(t, store.GetDB())
	assert.Equal(t, database, store.GetDB())
}

// TestSQLEventStore_DefaultTableName 验证 SQLEventStore DefaultTableName。
func TestSQLEventStore_DefaultTableName(t *testing.T) {
	database := setupTestDB(t)

	store := newTestStore(t, database, "")
	assert.Equal(t, "event_store", store.GetTableName())
}

// TestSQLEventStore_TableNameValidation 验证 SQLEventStore TableNameValidation。
func TestSQLEventStore_TableNameValidation(t *testing.T) {
	database := setupTestDB(t)

	_, err := NewSQLEventStore(database, "event_store;DROP TABLE event_store;", WithLogger(logging.NewNoopLogger()))
	require.Error(t, err)

	_, err = NewSQLEventStore(database, "1event_store", WithLogger(logging.NewNoopLogger()))
	require.Error(t, err)
}
