package rest

import (
	"context"
	"net/http"
	"testing"

	"gochen/app"
	appcrud "gochen/app/crud"
	"gochen/app/query"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	domainaudited "gochen/domain/audited"
	"gochen/errors"
	"gochen/httpx"
	"gochen/testkit"
	"gochen/testkit/require"
)

type mountEntity struct {
	ID      int64
	Version uint64
}

func (e *mountEntity) GetID() int64       { return e.ID }
func (e *mountEntity) GetVersion() uint64 { return e.Version }

// stubApplication 只提供 Descriptor 与上限，其余方法在路由推导测试中不会被调用。
type stubApplication struct {
	descriptor auth.Descriptor
}

func (s *stubApplication) Descriptor() auth.Descriptor { return s.descriptor }
func (s *stubApplication) MaxPageSize() int            { return 100 }
func (s *stubApplication) MaxBulkSize() int            { return 100 }

func (s *stubApplication) Get(context.Context, int64) (*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) List(context.Context, int, int) ([]*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) Query(context.Context, query.QueryOptions) ([]*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) Count(context.Context) (int64, error) { panic("unused") }

func (s *stubApplication) QueryCount(context.Context, query.QueryOptions) (int64, error) {
	panic("unused")
}

func (s *stubApplication) AuditTrail(context.Context, int64, int, int) ([]domainaudited.AuditRecord, error) {
	panic("unused")
}

func (s *stubApplication) Create(context.Context, *mountEntity) error { panic("unused") }
func (s *stubApplication) Update(context.Context, *mountEntity) error { panic("unused") }
func (s *stubApplication) Delete(context.Context, int64) error        { panic("unused") }

func (s *stubApplication) DeleteBulk(context.Context, []int64) error { panic("unused") }

func (s *stubApplication) Execute(context.Context, string, int64, func(context.Context, *mountEntity) error, ...appcrud.ExecOption) (*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) ExecuteNew(context.Context, string, func(context.Context) (*mountEntity, error), ...appcrud.ExecOption) (*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) ExecuteBulk(context.Context, string, []int64, func(context.Context, *mountEntity) error, ...appcrud.ExecOption) ([]*mountEntity, error) {
	panic("unused")
}

func (s *stubApplication) ExecuteNewBulk(context.Context, string, func(context.Context) ([]*mountEntity, error), ...appcrud.ExecOption) ([]*mountEntity, error) {
	panic("unused")
}

type stubLifecycleApplication struct {
	*stubApplication
}

func (s *stubLifecycleApplication) Restore(context.Context, int64, ...appcrud.ExecOption) (*mountEntity, error) {
	panic("unused")
}

func (s *stubLifecycleApplication) Purge(context.Context, int64, ...appcrud.ExecOption) error {
	panic("unused")
}

func (s *stubLifecycleApplication) ListDeleted(context.Context, int, int) ([]*mountEntity, error) {
	panic("unused")
}

func crudDescriptor() auth.Descriptor {
	return auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionList, Kind: auth.KindList},
			{Name: actionCreate, Kind: auth.KindCreate},
			{Name: actionUpdate, Kind: auth.KindUpdate},
			{Name: actionDelete, Kind: auth.KindDelete},
		},
	}
}

func routeSet(routes []testkit.RecordedRoute) map[string]struct{} {
	out := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		out[route.Method+" "+route.Path] = struct{}{}
	}
	return out
}

func requireRoutes(t *testing.T, routes []testkit.RecordedRoute, expected ...string) {
	t.Helper()
	got := routeSet(routes)
	for _, want := range expected {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing route %q in %v", want, got)
		}
	}
	require.Equal(t, len(expected), len(got), "unexpected route count")
}

func TestMountRegistersRoutesFromDescriptor(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: crudDescriptor()}

	err := Mount[*mountEntity, int64](router, app, WithBasePath[*mountEntity, int64]("/users"))
	require.NoError(t, err)

	requireRoutes(t, router.RecordedRoutes(),
		"GET /users",
		"POST /users",
		"POST /users/bulk",
		"PUT /users/bulk",
		"DELETE /users/bulk",
		"GET /users/:id",
		"PUT /users/:id",
		"DELETE /users/:id",
	)
}

func TestMountRegistersOnlyDeclaredActions(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionList, Kind: auth.KindList},
		},
	}}

	err := Mount[*mountEntity, int64](router, app, WithBasePath[*mountEntity, int64]("/users"))
	require.NoError(t, err)
	requireRoutes(t, router.RecordedRoutes(), "GET /users", "GET /users/:id")
}

func TestMountRejectsLifecycleActionsWithoutLifecycleApplication(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionRestore, Kind: auth.KindUpdate},
		},
	}}

	err := Mount[*mountEntity, int64](router, app,
		WithBasePath[*mountEntity, int64]("/users"),
		WithOperatorExtractor[*mountEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }))
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
}

