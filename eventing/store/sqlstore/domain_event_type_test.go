package sqlstore

import (
	"testing"

	appes "gochen/app/eventsourced"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/db"
	"gochen/domain"
	es "gochen/domain/eventsourced"
	"gochen/errors"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen/uuid"
	"gochen/testkit/require"
)

func TestDomainEventStoreTypeValidationMemoryAndSQLite(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			var database db.IDatabase
			var persisted store.IEventStreamStore[int64] = store.NewMemoryEventStore[int64]()
			if backend == "sqlite" {
				database = setupTestDB(t)
				t.Cleanup(func() { require.NoError(t, database.Close()) })
				persisted = newTestStore(t, database, "event_store")
			}
			reg := registry.NewRegistry()
			require.NoError(t, reg.Register("CounterAdded", func() any { return &counterAdded{} }))
			opts := esstore.DomainEventStoreOptions[int64]{
				AggregateType: "Counter", EventIDGenerator: uuid.NewGenerator(), EventStore: persisted,
				EventRegistry: reg, UpgraderRegistry: upcast.NewUpgraderRegistry(), PublishMode: esstore.PublishModePersistOnly,
			}
			stream, err := esstore.NewEventStream(opts)
			require.NoError(t, err)
			assertEmpty := func() {
				t.Helper()
				for _, id := range []int64{1, 2} {
					version, err := persisted.GetAggregateVersion(ctx, "Counter", id)
					require.NoError(t, err)
					require.Equal(t, uint64(0), version)
				}
				if database != nil {
					var count int
					require.NoError(t, database.QueryRow(ctx, "SELECT COUNT(*) FROM event_store").Scan(&count))
					require.Equal(t, 0, count)
				}
			}
			for _, events := range [][]domain.IDomainEvent{
				{unrelatedCounterAdded{Amount: 99}},
				{&unrelatedCounterAdded{Amount: 99}},
				{counterAdded{Amount: 1}, &unrelatedCounterAdded{Amount: 99}},
			} {
				err := stream.AppendEvents(ctx, 1, events, 0)
				require.True(t, errors.Is(err, errors.InvalidInput))
				assertEmpty()
			}
			atomic, err := esstore.NewAtomicDomainEventStore(opts)
			require.NoError(t, err)
			batch := []es.DomainEventAppend[int64]{
				{AggregateID: 1, Events: []domain.IDomainEvent{counterAdded{Amount: 1}}},
				{AggregateID: 2, Events: []domain.IDomainEvent{&counterAdded{Amount: 2}, &unrelatedCounterAdded{Amount: 99}}},
			}
			err = atomic.AppendEventsBatch(ctx, batch)
			require.True(t, errors.Is(err, errors.InvalidInput))
			assertEmpty()

			// 同一个已注册类型的值和指针都可以追加；失败批次不消耗聚合版本。
			require.NoError(t, stream.AppendEvents(ctx, 1, []domain.IDomainEvent{counterAdded{Amount: 3}, &counterAdded{Amount: 4}}, 0))
			batch[0].ExpectedVersion = 2
			batch[1].Events[1] = counterAdded{Amount: 5}
			require.NoError(t, atomic.AppendEventsBatch(ctx, batch))
			valueRepo, err := appes.NewStateRepository(es.Model[int, counterAdded]{
				Initial: func() int { return 0 }, Clone: func(v int) int { return v },
				Reduce: func(v int, e counterAdded) (int, error) { return v + e.Amount, nil },
			}, stream)
			require.NoError(t, err)
			pointerRepo, err := appes.NewStateRepository(es.Model[int, *counterAdded]{
				Initial: func() int { return 0 }, Clone: func(v int) int { return v },
				Reduce: func(v int, e *counterAdded) (int, error) { return v + e.Amount, nil },
			}, stream)
			require.NoError(t, err)
			for _, want := range []struct {
				id      int64
				value   int
				version uint64
			}{{id: 1, value: 8, version: 3}, {id: 2, value: 7, version: 2}} {
				value, err := valueRepo.Load(ctx, want.id)
				require.NoError(t, err)
				require.Equal(t, want.value, value.Value())
				require.Equal(t, want.version, value.Version())
				pointer, err := pointerRepo.Load(ctx, want.id)
				require.NoError(t, err)
				require.Equal(t, want.value, pointer.Value())
				require.Equal(t, want.version, pointer.Version())
			}
		})
	}
}
