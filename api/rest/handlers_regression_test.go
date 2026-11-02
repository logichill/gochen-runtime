package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gochen-runtime/http/nethttp"
	appaudited "gochen/app/audited"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/auth/action"
	"gochen/contextx"
	"gochen/domain"
	domaudited "gochen/domain/audited"
	domcrud "gochen/domain/crud"
	"gochen/errors"
	"gochen/httpx"
	"gochen/testkit"
	"gochen/testkit/require"
)

type handlerEntity struct {
	domaudited.AuditedEntity[int64]
	Name string `json:"name"`
}

type timestampHandlerEntity struct {
	domcrud.Entity[int64]
	domain.Timestamps
	Name string `json:"name"`
}

func TestBulkUpdatePreservesEmbeddedManagedTimestamps(t *testing.T) {
	ctx := context.Background()
	repo, repositoryErr := testkit.NewMemoryRepository[*timestampHandlerEntity, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		t.Fatal(repositoryErr)
	}
	app, err := crud.NewApplication(repo, crud.Options[*timestampHandlerEntity, int64]{Resource: "items", Authorizer: auth.Allow()})
	require.NoError(t, err)
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entity := &timestampHandlerEntity{Timestamps: domain.Timestamps{CreatedAt: createdAt, UpdatedAt: createdAt}, Name: "original"}
	require.NoError(t, app.Create(ctx, entity))
	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount(router, app, WithBasePath[*timestampHandlerEntity, int64]("/items")))
	w := callRecordedHandler(t, router, ctx, http.MethodPut, "/items/bulk", "/items/bulk", `[{"id":1,"name":"changed"}]`)
	require.Equal(t, http.StatusOK, w.Code)
	stored, err := app.Get(ctx, entity.GetID())
	require.NoError(t, err)
	require.Equal(t, "changed", stored.Name)
	require.Equal(t, createdAt, stored.CreatedAt)
	require.Equal(t, createdAt, stored.UpdatedAt)
}

func auditedHandlerFixture(t *testing.T) (context.Context, *appaudited.Application[*handlerEntity, int64], *testkit.RecordingRouter) {
	t.Helper()
	ctx, err := contextx.WithOperator(context.Background(), "tester")
	require.NoError(t, err)
	repo, repositoryErr := testkit.NewAuditedMemoryRepository[*handlerEntity, int64](nil)
	if repositoryErr != nil {
		t.Fatal(repositoryErr)
	}
	app, err := appaudited.NewApplication(repo, repo, appaudited.Options[*handlerEntity, int64]{Options: crud.Options[*handlerEntity, int64]{Resource: "items", Authorizer: auth.Allow()}})
	require.NoError(t, err)
	for i, name := range []string{"A", "B"} {
		e := &handlerEntity{Name: name}
		e.ID = int64(i + 1)
		require.NoError(t, app.Create(ctx, e))
	}
	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount(router, app, WithBasePath[*handlerEntity, int64]("/items"), WithOperatorExtractor[*handlerEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true })))
	return ctx, app, router
}