func TestMountRegistersLifecycleRoutes(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubLifecycleApplication{stubApplication: &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionList, Kind: auth.KindList},
			{Name: actionRestore, Kind: auth.KindUpdate},
			{Name: actionPurge, Kind: auth.KindDelete},
			{Name: actionListDeleted, Kind: auth.KindList},
		},
	}}}

	err := Mount[*mountEntity, int64](router, app,
		WithBasePath[*mountEntity, int64]("/users"),
		WithOperatorExtractor[*mountEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }))
	require.NoError(t, err)

	requireRoutes(t, router.RecordedRoutes(),
		"GET /users",
		"GET /users/deleted",
		"POST /users/:id/restore",
		"DELETE /users/:id/purge",
		"GET /users/:id",
	)
}

func TestActionRequiresDeclaredAction(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: actionRead, Kind: auth.KindRead}},
	}}

	err := Action[*mountEntity, int64](router, app, http.MethodPost, "/users/:id/publish", "publish",
		func(context.Context, httpx.IContext, Application[*mountEntity, int64]) error { return nil })
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
}

func TestMountAllowsEmptyBasePath(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: actionList, Kind: auth.KindList},
		},
	}}

	require.NoError(t, Mount[*mountEntity, int64](router, app))
	requireRoutes(t, router.RecordedRoutes(), "GET /", "GET /:id")
}

func TestMountRegistersCORSAndPreflightRoutes(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: crudDescriptor()}

	err := Mount[*mountEntity, int64](router, app,
		WithBasePath[*mountEntity, int64]("/users"),
		WithCORS[*mountEntity, int64]([]string{"https://example.com"}),
	)
	require.NoError(t, err)
	requireRoutes(t, router.RecordedRoutes(),
		"GET /users",
		"POST /users",
		"POST /users/bulk",
		"PUT /users/bulk",
		"DELETE /users/bulk",
		"GET /users/:id",
		"PUT /users/:id",
		"DELETE /users/:id",
		"OPTIONS /users",
		"OPTIONS /users/bulk",
		"OPTIONS /users/:id",
	)
}

type stubStartupRegistry struct {
	apps []app.IDescriptorProvider
}

func (s *stubStartupRegistry) Register(apps ...app.IDescriptorProvider) error {
	s.apps = append(s.apps, apps...)
	return nil
}

func TestMountRegistersApplicationIntoStartupRegistry(t *testing.T) {
	registry := &stubStartupRegistry{}
	app := &stubApplication{descriptor: crudDescriptor()}

	err := Mount[*mountEntity, int64](testkit.NewRecordingRouter(), app,
		WithBasePath[*mountEntity, int64]("/users"),
		WithStartupRegistry[*mountEntity, int64](registry),
	)
	require.NoError(t, err)
	require.Equal(t, 1, len(registry.apps))
	require.Equal(t, app, registry.apps[0])
}

func TestMountRejectsNilRouteGroup(t *testing.T) {
	app := &stubApplication{descriptor: crudDescriptor()}
	err := Mount[*mountEntity, int64](nil, app)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
}

// TestActionRejectsUnsupportedMethod：“未知 method”不能静默降级成 OPTIONS。
func TestActionRejectsUnsupportedMethod(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: "publish", Kind: auth.KindUpdate}},
	}}

	err := Action[*mountEntity, int64](router, app, "TRACE", "/users/:id/publish", "publish",
		func(context.Context, httpx.IContext, Application[*mountEntity, int64]) error { return nil })
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput, got %v", err)
	require.Equal(t, 0, len(router.RecordedRoutes()))
}

func TestActionRegistersDeclaredAction(t *testing.T) {
	router := testkit.NewRecordingRouter()
	app := &stubApplication{descriptor: auth.Descriptor{
		Resource: "user",
		Actions: []auth.Action{
			{Name: actionRead, Kind: auth.KindRead},
			{Name: "publish", Kind: auth.KindUpdate},
		},
	}}

	err := Action[*mountEntity, int64](router, app, http.MethodPost, "/users/:id/publish", "publish",
		func(context.Context, httpx.IContext, Application[*mountEntity, int64]) error { return nil })
	require.NoError(t, err)
	requireRoutes(t, router.RecordedRoutes(), "POST /users/:id/publish")
}

// stubContext 只实现 degraded 映射会触及的响应接口。
type stubContext struct {
	httpx.IContext
	headers map[string]string
	status  int
}

func (c *stubContext) Set(string, httpx.ContextValue) {}

func (c *stubContext) SetHeader(key, value string) {
	c.headers[key] = value
}

func (c *stubContext) JSON(code int, _ httpx.JSONBody) error {
	c.status = code
	return nil
}

func TestWrapHandlerMapsAfterCommitErrorToDegraded(t *testing.T) {
	rb := &RouteBuilder[*mountEntity, int64]{config: DefaultRouteConfig[int64]()}
	handler := rb.wrapHandler(func(httpx.IContext) error {
		return contextx.WrapAfterCommitError(errors.NewCode(errors.Internal, "projection failed"))
	})

	ctx := &stubContext{headers: make(map[string]string)}
	require.NoError(t, handler(ctx))
	require.Equal(t, "degraded", ctx.headers["X-Execution-Status"])
	require.Equal(t, http.StatusOK, ctx.status)
}

var _ Application[*mountEntity, int64] = (*stubApplication)(nil)
var _ LifecycleApplication[*mountEntity, int64] = (*stubLifecycleApplication)(nil)
var _ domain.IEntity[int64] = (*mountEntity)(nil)
