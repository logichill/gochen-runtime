package sqlstore

import (
	"context"
	"testing"

	"gochen-runtime/db/sql/stdsql"
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

	_ "modernc.org/sqlite"
)

type counterAdded struct {
	Amount int `json:"amount"`
}

func (counterAdded) EventType() string { return "CounterAdded" }

// 相同 wire 名称不代表相同 Go 事件类型，不允许按名称绕过模型类型边界。
type unrelatedCounterAdded struct {
	Amount int `json:"amount"`
}

func (unrelatedCounterAdded) EventType() string { return "CounterAdded" }

func TestExplicitValueEventMemoryAndSQLRoundtrip(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			var events esstore.IAggregateEventStore[int64] = store.NewMemoryEventStore[int64]()
			if backend == "sqlite" {
				database, err := stdsql.New(db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, database.Close()) })
				_, err = database.Exec(ctx, `CREATE TABLE event_store (
					id TEXT PRIMARY KEY, type TEXT NOT NULL, aggregate_id INTEGER NOT NULL,
					aggregate_type TEXT NOT NULL, version INTEGER NOT NULL,
					schema_version INTEGER NOT NULL, timestamp DATETIME NOT NULL,
					payload TEXT NOT NULL, metadata TEXT NOT NULL,
					UNIQUE(aggregate_id, aggregate_type, version))`)
				require.NoError(t, err)
				events, err = NewSQLEventStore(database, "event_store")
				require.NoError(t, err)
			}
			registry := registry.NewRegistry()
			require.NoError(t, registry.Register("CounterAdded", func() any { return &counterAdded{} }))
			stream, err := esstore.NewEventStream(esstore.DomainEventStoreOptions[int64]{
				AggregateType: "Counter", EventIDGenerator: uuid.NewGenerator(), EventStore: events,
				EventRegistry: registry, UpgraderRegistry: upcast.NewUpgraderRegistry(), PublishMode: esstore.PublishModePersistOnly,
			})
			require.NoError(t, err)
			model := es.Model[int, counterAdded]{Initial: func() int { return 0 }, Clone: func(v int) int { return v }, Reduce: func(v int, e counterAdded) (int, error) { return v + e.Amount, nil }}
			repository, err := appes.NewStateRepository(model, stream)
			require.NoError(t, err)
			base, err := repository.Load(ctx, 1)
			require.NoError(t, err)
			committed, err := repository.Execute(ctx, base, func(context.Context, int) ([]counterAdded, error) { return []counterAdded{{Amount: 7}}, nil })
			require.NoError(t, err)
			replayed, err := repository.Load(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, 7, replayed.Value())
			require.Equal(t, committed.Value(), replayed.Value())
			require.Equal(t, uint64(1), replayed.Version())
			_, err = repository.Execute(ctx, base, func(context.Context, int) ([]counterAdded, error) { return []counterAdded{{Amount: 99}}, nil })
			require.True(t, errors.Is(err, errors.Concurrency))
			// 反向选择不相干的事件模型，必须失败，不能仅因 EventType 相同就转换。
			wrong, err := appes.NewStateRepository(es.Model[int, unrelatedCounterAdded]{Initial: func() int { return 0 }, Clone: func(v int) int { return v }, Reduce: func(v int, e unrelatedCounterAdded) (int, error) { return v + e.Amount, nil }}, stream)
			require.NoError(t, err)
			_, err = wrong.Load(ctx, 1)
			require.True(t, errors.Is(err, errors.InvalidInput))
			// 指针事件和事件接口模型仍直接接收原表示。
			pointer, err := appes.NewStateRepository(es.Model[int, *counterAdded]{Initial: func() int { return 0 }, Clone: func(v int) int { return v }, Reduce: func(v int, e *counterAdded) (int, error) { return v + e.Amount, nil }}, stream)
			require.NoError(t, err)
			pointed, err := pointer.Load(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, 7, pointed.Value())
			polymorphic, err := appes.NewStateRepository(es.Model[int, domain.IDomainEvent]{Initial: func() int { return 0 }, Clone: func(v int) int { return v }, Reduce: func(v int, e domain.IDomainEvent) (int, error) { return v + 1, nil }}, stream)
			require.NoError(t, err)
			value, err := polymorphic.Load(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, 1, value.Value())
		})
	}
}
