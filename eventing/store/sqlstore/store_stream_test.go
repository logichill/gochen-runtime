package sqlstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/store"
)

// TestSQLEventStore_StreamEvents 验证 SQLEventStore StreamEvents。
func TestSQLEventStore_StreamEvents(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	events1 := []eventing.Event[int64]{makeEvent(1, "TypeA", "event-1", 1, nil)}
	err := es.AppendEvents(ctx, "TypeA", 1, toStorableEvents(events1), 0)
	require.NoError(t, err)

	events2 := []eventing.Event[int64]{makeEvent(2, "TypeB", "event-2", 1, nil)}
	err = es.AppendEvents(ctx, "TypeB", 2, toStorableEvents(events2), 0)
	require.NoError(t, err)

	loaded, err := es.StreamEvents(ctx, &store.StreamOptions{
		FromTime: events1[0].Timestamp.Add(-1 * time.Second),
		Limit:    10,
	})
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(loaded.Events), 2)
}

// TestSQLEventStore_StreamEvents_PaginationAndFilters 验证 SQLEventStore StreamEvents PaginationAndFilters。
func TestSQLEventStore_StreamEvents_PaginationAndFilters(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		events := []eventing.Event[int64]{makeEvent(int64(i), "TestAggregate", fmt.Sprintf("event-%d", i), 1, nil)}
		err := es.AppendEvents(ctx, "TestAggregate", int64(i), toStorableEvents(events), 0)
		require.NoError(t, err)
	}

	t.Run("基础分页", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			Limit: 2,
		})
		assert.NoError(t, err)
		assert.Len(t, result.Events, 2)
		assert.True(t, result.HasMore)
		assert.NotEmpty(t, result.NextCursor)
	})

	t.Run("使用游标", func(t *testing.T) {
		result1, err := es.StreamEvents(ctx, &store.StreamOptions{
			Limit: 2,
		})
		require.NoError(t, err)

		result2, err := es.StreamEvents(ctx, &store.StreamOptions{
			After: result1.NextCursor,
			Limit: 2,
		})
		assert.NoError(t, err)
		assert.Greater(t, len(result2.Events), 0)
	})

	t.Run("按类型过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			AggregateTypes: []string{"TestAggregate"},
			Limit:          10,
		})
		assert.NoError(t, err)
		assert.Greater(t, len(result.Events), 0)
		for _, evt := range result.Events {
			assert.Equal(t, "TestAggregate", evt.AggregateType)
		}
	})
}

