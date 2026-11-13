package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen-runtime/host/authz"
	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/middleware"
	"gochen-runtime/http/nethttp"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/contextx"
	domcrud "gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/httpx"
	"gochen/testkit"
)

type bindingEntity struct {
	domcrud.Entity[int64]
	Department int64 `dim:"department"`
	Name       string
}

type bindingInput struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type bindingOutput struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
}

type bindingScope struct{}

func (bindingScope) ResolveSpec(context.Context, auth.Request) (spec.Spec, []auth.Assignment, error) {
	return spec.Eq(meta.ByDimension("department"), int64(10)), nil, nil
}
func (bindingScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension("department")}}
}

// bindingService 保留具名业务方法，以同一对象提供传输执行和授权清单。
type bindingService struct{ fixture *bindingFixture }
type bindingFixture struct {
	service      *bindingService
	repo         *testkit.MemoryRepository[*bindingEntity, int64]
	applications []*crud.Application[*bindingEntity, int64]
	authorizers  []auth.IAuthorizer
	deny         bool
	calls        int
	failure      error
}

func (s *bindingService) AuthorizationBinding() (auth.EntryBinding, error) {
	if s == nil || s.fixture == nil {
		return auth.EntryBinding{}, errors.NewCode(errors.InvalidInput, "service is not initialized")
	}
	f := s.fixture
	binding, err := f.applications[0].AuthorizationBinding("approve")
	if err != nil {
		return auth.EntryBinding{}, err
	}
	other, err := f.applications[1].AuthorizationBinding("approve")
	if err != nil {
		return auth.EntryBinding{}, err
	}
	binding.Resources = []auth.ResourceBinding{{Name: "inventory", Plan: other.Plan, Action: other.Action, Source: other.Source, ConditionPath: other.ConditionPath}}
	return binding, nil
}
func (s *bindingService) Execute(ctx context.Context, input bindingInput) (bindingOutput, error) {
	return s.Approve(ctx, input)
}
func (s *bindingService) Approve(ctx context.Context, input bindingInput) (bindingOutput, error) {
	f := s.fixture
	// 跨资源先预检；普通应用仍正式授权，不隐式复用决定或暴露原始仓储。
	for i, name := range []string{"orders", "inventory"} {
		decision, err := f.authorizers[i].Authorize(ctx, auth.Request{Resource: name, Action: "approve", IDs: []string{fmt.Sprint(input.ID)}})
		if err != nil {
			return bindingOutput{}, err
		}
		if !decision.Allowed {
			return bindingOutput{}, errors.NewCode(errors.Forbidden, "preflight denied")
		}
	}
	entity, err := f.applications[0].Execute(ctx, "approve", input.ID, func(_ context.Context, e *bindingEntity) error { e.Name = input.Name; return nil })
	if err != nil {
		return bindingOutput{}, err
	}
	f.calls++
	return bindingOutput{Name: entity.Name, Operator: contextx.Operator(ctx)}, f.failure
}
func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	f := &bindingFixture{}
	var err error
	f.repo, err = testkit.NewMemoryRepository[*bindingEntity, int64](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, department := range []int64{10, 20} {
		if err := f.repo.Create(t.Context(), &bindingEntity{Entity: domcrud.Entity[int64]{ID: department / 10}, Department: department, Name: "before"}, spec.All()); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"orders", "inventory"} {
		options := crud.Options[*bindingEntity, int64]{DeleteMode: crud.DeletePurge, Resource: name, Actions: []auth.Action{{Name: "approve", Kind: auth.KindUpdate}, {Name: "unused", Kind: auth.KindUpdate}}}
		descriptor, err := crud.Describe(options, true)
		if err != nil {
			t.Fatal(err)
		}
		options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: []auth.Rule{{Scope: &auth.ScopeRule{Resolver: bindingScope{}}}}},
			auth.CompileOptions{Metadata: f.repo.ExecutionDescriptor().Metadata(), Checker: auth.ActionCheckerFunc(func(ctx context.Context, permission string) error {
				principal, ok := auth.PrincipalFromContext(ctx)
				if !ok || principal.SubjectID != "alice" || (f.deny && permission == "inventory:api:approve") {
					return errors.NewCode(errors.Forbidden, "denied")
				}
				return nil
			})})
		if err != nil {
			t.Fatal(err)
		}
		application, err := crud.NewApplication(f.repo, options)
		if err != nil {
			t.Fatal(err)
		}
		f.applications = append(f.applications, application)
		f.authorizers = append(f.authorizers, options.Authorizer)
	}
	f.service = &bindingService{fixture: f}
	return f
}