func callRecordedHandler(t *testing.T, router *testkit.RecordingRouter, ctx context.Context, method, path, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	for _, route := range router.RecordedRoutes() {
		if route.Method != method || route.Path != path {
			continue
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, url, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Origin", "https://example.com")
		c, err := nethttp.NewBaseContext(w, r)
		require.NoError(t, err)
		require.NoError(t, route.Handler(c))
		return w
	}
	t.Fatalf("missing route %s %s", method, path)
	return nil
}

func TestFilteredPaginationCountsMatchingRows(t *testing.T) {
	ctx, _, router := auditedHandlerFixture(t)
	w := callRecordedHandler(t, router, ctx, http.MethodGet, "/items", "/items?filter=name:eq:B&page=1&size=1", "")
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data struct {
			Total   int64            `json:"total"`
			HasNext bool             `json:"has_next"`
			Items   []*handlerEntity `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, int64(1), response.Data.Total)
	require.False(t, response.Data.HasNext)
	require.Len(t, response.Data.Items, 1)
	require.Equal(t, "B", response.Data.Items[0].Name)
}

func TestAuditedBulkUpdateRejectsStaleVersionAndRollsBack(t *testing.T) {
	ctx, app, router := auditedHandlerFixture(t)
	current, err := app.Get(ctx, 2)
	require.NoError(t, err)
	current.Name = "newest"
	require.NoError(t, app.Update(ctx, current))
	w := callRecordedHandler(t, router, ctx, http.MethodPut, "/items/bulk", "/items/bulk", `[{"id":1,"version":0,"name":"changed"},{"id":2,"version":0,"name":"stale"}]`)
	require.Equal(t, http.StatusConflict, w.Code)
	first, err := app.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "A", first.Name)
	require.Equal(t, uint64(0), first.Version)
	second, err := app.Get(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, "newest", second.Name)
	require.Equal(t, uint64(1), second.Version)
	audits, err := app.AuditTrail(ctx, 1, 0, 100)
	require.NoError(t, err)
	require.Len(t, audits, 1)
}

func TestAuditedBulkUpdateValidatesManagedFieldsAndVersion(t *testing.T) {
	for _, body := range []string{
		`[{"id":1,"name":"changed"}]`,
		`[{"id":1,"version":null,"name":"changed"}]`,
		`[{"id":1,"version":0,"Version":0,"name":"changed"}]`,
		`[{"id":1,"version":0,"created_by":"forged","name":"changed"}]`,
	} {
		t.Run(body, func(t *testing.T) {
			ctx, app, router := auditedHandlerFixture(t)
			w := callRecordedHandler(t, router, ctx, http.MethodPut, "/items/bulk", "/items/bulk", body)
			require.Equal(t, http.StatusBadRequest, w.Code)
			current, err := app.Get(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, "A", current.Name)
		})
	}
	ctx, app, router := auditedHandlerFixture(t)
	w := callRecordedHandler(t, router, ctx, http.MethodPut, "/items/bulk", "/items/bulk", `[{"id":1,"version":0,"name":"changed"}]`)
	require.Equal(t, http.StatusOK, w.Code)
	current, err := app.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "changed", current.Name)
	require.Equal(t, uint64(1), current.Version)
}

func TestFastDenyRunsAfterAuthenticationAndUsesResponseMiddleware(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[denied], func(t *testing.T) {
			_, app, _ := auditedHandlerFixture(t)
			called := false
			authn := func(c httpx.IContext, next func() error) error {
				called = true
				ctx, err := auth.WithPrincipal(c.RequestContext(), auth.Principal{SubjectID: "user"})
				if err != nil {
					return err
				}
				c.SetContext(c.RequestContext().WithContext(ctx))
				return next()
			}
			checker := action.ActionCheckerFunc(func(ctx context.Context, _ string) error {
				if _, ok := auth.PrincipalFromContext(ctx); !ok {
					return errors.NewCode(errors.Unauthorized, "missing principal")
				}
				if denied {
					return errors.NewCode(errors.Forbidden, "denied")
				}
				return nil
			})
			router := testkit.NewRecordingRouter()
			require.NoError(t, Mount(router, app, WithBasePath[*handlerEntity, int64]("/items"), WithOperatorExtractor[*handlerEntity, int64](func(httpx.IContext) (string, bool) { return "tester", true }), WithAuth[*handlerEntity, int64](authn), WithPermissionChecker[*handlerEntity, int64](checker), WithCORS[*handlerEntity, int64]([]string{"https://example.com"})))
			w := callRecordedHandler(t, router, context.Background(), http.MethodGet, "/items", "/items", "")
			require.True(t, called)
			require.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
			want := http.StatusOK
			if denied {
				want = http.StatusForbidden
			}
			require.Equal(t, want, w.Code)
		})
	}
}
