package sqlstore

import (
	"context"
	"fmt"
	"testing"

	"gochen/eventing"
)

// BenchmarkSQLEventStore_AppendEvents 用于评估 SQLEventStore AppendEvents 的性能。
func BenchmarkSQLEventStore_AppendEvents(b *testing.B) {
	database := setupTestDB(b)
	store := newTestStore(b, database, "event_store")

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggregateID := int64(i)
		events := []eventing.Event[int64]{makeEvent(aggregateID, "TestAggregate", fmt.Sprintf("event-%d", i), 1, nil)}
		_ = store.AppendEvents(ctx, "TestAggregate", aggregateID, toStorableEvents(events), 0)
	}
}
