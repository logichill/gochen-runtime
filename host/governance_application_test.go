package host_test

import (
	"context"
	"testing"
	"time"

	"gochen-runtime/host/internal/appregistry"
	"gochen/app"
	appcrud "gochen/app/crud"
	appes "gochen/app/eventsourced"
	"gochen/app/eventsourced/eventstore"
	"gochen/auth"

	"gochen/contextx"
	"gochen/domain/crud"
	domaines "gochen/domain/eventsourced"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/gen"
	"gochen/testkit"
	"gochen/testkit/require"
)

type governanceDocument struct {
	crud.Entity[int64]
	Name string `json:"name"`
}

type governanceAuditedDocument struct {
	crud.TrackedEntity[int64]
	Name string `json:"name"`
}

type governanceAggregate struct {
	*domaines.EventSourcedAggregate[int64]
	Name string
}

type governanceCreated struct{ Name string }

func (*governanceCreated) EventType() string                               { return "GovernanceCreated" }
func (a *governanceAggregate) ApplyGovernanceCreated(e *governanceCreated) { a.Name = e.Name }

type governanceAuditStore struct {
	err     error
	entries []auth.AuthzLogEntry
}

type governanceSpecResolver struct{ err error }

func (r governanceSpecResolver) ResolveSpec(context.Context, auth.Request) (spec.Spec, []auth.Assignment, error) {
	if r.err != nil {
		return spec.Spec{}, nil, r.err
	}
	return spec.Eq(meta.ByField("Name"), "allowed"), nil, nil
}

func (governanceSpecResolver) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByField("Name")}}
}

func (s *governanceAuditStore) SaveAuthzLogEntry(_ context.Context, e auth.AuthzLogEntry) error {
	s.entries = append(s.entries, e)
	return s.err
}

func (s *governanceAuditStore) ListAuthzLogEntries(context.Context, auth.AuthzLogEntryType, int) ([]auth.AuthzLogEntry, error) {
	return s.entries, nil
}

func newGovernanceApplicationFixture(t *testing.T, kind string, authorizer auth.IAuthorizer) (app.IDescriptorProvider, func(context.Context) error, func(context.Context) int) {
	t.Helper()
	switch kind {
	case "crud":
		repo, err := testkit.NewMemoryRepository[*governanceDocument, int64](testkit.NewInt64Sequence(1))
		require.NoError(t, err)
		application, err := appcrud.NewApplication(repo, appcrud.Options[*governanceDocument, int64]{Resource: "document", Authorizer: authorizer})
		require.NoError(t, err)
		return application, func(ctx context.Context) error {
				return application.Create(ctx, &governanceDocument{Name: "allowed"})
			}, func(ctx context.Context) int {
				count, err := repo.Count(ctx, spec.All())
				require.NoError(t, err)
				return int(count)
			}
	case "audited":
		repo, err := testkit.NewAuditedMemoryRepository[*governanceAuditedDocument, int64](testkit.NewInt64Sequence(1))
		require.NoError(t, err)
		application, err := appcrud.NewApplication(repo, appcrud.Options[*governanceAuditedDocument, int64]{Resource: "document", Authorizer: authorizer, Audit: repo})
		require.NoError(t, err)
		return application, func(ctx context.Context) error {
				return application.Create(ctx, &governanceAuditedDocument{Name: "allowed"})
			}, func(ctx context.Context) int {
				count, err := repo.Count(ctx, spec.All())
				require.NoError(t, err)
				return int(count)
			}
	case "eventsourced":
		events := store.NewMemoryEventStore[int64]()
		eventRegistry := registry.NewRegistry()
		require.NoError(t, eventRegistry.Register("GovernanceCreated", func() any { return &governanceCreated{} }))
		adapter, err := eventstore.NewDomainEventStore(eventstore.DomainEventStoreOptions[int64]{
			AggregateType: "GovernanceAggregate", PublishMode: eventstore.PublishModePersistOnly,
			EventIDGenerator: gen.NewUUIDGenerator(), EventStore: events,
			EventRegistry: eventRegistry, UpgraderRegistry: upcast.NewUpgraderRegistry(),
		})
		require.NoError(t, err)
		metadata := domaines.NewMetadataRegistry()
		factory := func(id int64) (*governanceAggregate, error) {
			aggregate := &governanceAggregate{}
			base, err := domaines.InitAggregate[int64](metadata, aggregate, id, "GovernanceAggregate")
			aggregate.EventSourcedAggregate = base
			return aggregate, err
		}
		repo, err := appes.NewEventSourcedRepository(appes.RepositoryOptions[*governanceAggregate, int64]{
			AggregateType: "GovernanceAggregate", Sample: &governanceAggregate{},
			Factory: factory, Store: adapter, MetadataRegistry: metadata,
		})
		require.NoError(t, err)
		application, err := appes.NewApplication(repo, appes.Options[*governanceAggregate, int64]{Resource: "document", Authorizer: authorizer})
		require.NoError(t, err)
		return application, func(ctx context.Context) error {
				_, err := application.ExecuteNew(ctx, appes.ActionCreate, func(context.Context) (*governanceAggregate, error) {
					aggregate, err := factory(1)
					if err != nil {
						return nil, err
					}
					return aggregate, aggregate.ApplyAndRecord(&governanceCreated{Name: "allowed"})
				})
				return err
			}, func(ctx context.Context) int {
				stored, err := events.LoadEvents(ctx, "GovernanceAggregate", 1, 0)
				require.NoError(t, err)
				return len(stored)
			}
	default:
		t.Fatalf("unknown application kind %q", kind)
		return nil, nil, nil
	}
}

