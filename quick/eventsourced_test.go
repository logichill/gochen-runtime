package quick_test

import (
	"context"
	"testing"

	"gochen-runtime/quick"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/contextx"
	deventsourced "gochen/domain/eventsourced"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/bus"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen"
)

type balanceChanged struct{ Amount int }

func (*balanceChanged) EventType() string { return "BalanceChanged" }

type account struct {
	*deventsourced.EventSourcedAggregate[int64]
	Balance int
}

func (a *account) ApplyBalanceChanged(e *balanceChanged) { a.Balance += e.Amount }

func accountOptions(t *testing.T) quick.EventSourcedOptions[*account, int64] {
	t.Helper()
	metadata := deventsourced.NewMetadataRegistry()
	reg := registry.NewRegistry()
	if err := reg.Register("BalanceChanged", func() any { return &balanceChanged{} }); err != nil {
		t.Fatal(err)
	}
	return quick.EventSourcedOptions[*account, int64]{
		Store: esstore.DomainEventStoreOptions[int64]{
			AggregateType: "Account", EventStore: store.NewMemoryEventStore[int64](),
			EventIDGenerator: gen.NewUUIDGenerator(), PublishMode: esstore.PublishModePersistOnly,
			EventRegistry: reg, UpgraderRegistry: upcast.NewUpgraderRegistry(),
		},
		Sample: &account{}, MetadataRegistry: metadata,
		Factory: func(id int64) (*account, error) {
			a := &account{}
			base, err := deventsourced.InitAggregate(metadata, a, id, "Account")
			if err != nil {
				return nil, err
			}
			a.EventSourcedAggregate = base
			return a, nil
		},
	}
}

func TestEventSourcedAssemblyPersistsAndReplays(t *testing.T) {
	repo, err := quick.EventSourced(accountOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := repo.GetOrCreate(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyAndRecord(&balanceChanged{Amount: 25}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Balance != 25 || loaded.GetVersion() != 1 || len(a.GetUncommittedEvents()) != 0 {
		t.Fatalf("replay balance=%d version=%d pending=%d", loaded.Balance, loaded.GetVersion(), len(a.GetUncommittedEvents()))
	}
	if _, ok := any(repo).(deventsourced.IAtomicEventSourcedRepository[*account, int64]); ok {
		t.Fatal("ordinary repository advertised atomic capability")
	}
}

type aggregateBackend struct {
	esstore.IAggregateEventStore[int64]
}

func TestAtomicEventSourcedAssemblyValidatesCapabilitiesAndRollsBack(t *testing.T) {
	opts := accountOptions(t)
	repo, err := quick.AtomicEventSourced(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := repo.GetOrCreate(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := repo.GetOrCreate(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetOrCreate(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*account{first, stale, current} {
		if err := a.ApplyAndRecord(&balanceChanged{Amount: 10}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAll(ctx, []*account{first, stale}); !errors.Is(err, errors.Concurrency) {
		t.Fatalf("stale atomic save = %v", err)
	}
	exists, err := repo.Exists(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if exists || len(first.GetUncommittedEvents()) != 1 {
		t.Fatal("failed batch wrote or cleared pending events")
	}
	if err := repo.SaveAll(ctx, []*account{first, current}); err != nil {
		t.Fatal(err)
	}
	if len(first.GetUncommittedEvents()) != 0 {
		t.Fatal("successful batch did not clear pending events")
	}

	opts.Store.EventStore = aggregateBackend{opts.Store.EventStore}
	if _, err := quick.AtomicEventSourced(opts); !errors.Is(err, errors.Unsupported) {
		t.Fatalf("non-atomic backend = %v", err)
	}
	opts.Store.PublishMode = ""
	if _, err := quick.EventSourced(opts); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("missing publish mode = %v", err)
	}
}

type failingPublisher struct{ bus.IEventBus }

func (*failingPublisher) PublishEvents(context.Context, []eventing.IEvent) error {
	return errors.New("publisher unavailable")
}

func TestEventSourcedAssemblyRetainsAfterCommitSemantics(t *testing.T) {
	opts := accountOptions(t)
	opts.Store.PublishMode = esstore.PublishModeDirect
	opts.Store.EventBus = &failingPublisher{}
	repo, err := quick.EventSourced(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := repo.GetOrCreate(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyAndRecord(&balanceChanged{Amount: 5}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, a); !contextx.IsAfterCommitError(err) {
		t.Fatalf("publication failure = %v", err)
	}
	if len(a.GetUncommittedEvents()) != 0 {
		t.Fatal("committed events must not remain pending")
	}
	loaded, err := repo.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Balance != 5 {
		t.Fatalf("persisted balance = %d", loaded.Balance)
	}
	if _, err := quick.AtomicEventSourced(opts); !errors.Is(err, errors.Unsupported) {
		t.Fatalf("direct atomic mode = %v", err)
	}
}
