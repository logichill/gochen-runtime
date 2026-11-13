package sqlstore

import (
	"context"
	"testing"

	"gochen/app"
	appes "gochen/app/eventsourced"
	esstore "gochen/app/eventsourced/eventstore"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	es "gochen/domain/eventsourced"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/eventing/registry"
	"gochen/eventing/upcast"
	"gochen/gen/uuid"
	"gochen/testkit/require"
)

type secureState struct {
	Tenant     *string
	Department string
	Amount     int
}
type secureStateView struct {
	ID         int64 `entity:"id"`
	Tenant     *string
	Department string `dim:"department"`
}
type secureStateSet struct {
	Tenant     *string
	Department string
	Amount     int
}

func (secureStateSet) EventType() string { return "SecureStateSet" }

type secureStateScope struct{}

func (secureStateScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension("department")}, Assignable: []meta.Dimension{"department"}}
}
func (secureStateScope) ResolveSpec(_ context.Context, request auth.Request) (spec.Spec, []auth.Assignment, error) {
	var assign []auth.Assignment
	if request.Action == "create" {
		assign = []auth.Assignment{{Dimension: "department", Value: "ops"}}
	}
	return spec.Eq(meta.ByDimension("department"), "ops"), assign, nil
}

// 真实 SQLite 的序列化、注册类型、授权投影、提交前检查和重放共用一条链路。
func TestStateApplicationSQLiteAuthorizationAndReplay(t *testing.T) {
	database := setupTestDB(t)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	persisted := newTestStore(t, database, "event_store")
	reg := registry.NewRegistry()
	require.NoError(t, reg.Register("SecureStateSet", func() any { return &secureStateSet{} }))
	stream, err := esstore.NewEventStream(esstore.DomainEventStoreOptions[int64]{AggregateType: "SecureState", EventIDGenerator: uuid.NewGenerator(), EventStore: persisted, EventRegistry: reg, UpgraderRegistry: upcast.NewUpgraderRegistry(), PublishMode: esstore.PublishModePersistOnly})
	require.NoError(t, err)
	clone := func(state secureState) secureState {
		if state.Tenant != nil {
			tenant := *state.Tenant
			state.Tenant = &tenant
		}
		return state
	}
	repository, err := appes.NewStateRepository(es.Model[secureState, domain.IDomainEvent]{Initial: func() secureState { return secureState{} }, Clone: clone, Reduce: func(_ secureState, event domain.IDomainEvent) (secureState, error) {
		var e secureStateSet
		switch value := event.(type) {
		case secureStateSet:
			e = value
		case *secureStateSet:
			e = *value
		default:
			return secureState{}, errors.NewCode(errors.InvalidInput, "unknown secure event")
		}
		return clone(secureState{Tenant: e.Tenant, Department: e.Department, Amount: e.Amount}), nil
	}}, stream)
	require.NoError(t, err)
	options := appes.StateOptions[secureState, int64, *secureStateView]{Resource: "secure_state", Actions: []auth.Action{{Name: "read", Kind: auth.KindRead}, {Name: "create", Kind: auth.KindCreate}, {Name: "change", Kind: auth.KindUpdate}}, View: func(id int64, state secureState) *secureStateView {
		return &secureStateView{ID: id, Tenant: state.Tenant, Department: state.Department}
	}, Contract: app.ResourceContract{Isolation: meta.ByField("Tenant"), Assignable: []meta.Dimension{"department"}}, Isolation: func(ctx context.Context) (any, error) { return contextx.TenantID(ctx), nil }}
	descriptor, err := appes.DescribeState(options)
	require.NoError(t, err)
	metadata, err := meta.Describe[*secureStateView]()
	require.NoError(t, err)
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: secureStateScope{}}}}}, auth.CompileOptions{Metadata: metadata, Checker: auth.ActionCheckerFunc(func(ctx context.Context, _ string) error {
		principal, err := auth.RequirePrincipal(ctx)
		if err != nil {
			return err
		}
		if principal.SubjectID != "alice" {
			return errors.NewCode(errors.Forbidden, "denied")
		}
		return nil
	})})
	require.NoError(t, err)
	application, err := appes.NewStateApplication(repository, options)
	require.NoError(t, err)
	tenantContext := func(tenant string) context.Context {
		ctx, err := contextx.WithTenantID(t.Context(), tenant)
		require.NoError(t, err)
		ctx, err = auth.WithPrincipal(ctx, auth.Principal{SubjectID: "alice"})
		require.NoError(t, err)
		return ctx
	}
	ctx, other := tenantContext("t1"), tenantContext("t2")
	tenant := "t1"
	event := func(tenant *string, department string, amount int) []domain.IDomainEvent {
		return []domain.IDomainEvent{secureStateSet{Tenant: tenant, Department: department, Amount: amount}}
	}
	assertRows := func(want int) {
		t.Helper()
		var count int
		require.NoError(t, database.QueryRow(t.Context(), "SELECT COUNT(*) FROM event_store").Scan(&count))
		require.Equal(t, want, count)
	}
	created, err := application.Execute(ctx, "create", 1, 0, func(context.Context, secureState) ([]domain.IDomainEvent, error) {
		return event(&tenant, "ops", 5), nil
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), created.Version())
	assertRows(1)
	called := false
	_, err = application.Execute(other, "create", 1, 0, func(context.Context, secureState) ([]domain.IDomainEvent, error) { called = true; return nil, nil })
	require.True(t, errors.Is(err, errors.NotFound))
	require.False(t, called)
	assertRows(1)
	_, err = application.Load(other, "read", 1)
	require.True(t, errors.Is(err, errors.NotFound))
	_, err = application.Execute(other, "change", 1, 999, func(context.Context, secureState) ([]domain.IDomainEvent, error) { called = true; return nil, nil })
	require.True(t, errors.Is(err, errors.NotFound))
	require.False(t, called)
	assertRows(1)
	for _, bad := range []secureStateSet{{Tenant: &tenant, Department: "other", Amount: 8}, {Tenant: func() *string { s := "t2"; return &s }(), Department: "ops", Amount: 8}} {
		_, err = application.Execute(ctx, "change", 1, 1, func(context.Context, secureState) ([]domain.IDomainEvent, error) {
			return []domain.IDomainEvent{bad}, nil
		})
		require.True(t, errors.Is(err, errors.Forbidden))
		assertRows(1)
	}
	_, err = application.Execute(ctx, "change", 1, 0, func(context.Context, secureState) ([]domain.IDomainEvent, error) {
		t.Fatal("stale callback ran")
		return nil, nil
	})
	require.True(t, errors.Is(err, errors.Concurrency))
	assertRows(1)
	next, err := application.Execute(ctx, "change", 1, 1, func(_ context.Context, temporary secureState) ([]domain.IDomainEvent, error) {
		*temporary.Tenant = "wrong"
		return event(&tenant, "ops", 9), nil
	})
	require.NoError(t, err)
	require.Equal(t, uint64(2), next.Version())
	assertRows(2)
	reloaded, err := application.Load(ctx, "read", 1)
	require.NoError(t, err)
	require.Equal(t, 9, reloaded.Value().Amount)
	require.Equal(t, "t1", *reloaded.Value().Tenant)
	require.Equal(t, uint64(2), reloaded.Version())
}