func decodeBindingInput(c httpx.IContext) (bindingInput, error) {
	var input bindingInput
	err := bindStrictJSON(c, &input)
	return input, err
}

func bindingContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := auth.WithPrincipal(t.Context(), auth.Principal{SubjectID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func invokeBinding(t *testing.T, handler httpx.Handler, ctx context.Context, body string) *httptest.ResponseRecorder {
	t.Helper()
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/orders/approve", strings.NewReader(body)).WithContext(ctx)
	c, err := nethttp.NewBaseContext(writer, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	return writer
}

func TestEndpointBindingUsesSameProtectedBusinessForHTTPAndBackend(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("post", "/orders/approve", f.service, decodeBindingInput, EndpointOptions{Audit: AuditOptions{OperatorExtractor: func(httpx.IContext) (string, bool) { return "http-actor", true }}})
	router := testkit.NewRecordingRouter()
	if err := binding.RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	handler := router.RecordedRoutes()[0].Handler
	ctx := bindingContext(t)
	response := invokeBinding(t, handler, ctx, `{"id":1,"name":"http"}`)
	var output struct {
		Data bindingOutput `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || output.Data.Name != "http" || output.Data.Operator != "http-actor" || f.calls != 1 {
		t.Fatalf("HTTP result: %d %s calls=%d", response.Code, response.Body, f.calls)
	}
	backend, err := f.service.Execute(ctx, bindingInput{ID: 1, Name: "backend"})
	if err != nil || backend.Name != "backend" || f.calls != 2 {
		t.Fatalf("backend result: %+v %v calls=%d", backend, err, f.calls)
	}
	f.deny = true
	response = invokeBinding(t, handler, ctx, `{"id":1,"name":"denied"}`)
	if response.Code != 403 || f.calls != 2 {
		t.Fatalf("secondary resource denial: %d calls=%d", response.Code, f.calls)
	}
	if _, err := f.service.Execute(ctx, bindingInput{ID: 1, Name: "denied"}); !errors.Is(err, errors.Forbidden) || f.calls != 2 {
		t.Fatalf("backend secondary denial: %v calls=%d", err, f.calls)
	}
	f.deny = false
	response = invokeBinding(t, handler, ctx, `{"id":2,"name":"out-of-scope"}`)
	if response.Code == 200 || f.calls != 2 {
		t.Fatalf("scope was bypassed: %d calls=%d", response.Code, f.calls)
	}
	response = invokeBinding(t, handler, t.Context(), `{"id":1,"name":"anonymous"}`)
	if response.Code != 403 || f.calls != 2 {
		t.Fatalf("identity lost: %d calls=%d", response.Code, f.calls)
	}
}

func TestEndpointBindingPreservesEveryResourceInHostEntryChecks(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("POST", "/approve", f.service, decodeBindingInput)
	contribution, err := binding.ModuleBinding()
	if err != nil || len(contribution.Applications) != 2 || contribution.RouteRegistrars[0] != binding {
		t.Fatalf("module contribution: %+v %v", contribution, err)
	}
	for index, resource := range []string{"orders", "inventory"} {
		descriptor := contribution.Applications[index].Descriptor()
		if descriptor.Resource != resource || len(descriptor.Actions) != 1 || descriptor.Actions[0].Name != "approve" {
			t.Fatalf("wrong resource projection: %+v", descriptor)
		}
		descriptor.Actions[0].Name = "tampered"
		if contribution.Applications[index].Descriptor().Actions[0].Name != "approve" {
			t.Fatal("descriptor was not copied")
		}
	}
	server := nethttp.NewServer(nil)
	entries := authz.NewEntryRegistry()
	group, err := runtimehttp.WithEntryRegistry(server.Group("/api/orders"), entries, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.RegisterRoutes(group); err != nil {
		t.Fatal(err)
	}
	if err := entries.ValidateEntries(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	report := entries.Report()
	var businessEntry *auth.EntryStatus
	for index := range report {
		if report[index].Entry.Key == "POST /api/orders/approve" {
			businessEntry = &report[index]
		}
	}
	if businessEntry == nil || businessEntry.Module != "orders" || len(businessEntry.Resources) != 1 || businessEntry.Resources[0].Plan.Descriptor.Resource != "inventory" {
		t.Fatalf("incomplete business entry: %+v", report)
	}
	// A manual route still fails the independent Host inventory check.
	unbound := nethttp.NewServer(nil)
	unbound.Group("/").POST("/unbound", func(httpx.IContext) error { return nil })
	if err := authz.NewEntryRegistry().ValidateEntries(t.Context(), unbound); err == nil {
		t.Fatal("unbound route must fail closed")
	}
}

func TestEndpointBindingRejectsInvalidConfigurationBeforeRegistering(t *testing.T) {
	f := newBindingFixture(t)
	var nilService *bindingService
	var nilBinding *EndpointBinding[bindingInput, bindingOutput]
	cases := []*EndpointBinding[bindingInput, bindingOutput]{
		nilBinding,
		Bind("POST", "/approve", nilService, decodeBindingInput),
		Bind("POST", "/approve", new(bindingService), decodeBindingInput),
		Bind("TRACE", "/approve", f.service, decodeBindingInput),
		Bind("POST", " ", f.service, decodeBindingInput),
		Bind("POST", "/approve", f.service, nil),
		Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{}, EndpointOptions{}),
		Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{MaxBodySize: -1}),
		Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{SuccessStatus: 204}),
		Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{HTTP: HTTPOptions{Middlewares: []httpx.Middleware{nil}}}),
		Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{HTTP: HTTPOptions{CORS: &middleware.CORSConfig{AllowOrigins: []string{"*"}, AllowCredentials: true}}}),
	}
	for index, binding := range cases {
		router := testkit.NewRecordingRouter()
		if _, err := binding.ModuleBinding(); !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("case %d Build error=%v", index, err)
		}
		if err := binding.RegisterRoutes(router); !errors.Is(err, errors.InvalidInput) || len(router.RecordedRoutes()) != 0 {
			t.Fatalf("case %d registered invalid route: %v", index, err)
		}
	}
}

func TestEndpointBindingSharesBodyLimitDecodeAndDegradedErrors(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{MaxBodySize: 50})
	router := testkit.NewRecordingRouter()
	if err := binding.RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	handler := router.RecordedRoutes()[0].Handler
	for _, body := range []string{`{"id":1,"unknown":true}`, `{"id":1,"name":"` + strings.Repeat("x", 100) + `"}`} {
		response := invokeBinding(t, handler, bindingContext(t), body)
		if response.Code < 400 || f.calls != 0 {
			t.Fatalf("invalid input ran business: %d calls=%d", response.Code, f.calls)
		}
	}
	f.failure = contextx.WrapAfterCommitError(errors.NewCode(errors.Internal, "delivery failed"))
	response := invokeBinding(t, handler, bindingContext(t), `{"id":1,"name":"committed"}`)
	if response.Code != 200 || response.Header().Get("X-Execution-Status") != "degraded" || f.calls != 1 {
		t.Fatalf("degraded response: %d %v calls=%d", response.Code, response.Header(), f.calls)
	}
}

func TestEndpointBindingFreezesCORSOptionsAndKeepsPreflightOutsideBusiness(t *testing.T) {
	f := newBindingFixture(t)
	origins := []string{"https://client.example"}
	binding := Bind("POST", "/approve", f.service, decodeBindingInput, EndpointOptions{HTTP: HTTPOptions{CORS: &middleware.CORSConfig{AllowOrigins: origins}}})
	origins[0] = "https://changed.example"
	router := testkit.NewRecordingRouter()
	if err := binding.RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	routes := router.RecordedRoutes()
	if len(routes) != 2 || routes[1].Method != http.MethodOptions {
		t.Fatalf("missing business preflight: %+v", routes)
	}
	request := httptest.NewRequest(http.MethodOptions, "/approve", nil)
	request.Header.Set("Origin", "https://client.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	writer := httptest.NewRecorder()
	c, err := nethttp.NewBaseContext(writer, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := routes[1].Handler(c); err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusNoContent || writer.Header().Get("Access-Control-Allow-Origin") != "https://client.example" || f.calls != 0 {
		t.Fatalf("preflight: %d %v calls=%d", writer.Code, writer.Header(), f.calls)
	}
}

type rejectedBindingRegistry struct{}

func (rejectedBindingRegistry) BindHTTP(string, string, auth.EntryBinding) error {
	return errors.NewCode(errors.FailedPrecondition, "entry registry is sealed")
}

func TestEndpointBindingStopsBeforeMountWhenEntryRegistrationFails(t *testing.T) {
	f := newBindingFixture(t)
	server := nethttp.NewServer(nil)
	group, err := runtimehttp.WithEntryRegistry(server.Group("/orders"), rejectedBindingRegistry{}, "orders")
	if err != nil {
		t.Fatal(err)
	}
	binding := Bind("POST", "/approve", f.service, decodeBindingInput)
	if err := binding.RegisterRoutes(group); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("registry rejection was hidden: %v", err)
	}
	if len(server.Routes()) != 0 || f.calls != 0 {
		t.Fatal("entry rejection must not mount or run business")
	}
}