func TestGovernanceApplicationCombinations(t *testing.T) {
	for _, kind := range []string{"crud", "audited", "eventsourced"} {
		for _, mode := range []string{"allow", "rbac", "scoped"} {
			for _, scenario := range []string{"success", "denied", "resolver failure", "snapshot failure", "strict audit failure", "lenient audit failure", "metrics panic"} {
				if scenario == "denied" && mode == "allow" || scenario == "resolver failure" && mode != "scoped" {
					continue
				}
				t.Run(kind+"/"+mode+"/"+scenario, func(t *testing.T) {
					failure := errors.New("injected governance failure")
					checker := auth.ActionCheckerFunc(func(context.Context, string) error {
						if scenario == "denied" {
							return errors.NewCode(errors.Forbidden, "denied")
						}
						return nil
					})
					var base auth.IPolicy
					var err error
					switch mode {
					case "allow":
						base = auth.AllowPolicy()
					case "rbac":
						base, err = auth.NewPermissionPolicy(checker)
					case "scoped":
						resolver := governanceSpecResolver{}
						if scenario == "resolver failure" {
							resolver.err = failure
						}
						base, err = auth.NewPermissionPolicy(checker, auth.WithScope(resolver))
					}
					require.NoError(t, err)
					auditStore := &governanceAuditStore{}
					if scenario == "strict audit failure" || scenario == "lenient audit failure" {
						auditStore.err = failure
					}
					options := auth.GovernanceOptions{
						Snapshot: auth.PolicySnapshotFunc(func(context.Context) (auth.PolicySnapshot, error) {
							if scenario == "snapshot failure" {
								return auth.PolicySnapshot{}, failure
							}
							return auth.PolicySnapshot{Key: "policy", Version: "v1"}, nil
						}),
						Audit: auditStore,
						Metrics: auth.AuthzMetricsFunc(func(context.Context, string, string, time.Duration) {
							if scenario == "metrics panic" {
								panic("injected metrics panic")
							}
						}),
					}
					if scenario == "strict audit failure" {
						options.StrictAudit = true
					}
					governed, err := auth.New(auth.Options{Policy: base, Governance: options})
					require.NoError(t, err)
					application, write, count := newGovernanceApplicationFixture(t, kind, governed)
					startup := appregistry.NewRegistry()
					require.NoError(t, startup.Register(application))
					if mode == "allow" {
						require.Equal(t, []string{"document"}, startup.AllowResources())
					} else {
						require.Empty(t, startup.AllowResources())
					}
					ctx, err := contextx.WithOperator(context.Background(), "alice")
					require.NoError(t, err)
					err = write(ctx)
					switch scenario {
					case "denied":
						require.True(t, errors.Is(err, errors.Forbidden))
						require.Equal(t, 0, count(ctx))
					case "resolver failure", "snapshot failure", "strict audit failure":
						require.True(t, errors.Is(err, failure))
						require.Equal(t, 0, count(ctx))
					default:
						require.NoError(t, err)
						require.Equal(t, 1, count(ctx))
					}
					require.Len(t, auditStore.entries, 1)
					if scenario == "success" {
						require.Equal(t, "v1", auditStore.entries[0].SnapshotVersion)
						require.Equal(t, "allow", auditStore.entries[0].Effect)
					}
				})
			}
		}
	}
}
