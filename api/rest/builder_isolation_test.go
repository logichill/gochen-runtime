package rest

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen-runtime/internal/testutil"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	secaction "gochen/app/security/action"
	"gochen/auth/action"
	"gochen/httpx"
)

type validatorFunc func(any) error

func (f validatorFunc) Validate(value any) error { return f(value) }

type allowingActionChecker struct{}

func (allowingActionChecker) RequireAction(context.Context, string) error { return nil }

func TestBuildersShareApplicationWithoutChangingItsBehavior(t *testing.T) {
	var hooks, validations atomic.Int64
	base, err := appcrud.NewApplication(&noopRepository{}, &appcrud.Options[*fakeEntity, int64]{
		Config:    &appcrud.ServiceConfig{AutoValidate: true, MaxBatchSize: 1},
		Validator: validatorFunc(func(any) error { validations.Add(1); return nil }),
		Hooks: &appcrud.Hooks[*fakeEntity, int64]{BeforeCreate: func(context.Context, *fakeEntity) error {
			hooks.Add(1)
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secured, err := secaction.New[*fakeEntity, int64](base, allowingActionChecker{}, action.OperationPolicy{Create: "item:api:create"})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"public", "internal"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewApiBuilder(secured, WithBatchOperations[*fakeEntity, int64](20))
			if err != nil {
				t.Fatal(err)
			}
			builder.Route(func(cfg *RouteConfig[int64]) { cfg.Routing.BasePath = "/items" })
			group := testutil.NewMockRouteGroup()
			if err := builder.Build(group); err != nil {
				t.Fatal(err)
			}
			if err := secured.Create(context.Background(), &fakeEntity{}); err != nil {
				t.Fatal(err)
			}
			if status := invokeIsolationHandler(t, group.Handlers["POST /items"], "POST", `{}`); status != 200 {
				t.Fatalf("create status = %d", status)
			}
			if status := invokeIsolationHandler(t, group.Handlers["POST /items/batch"], "POST", `[{},{}]`); status != 400 {
				t.Fatalf("HTTP limit must not relax application batch limit: status = %d", status)
			}
		})
	}
	t.Cleanup(func() {
		if hooks.Load() != 4 || validations.Load() != 4 {
			t.Errorf("direct and HTTP calls must retain application behavior: hooks=%d validations=%d", hooks.Load(), validations.Load())
		}
		if base.Config().MaxBatchSize != 1 {
			t.Error("registration changed application batch limit")
		}
	})
}

func TestBatchLimitsAreLocalToEachRouteGroup(t *testing.T) {
	for _, tc := range []struct{ method, body string }{
		{"POST", `[{"id":1},{"id":2}]`},
		{"PUT", `[{"id":1},{"id":2}]`},
		{"DELETE", `{"ids":[1,2]}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			service := newStubAppService(nil)
			groups := make([]*testutil.MockRouteGroup, 0, 2)
			for _, limit := range []int{1, 2} {
				builder, err := NewApiBuilder[*fakeEntity, int64](service, WithBatchOperations[*fakeEntity, int64](limit))
				if err != nil {
					t.Fatal(err)
				}
				builder.Route(func(cfg *RouteConfig[int64]) { cfg.Routing.BasePath = "/items" })
				group := testutil.NewMockRouteGroup()
				if err := builder.Build(group); err != nil {
					t.Fatal(err)
				}
				groups = append(groups, group)
			}
			// 两组都注册后，先前一组的限制仍然生效。
			for i, want := range []int{400, 200} {
				if status := invokeIsolationHandler(t, groups[i].Handlers[tc.method+" /items/batch"], tc.method, tc.body); status != want {
					t.Fatalf("group %d: status = %d, want %d", i, status, want)
				}
			}
			if calls := service.createAllCalls + service.updateAllCalls + service.deleteAllCalls; calls != 1 {
				t.Fatalf("oversized request reached application; calls = %d", calls)
			}
		})
	}
}

func invokeIsolationHandler(t *testing.T, handler httpx.Handler, method, body string) int {
	t.Helper()
	if handler == nil {
		t.Fatal("missing handler")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/items/batch", strings.NewReader(body))
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(ctx); err != nil {
		t.Fatal(err)
	}
	return w.Code
}

func TestSecuredAuditedRoutesUseApplicationPurgeCapability(t *testing.T) {
	base, err := appaudited.NewApplication(&auditedUpdateRepo{}, noopAuditStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secured, err := secaction.New[*auditedUpdateEntity, int64](base, allowingActionChecker{}, action.OperationPolicy{Purge: "item:api:purge"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := secured.(appcrud.IRepositoryProvider[*auditedUpdateEntity, int64]); ok {
		t.Fatal("raw repository exposed")
	}
	builder, err := NewApiBuilder(secured, WithOperatorExtractor[*auditedUpdateEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }))
	if err != nil {
		t.Fatal(err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) { cfg.Routing.BasePath = "/items" })
	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatal(err)
	}
	if group.Handlers["DELETE /items/:id/purge"] == nil {
		t.Fatal("secured purge route missing")
	}

	noPurge, err := appaudited.NewApplication(&auditedNoopRepo{}, stubAuditStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := secaction.New[*auditedFakeEntity, int64](noPurge, allowingActionChecker{}, action.OperationPolicy{Purge: "item:api:purge"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewApiBuilder(protected, WithOperatorExtractor[*auditedFakeEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }))
	if err != nil {
		t.Fatal(err)
	}
	b.Route(func(cfg *RouteConfig[int64]) { cfg.Routing.BasePath = "/items" })
	g := testutil.NewMockRouteGroup()
	if err := b.Build(g); err != nil {
		t.Fatal(err)
	}
	if g.Handlers["DELETE /items/:id/purge"] != nil {
		t.Fatal("unsupported purge route must remain absent")
	}
}
