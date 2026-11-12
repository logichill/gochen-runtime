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
	"gochen/app"
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

type boundTestCommand = app.Command[bindingInput, *app.CommandRepository[*bindingEntity, int64], bindingOutput]

type bindingFixture struct {
	command *boundTestCommand
	repo    *testkit.MemoryRepository[*bindingEntity, int64]
	deny    bool
	calls   int
	failure error
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
	resources := make([]app.CommandResource[bindingInput], 0, 2)
	for _, name := range []string{"orders", "inventory"} {
		authorizer, err := auth.Compile(auth.Declaration{
			Descriptor:     auth.Descriptor{Resource: name, Actions: []auth.Action{{Name: "approve", Kind: auth.KindUpdate}, {Name: "unused", Kind: auth.KindUpdate}}},
			ResourceAccess: auth.ResourceAccessData,
			Rules:          []auth.Rule{{Scope: &auth.ScopeRule{Resolver: bindingScope{}}}},
		}, auth.CompileOptions{Metadata: f.repo.ExecutionDescriptor().Metadata(), Checker: auth.ActionCheckerFunc(func(ctx context.Context, permission string) error {
			principal, ok := auth.PrincipalFromContext(ctx)
			if !ok || principal.SubjectID != "alice" || (f.deny && permission == "inventory:api:approve") {
				return errors.NewCode(errors.Forbidden, "denied")
			}
			return nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, app.CommandResource[bindingInput]{Name: name, Repository: f.repo, Authorizer: authorizer, Action: "approve", Intent: func(input bindingInput) (auth.Request, error) {
			return auth.Request{IDs: []string{fmt.Sprint(input.ID)}}, nil
		}})
	}
	f.command, err = app.NewCommand(app.CommandOptions[bindingInput, *app.CommandRepository[*bindingEntity, int64], bindingOutput]{
		Resources: resources,
		Access: func(_ context.Context, _ bindingInput, grants []*app.CommandGrant) (*app.CommandRepository[*bindingEntity, int64], error) {
			return app.NewCommandRepository(f.repo, grants[0])
		},
		Handler: func(ctx context.Context, input bindingInput, repo *app.CommandRepository[*bindingEntity, int64]) (bindingOutput, error) {
			entity, err := repo.Get(ctx, input.ID)
			if err != nil {
				return bindingOutput{}, err
			}
			entity.Name = input.Name
			if err := repo.Update(ctx, entity); err != nil {
				return bindingOutput{}, err
			}
			f.calls++
			return bindingOutput{Name: entity.Name, Operator: contextx.Operator(ctx)}, f.failure
		},
	})
	if err != nil {
		t.Fatal(err)
	}
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

func TestCommandBindingUsesSameProtectedCommandForHTTPAndBackend(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("post", "/orders/approve", f.command, decodeBindingInput, CommandOptions{Audit: AuditOptions{OperatorExtractor: func(httpx.IContext) (string, bool) { return "http-actor", true }}})
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
	backend, err := f.command.Execute(ctx, bindingInput{ID: 1, Name: "backend"})
	if err != nil || backend.Name != "backend" || f.calls != 2 {
		t.Fatalf("backend result: %+v %v calls=%d", backend, err, f.calls)
	}
	f.deny = true
	response = invokeBinding(t, handler, ctx, `{"id":1,"name":"denied"}`)
	if response.Code != 403 || f.calls != 2 {
		t.Fatalf("secondary resource denial: %d calls=%d", response.Code, f.calls)
	}
	if _, err := f.command.Execute(ctx, bindingInput{ID: 1, Name: "denied"}); !errors.Is(err, errors.Forbidden) || f.calls != 2 {
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

func TestCommandBindingPreservesEveryResourceInHostEntryChecks(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("POST", "/approve", f.command, decodeBindingInput)
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
	var commandEntry *auth.EntryStatus
	for index := range report {
		if report[index].Entry.Key == "POST /api/orders/approve" {
			commandEntry = &report[index]
		}
	}
	if commandEntry == nil || commandEntry.Module != "orders" || len(commandEntry.Resources) != 1 || commandEntry.Resources[0].Plan.Descriptor.Resource != "inventory" {
		t.Fatalf("incomplete command entry: %+v", report)
	}
	// A manual route still fails the independent Host inventory check.
	unbound := nethttp.NewServer(nil)
	unbound.Group("/").POST("/unbound", func(httpx.IContext) error { return nil })
	if err := authz.NewEntryRegistry().ValidateEntries(t.Context(), unbound); err == nil {
		t.Fatal("unbound route must fail closed")
	}
}

func TestCommandBindingRejectsInvalidConfigurationBeforeRegistering(t *testing.T) {
	f := newBindingFixture(t)
	var nilCommand *boundTestCommand
	var nilBinding *CommandBinding[bindingInput, *app.CommandRepository[*bindingEntity, int64], bindingOutput]
	cases := []*CommandBinding[bindingInput, *app.CommandRepository[*bindingEntity, int64], bindingOutput]{
		nilBinding,
		Bind("POST", "/approve", nilCommand, decodeBindingInput),
		Bind("POST", "/approve", new(boundTestCommand), decodeBindingInput),
		Bind("TRACE", "/approve", f.command, decodeBindingInput),
		Bind("POST", " ", f.command, decodeBindingInput),
		Bind("POST", "/approve", f.command, nil),
		Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{}, CommandOptions{}),
		Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{MaxBodySize: -1}),
		Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{SuccessStatus: 204}),
		Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{HTTP: HTTPOptions{Middlewares: []httpx.Middleware{nil}}}),
		Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{HTTP: HTTPOptions{CORS: &middleware.CORSConfig{AllowOrigins: []string{"*"}, AllowCredentials: true}}}),
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

func TestCommandBindingSharesBodyLimitDecodeAndDegradedErrors(t *testing.T) {
	f := newBindingFixture(t)
	binding := Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{MaxBodySize: 50})
	router := testkit.NewRecordingRouter()
	if err := binding.RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	handler := router.RecordedRoutes()[0].Handler
	for _, body := range []string{`{"id":1,"unknown":true}`, `{"id":1,"name":"` + strings.Repeat("x", 100) + `"}`} {
		response := invokeBinding(t, handler, bindingContext(t), body)
		if response.Code < 400 || f.calls != 0 {
			t.Fatalf("invalid input ran command: %d calls=%d", response.Code, f.calls)
		}
	}
	f.failure = contextx.WrapAfterCommitError(errors.NewCode(errors.Internal, "delivery failed"))
	response := invokeBinding(t, handler, bindingContext(t), `{"id":1,"name":"committed"}`)
	if response.Code != 200 || response.Header().Get("X-Execution-Status") != "degraded" || f.calls != 1 {
		t.Fatalf("degraded response: %d %v calls=%d", response.Code, response.Header(), f.calls)
	}
}

func TestCommandBindingFreezesCORSOptionsAndKeepsPreflightOutsideCommand(t *testing.T) {
	f := newBindingFixture(t)
	origins := []string{"https://client.example"}
	binding := Bind("POST", "/approve", f.command, decodeBindingInput, CommandOptions{HTTP: HTTPOptions{CORS: &middleware.CORSConfig{AllowOrigins: origins}}})
	origins[0] = "https://changed.example"
	router := testkit.NewRecordingRouter()
	if err := binding.RegisterRoutes(router); err != nil {
		t.Fatal(err)
	}
	routes := router.RecordedRoutes()
	if len(routes) != 2 || routes[1].Method != http.MethodOptions {
		t.Fatalf("missing command preflight: %+v", routes)
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

func TestCommandBindingStopsBeforeMountWhenEntryRegistrationFails(t *testing.T) {
	f := newBindingFixture(t)
	server := nethttp.NewServer(nil)
	group, err := runtimehttp.WithEntryRegistry(server.Group("/orders"), rejectedBindingRegistry{}, "orders")
	if err != nil {
		t.Fatal(err)
	}
	binding := Bind("POST", "/approve", f.command, decodeBindingInput)
	if err := binding.RegisterRoutes(group); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("registry rejection was hidden: %v", err)
	}
	if len(server.Routes()) != 0 || f.calls != 0 {
		t.Fatal("entry rejection must not mount or run command")
	}
}