func TestSQLEventStore_StreamEvents_StopsAtGapHorizon(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	es := newTestStore(t, database, "event_store")
	ctx := context.Background()
	ts := time.Now()

	insert := func(id string, aggregateID int64, position int64) {
		_, err := database.Exec(ctx,
			`INSERT INTO event_store (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "TestEvent", aggregateID, "TestAggregate", 1, 1, position, ts, "{}", "{}")
		require.NoError(t, err)
	}

	// 模拟在途事务：position 1 与 3 已提交，position 2 仍缺失（更晚提交但更早分配）。
	insert("evt-pos-1", 1, 1)
	insert("evt-pos-3", 3, 3)

	// horizon=1：游标不得越过空洞读到 position 3，否则 position 2 提交后将被永久跳过。
	page, err := es.StreamEvents(ctx, &store.StreamOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, "evt-pos-1", page.Events[0].ID)

	// 空洞补齐后，position 2 与 3 一并可见。
	insert("evt-pos-2", 2, 2)
	es.invalidateStreamHorizonCache()
	next, err := es.StreamEvents(ctx, &store.StreamOptions{After: page.NextCursor, Limit: 10})
	require.NoError(t, err)
	require.Len(t, next.Events, 2)
	require.Equal(t, "evt-pos-2", next.Events[0].ID)
	require.Equal(t, "evt-pos-3", next.Events[1].ID)
}

func TestSQLEventStore_StreamEvents_UsesGlobalPositionCursor(t *testing.T) {
	database := setupTestDB(t)
	addGlobalPositionColumnAndIndex(t, database)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()
	ts := time.Now()
	first := makeEvent(1, "TestAggregate", "z-event", 1, nil)
	first.Timestamp = ts
	second := makeEvent(2, "TestAggregate", "a-event", 1, nil)
	second.Timestamp = ts

	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{first}), 0))
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{second}), 0))

	page1, err := es.StreamEvents(ctx, &store.StreamOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page1.Events, 1)
	require.Equal(t, "z-event", page1.Events[0].ID)
	require.Equal(t, int64(1), page1.Events[0].GlobalPosition)

	page2, err := es.StreamEvents(ctx, &store.StreamOptions{After: page1.NextCursor, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page2.Events, 1)
	require.Equal(t, "a-event", page2.Events[0].ID)
	require.Equal(t, int64(2), page2.Events[0].GlobalPosition)
}

func TestSQLEventStore_StreamEvents_GlobalPositionColumnWithNullRowsFallsBack(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()
	first := makeEvent(1, "TestAggregate", "event-before-column", 1, nil)
	first.ID = "event-before-column"
	first.Timestamp = time.Now()
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{first}), 0))
	addGlobalPositionColumnAndIndex(t, database)
	expireGlobalPositionProbe(es)

	second := makeEvent(2, "TestAggregate", "event-after-column", 1, nil)
	second.ID = "event-after-column"
	second.Timestamp = first.Timestamp.Add(time.Second)
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{second}), 0))

	result, err := es.StreamEvents(ctx, &store.StreamOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.Equal(t, "event-before-column", result.Events[0].ID)
	require.Equal(t, int64(0), result.Events[0].GlobalPosition)

	next, err := es.StreamEvents(ctx, &store.StreamOptions{After: result.NextCursor, Limit: 1})
	require.NoError(t, err)
	require.Len(t, next.Events, 1)
	require.Equal(t, "event-after-column", next.Events[0].ID)
	require.Equal(t, int64(1), next.Events[0].GlobalPosition)
}

func TestSQLEventStore_StreamEvents_NullCursorIncludesPositionedRowsEvenWithEarlierTimestamp(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()
	base := time.Now()
	cursor := makeEvent(1, "TestAggregate", "z-cursor", 1, nil)
	cursor.ID = "z-cursor"
	cursor.Timestamp = base
	laterNull := makeEvent(2, "TestAggregate", "a-later-null", 1, nil)
	laterNull.ID = "a-later-null"
	laterNull.Timestamp = base.Add(time.Second)
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents([]eventing.Event[int64]{cursor}), 0))
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 2, toStorableEvents([]eventing.Event[int64]{laterNull}), 0))

	addGlobalPositionColumnAndIndex(t, database)
	expireGlobalPositionProbe(es)

	positioned := makeEvent(3, "TestAggregate", "positioned", 1, nil)
	positioned.ID = "positioned"
	positioned.Timestamp = base.Add(-time.Second)
	require.NoError(t, es.AppendEvents(ctx, "TestAggregate", 3, toStorableEvents([]eventing.Event[int64]{positioned}), 0))

	result, err := es.StreamEvents(ctx, &store.StreamOptions{After: cursor.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Events, 2)
	require.Equal(t, "a-later-null", result.Events[0].ID)
	require.Equal(t, "positioned", result.Events[1].ID)
}

func TestSQLEventStore_StreamEvents_ClampsOversizedLimit(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")
	ctx := context.Background()

	for i := 1; i <= store.MaxStreamLimit+5; i++ {
		events := []eventing.Event[int64]{makeEvent(int64(i), "TestAggregate", fmt.Sprintf("event-%d", i), 1, nil)}
		err := es.AppendEvents(ctx, "TestAggregate", int64(i), toStorableEvents(events), 0)
		require.NoError(t, err)
	}

	result, err := es.StreamEvents(ctx, &store.StreamOptions{Limit: store.MaxStreamLimit + 500})
	require.NoError(t, err)
	require.Len(t, result.Events, store.MaxStreamLimit)
	require.True(t, result.HasMore)
}

// TestSQLEventStore_StreamEvents_TimeFilters 验证 SQLEventStore StreamEvents TimeFilters。
func TestSQLEventStore_StreamEvents_TimeFilters(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	now := time.Now()
	for i := 1; i <= 5; i++ {
		evt := makeEvent(int64(i), "TestAggregate", fmt.Sprintf("time-event-%d", i), 1, nil)
		evt.Timestamp = now.Add(time.Duration(i) * time.Second)
		err := es.AppendEvents(ctx, "TestAggregate", int64(i), toStorableEvents([]eventing.Event[int64]{evt}), 0)
		require.NoError(t, err)
	}

	t.Run("FromTime过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			FromTime: now.Add(3 * time.Second),
			Limit:    10,
		})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(result.Events), 3)
	})

	t.Run("ToTime过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			ToTime: now.Add(3 * time.Second),
			Limit:  10,
		})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(result.Events), 3)
	})

	t.Run("时间范围过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			FromTime: now.Add(2 * time.Second),
			ToTime:   now.Add(4 * time.Second),
			Limit:    10,
		})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(result.Events), 2)
	})
}

// TestSQLEventStore_StreamEvents_TypeFilters 验证 SQLEventStore StreamEvents TypeFilters。
func TestSQLEventStore_StreamEvents_TypeFilters(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	events := []eventing.Event[int64]{
		makeEvent(1, "OrderAggregate", "order-1", 1, nil),
		makeEvent(2, "UserAggregate", "user-1", 1, nil),
		makeEvent(3, "ProductAggregate", "product-1", 1, nil),
	}
	for i, evt := range events {
		evt.Type = fmt.Sprintf("Event%d", i+1)
		err := es.AppendEvents(ctx, evt.AggregateType, int64(i+1), toStorableEvents([]eventing.Event[int64]{evt}), 0)
		require.NoError(t, err)
	}

	t.Run("按事件类型过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			Types: []string{"Event1", "Event2"},
			Limit: 10,
		})
		assert.NoError(t, err)
		assert.Equal(t, 2, len(result.Events))
	})

	t.Run("按聚合类型过滤", func(t *testing.T) {
		result, err := es.StreamEvents(ctx, &store.StreamOptions{
			AggregateTypes: []string{"OrderAggregate", "ProductAggregate"},
			Limit:          10,
		})
		assert.NoError(t, err)
		assert.Equal(t, 2, len(result.Events))
	})
}

// TestSQLEventStore_StreamEvents_EmptyResult 验证 SQLEventStore StreamEvents EmptyResult。
func TestSQLEventStore_StreamEvents_EmptyResult(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	result, err := es.StreamEvents(ctx, &store.StreamOptions{
		Limit: 10,
	})
	assert.NoError(t, err)
	assert.Len(t, result.Events, 0)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

// TestSQLEventStore_StreamEvents_InvalidCursor 验证 SQLEventStore StreamEvents InvalidCursor。
func TestSQLEventStore_StreamEvents_InvalidCursor(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	events := []eventing.Event[int64]{makeEvent(1, "TestAggregate", "event-1", 1, nil)}
	err := es.AppendEvents(ctx, "TestAggregate", 1, toStorableEvents(events), 0)
	require.NoError(t, err)

	_, err = es.StreamEvents(ctx, &store.StreamOptions{
		After: "non-existent-cursor-id",
		Limit: 10,
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, errors.NotFound))
}

func TestSQLEventStore_StreamEvents_WithOptions(t *testing.T) {
	database := setupTestDB(t)
	es := newTestStore(t, database, "event_store")

	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		events := []eventing.Event[int64]{makeEvent(int64(i), "TypeA", fmt.Sprintf("event-a-%d", i), 1, nil)}
		err := es.AppendEvents(ctx, "TypeA", int64(i), toStorableEvents(events), 0)
		require.NoError(t, err)
	}
	for i := 4; i <= 6; i++ {
		events := []eventing.Event[int64]{makeEvent(int64(i), "TypeB", fmt.Sprintf("event-b-%d", i), 1, nil)}
		err := es.AppendEvents(ctx, "TypeB", int64(i), toStorableEvents(events), 0)
		require.NoError(t, err)
	}

	result, err := es.StreamEvents(ctx, &store.StreamOptions{
		AggregateTypes: []string{"TypeA"},
		Limit:          10,
	})
	assert.NoError(t, err)
	assert.Len(t, result.Events, 3)
	for _, evt := range result.Events {
		assert.Equal(t, "TypeA", evt.AggregateType)
	}
}
