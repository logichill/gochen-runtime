package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gochen-runtime/host"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	hostruntime "gochen-runtime/host/module/runtime"
	"gochen-runtime/http/nethttp"
	"gochen/app/crud"
	appaudit "gochen/app/crud/audit"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain"
	domcrud "gochen/domain/crud"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/testkit"
	"gochen/testkit/require"
)

type deletionRouteEntity struct{ domcrud.Entity[int64] }

type mismatchedDeleteSurface struct{ *stubApplication }

func (*mismatchedDeleteSurface) DeleteAction() string { return actionPurge }

func TestRouteSelectionIsExplicitAndFrozen(t *testing.T) {
	repository, err := testkit.NewMemoryRepository[*handlerEntity, int64](nil)
	require.NoError(t, err)
	application, err := crud.NewApplication(repository, crud.Options[*handlerEntity, int64]{Resource: "note", DeleteMode: crud.DeleteSoft, EnableRestore: true, EnableDeletedQuery: true, Authorizer: auth.Allow()})
	require.NoError(t, err)
	_, specs, err := Describe(application)
	require.NoError(t, err)
	require.Equal(t, 8, len(specs))
	for _, route := range specs {
		if route.Op == OpRestore || route.Op == OpListDeleted || route.Op == OpPurge {
			t.Fatalf("lifecycle route automatically exposed: %+v", route)
		}
	}
	for operation, path := range map[Op]string{OpRestore: "POST /:id/restore", OpListDeleted: "GET /deleted", OpPurge: "DELETE /:id/purge"} {
		_, isolated, err := Describe(application, WithRoutes[*handlerEntity, int64](operation))
		require.NoError(t, err)
		require.Equal(t, []string{path}, specKeys(isolated))
		router := testkit.NewRecordingRouter()
		require.NoError(t, Mount(router, application, WithRoutes[*handlerEntity, int64](operation)))
		require.Equal(t, 1, len(router.RecordedRoutes()))
	}
	selected := []Op{OpRead, OpList}
	option := WithRoutes[*handlerEntity, int64](selected...)
	selected[0] = OpPurge
	builder, err := NewRouteBuilder(application, option)
	require.NoError(t, err)
	_, specs, err = builder.Describe()
	require.NoError(t, err)
	require.Equal(t, []string{"GET ", "GET /:id"}, specKeys(specs))
	// HTTP 只读清单不改变写 Application 本身的执行能力。
	require.Equal(t, actionDelete, application.DeleteAction())
	empty, err := NewRouteBuilder(application, WithRoutes[*handlerEntity, int64]())
	require.NoError(t, err)
	_, specs, err = empty.Describe()
	require.NoError(t, err)
	require.Equal(t, 0, len(specs))
	router := testkit.NewRecordingRouter()
	require.NoError(t, empty.Register(router))
	require.Equal(t, 0, len(router.RecordedRoutes()))
	for _, selected := range [][]Op{{Op("unknown")}, {OpRead, OpRead}} {
		_, err := NewRouteBuilder(application, WithRoutes[*handlerEntity, int64](selected...))
		require.True(t, errors.Is(err, errors.InvalidInput))
	}
	disabled, err := crud.NewApplication(repository, crud.Options[*handlerEntity, int64]{Resource: "note", DeleteMode: crud.DeletePurge, Authorizer: auth.Allow()})
	require.NoError(t, err)
	for _, operation := range []Op{OpRestore, OpListDeleted} {
		_, err = NewRouteBuilder(disabled, WithRoutes[*handlerEntity, int64](operation))
		require.True(t, errors.Is(err, errors.InvalidInput))
	}
	for _, operation := range []Op{OpRestore, OpListDeleted, OpPurge} {
		descriptor := auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: actionRestore, Kind: auth.KindUpdate}, {Name: actionListDeleted, Kind: auth.KindList}, {Name: actionPurge, Kind: auth.KindDelete}}}
		_, err := NewRouteBuilder(&stubApplication{descriptor: descriptor}, WithRoutes[*mountEntity, int64](operation))
		require.True(t, errors.Is(err, errors.InvalidInput))
	}
	_, err = NewRouteBuilder(&mismatchedDeleteSurface{&stubApplication{descriptor: crudDescriptor()}})
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestHostDeletionRoutesBindActualExecution(t *testing.T) {
	t.Run("ordinary", func(t *testing.T) {
		runHostDeletionRoutes(t, crud.DeletePurge, false, func(id int64) *deletionRouteEntity {
			return &deletionRouteEntity{Entity: domcrud.Entity[int64]{ID: id}}
		})
	})
	t.Run("soft disabled", func(t *testing.T) {
		runHostDeletionRoutes(t, crud.DeletePurge, true, func(id int64) *handlerEntity {
			return &handlerEntity{TrackedEntity: domcrud.TrackedEntity[int64]{Entity: domcrud.Entity[int64]{ID: id}}}
		})
	})
	t.Run("soft enabled", func(t *testing.T) {
		runHostDeletionRoutes(t, crud.DeleteSoft, true, func(id int64) *handlerEntity {
			return &handlerEntity{TrackedEntity: domcrud.TrackedEntity[int64]{Entity: domcrud.Entity[int64]{ID: id}}}
		})
	})
}

func runHostDeletionRoutes[T domain.IEntity[int64]](t *testing.T, mode crud.DeleteMode, softCapable bool, entity func(int64) T) {
	t.Helper()
	repository, err := testkit.NewAuditedMemoryRepository[T, int64](nil)
	require.NoError(t, err)
	ctx, err := contextx.WithOperator(t.Context(), "alice")
	require.NoError(t, err)
	ctx, err = auth.WithPrincipal(ctx, auth.Principal{SubjectID: "alice"})
	require.NoError(t, err)
	for id := int64(1); id <= 6; id++ {
		require.NoError(t, repository.Create(ctx, entity(id), spec.All()))
	}
	actual := actionPurge
	if mode == crud.DeleteSoft {
		actual = actionDelete
	}
	denied := false
	var permissions, before, after, post []string
	options := crud.Options[T, int64]{Resource: "note", DeleteMode: mode, EnableRestore: softCapable, EnableDeletedQuery: softCapable, Audit: repository, ActionPermissions: map[string]string{actionPurge: "note:api:erase"}, Hooks: crud.Hooks[T]{}}
	for _, action := range []string{actionPurge, actual} {
		current := action
		options.Hooks[current] = crud.Stage[T]{Before: func(context.Context, T) error { before = append(before, current); return nil }, After: func(context.Context, T) error { after = append(after, current); return nil }, PostCommit: func(context.Context, T) error { post = append(post, current); return nil }}
	}
	if actual == actionDelete {
		options.ActionPermissions[actionDelete] = "note:api:trash"
	}
	descriptor, err := crud.Describe(options, true)
	require.NoError(t, err)
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData}, auth.CompileOptions{Metadata: repository.ExecutionDescriptor().Metadata(), Checker: auth.ActionCheckerFunc(func(_ context.Context, permission string) error {
		permissions = append(permissions, permission)
		// 旧角色只获准 legacy delete，不能调用硬模式实际的 purge 权限。
		if denied && permission != "note:api:delete" {
			return errors.NewCode(errors.Forbidden, "delete-only role cannot purge")
		}
		return nil
	})})
	require.NoError(t, err)
	application, err := crud.NewApplication(repository, options)
	require.NoError(t, err)
	history, err := appaudit.NewReader[int64](repository, repository, appaudit.Options{Resource: "note", Authorizer: auth.Allow()})
	require.NoError(t, err)
	// 缺省清单可公开业务删除，但绝不会因 concrete 方法自动增加 /purge。
	_, defaults, err := Describe(application)
	require.NoError(t, err)
	for _, route := range defaults {
		if route.Op == OpDelete || route.Op == OpDeleteBulk {
			require.Equal(t, actual, route.Action)
		}
		if route.Op == OpPurge {
			t.Fatal("purge route was implicit")
		}
	}
	operations := []Op{OpDelete, OpDeleteBulk, OpPurge}
	if softCapable {
		operations = append(operations, OpRestore, OpListDeleted)
	}
	server := nethttp.NewServer(nil)
	entries := authz.NewEntryRegistry()
	ctor := func() (module.IModule, error) {
		return host.Module("notes").Binding(Resource(application, WithRoutes[T, int64](operations...), WithFastDeny[T, int64]())).Build()
	}
	runtime := hostruntime.NewHost([]module.ModuleCtor{ctor}, config.WithBasePath("/api"), config.WithDisableHealthRoute(true), config.WithHTTPServer(server), config.WithEntryValidator(entries))
	require.NoError(t, runtime.Prepare(ctx))
	t.Cleanup(func() { require.NoError(t, runtime.Shutdown(context.Background())) })
	require.NoError(t, runtime.StartBackground(ctx))
	found := 0
	for _, entry := range entries.Report() {
		if entry.Entry.Key == "DELETE /api/notes/{id}" || entry.Entry.Key == "DELETE /api/notes/bulk" || entry.Entry.Key == "DELETE /api/notes/{id}/purge" {
			expected := actual
			if strings.HasSuffix(entry.Entry.Key, "/purge") {
				expected = actionPurge
			}
			require.Equal(t, auth.EntryDeclared, entry.Class)
			require.Equal(t, expected, entry.Action)
			require.Equal(t, "notes", entry.Module)
			if entry.Plan == nil {
				t.Fatal("missing compiled plan")
			}
			permission, ok := entry.Plan.Descriptor.PermissionFor(expected)
			require.True(t, ok)
			require.Equal(t, options.ActionPermissions[expected], permission)
			found++
		}
	}
	require.Equal(t, 3, found)
	invoke := func(method, path, body string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(method, "/api/notes"+path, strings.NewReader(body)).WithContext(ctx))
		require.Equalf(t, want, response.Code, "%s %s: %s", method, path, response.Body.String())
	}
	denied = true
	invoke(http.MethodDelete, "/1", "", http.StatusForbidden)
	require.True(t, errors.Is(application.Delete(ctx, 1), errors.Forbidden))
	require.Equal(t, 0, len(before))
	require.Equal(t, 0, len(after))
	require.Equal(t, 0, len(post))
	_, err = repository.Get(ctx, 1, spec.All())
	require.NoError(t, err)
	records, err := history.AuditTrail(ctx, 1, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 0, len(records))
	for _, permission := range permissions {
		require.Equal(t, options.ActionPermissions[actual], permission)
	}
	denied = false
	invoke(http.MethodDelete, "/1", "", http.StatusOK)
	require.Equal(t, []string{actual}, before)
	require.Equal(t, before, after)
	require.Equal(t, before, post)
	records, err = history.AuditTrail(ctx, 1, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 1, len(records))
	require.Equal(t, actual, records[0].Action)
	invoke(http.MethodDelete, "/1", "", http.StatusNotFound)
	require.Equal(t, []string{actual}, before)
	require.Equal(t, before, after)
	require.Equal(t, before, post)
	records, err = history.AuditTrail(ctx, 1, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 1, len(records))
	if mode == crud.DeleteSoft {
		stored, err := repository.GetWithDeleted(ctx, 1, spec.All())
		require.NoError(t, err)
		require.True(t, any(stored).(domain.ISoftDeletable).IsDeleted())
		invoke(http.MethodGet, "/deleted", "", http.StatusOK)
		invoke(http.MethodPost, "/1/restore", "", http.StatusOK)
		invoke(http.MethodDelete, "/1", "", http.StatusOK)
		invoke(http.MethodDelete, "/1/purge", "", http.StatusOK)
	}
	_, err = repository.GetWithDeleted(ctx, 1, spec.All())
	require.True(t, errors.Is(err, errors.NotFound))
	if softCapable {
		for _, id := range []int64{5, 6} {
			stored, err := repository.Get(ctx, id, spec.All())
			require.NoError(t, err)
			require.NoError(t, any(stored).(domain.ISoftDeletableBy).SoftDeleteBy("alice", time.Now()))
			require.NoError(t, repository.Update(ctx, stored, spec.All()))
		}
		if mode == crud.DeletePurge {
			invoke(http.MethodDelete, "/5", "", http.StatusOK)
			_, err = repository.GetWithDeleted(ctx, 5, spec.All())
			require.True(t, errors.Is(err, errors.NotFound))
			records, err = history.AuditTrail(ctx, 5, 0, 100)
			require.NoError(t, err)
			require.Equal(t, actionPurge, records[0].Action)
		}
		invoke(http.MethodDelete, "/6/purge", "", http.StatusOK)
		_, err = repository.GetWithDeleted(ctx, 6, spec.All())
		require.True(t, errors.Is(err, errors.NotFound))
	}
	// 显式 Purge 对活动实体与软删除实体均物理清除。
	invoke(http.MethodDelete, "/2/purge", "", http.StatusOK)
	_, err = repository.GetWithDeleted(ctx, 2, spec.All())
	require.True(t, errors.Is(err, errors.NotFound))
	records, err = history.AuditTrail(ctx, 2, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 1, len(records))
	require.Equal(t, actionPurge, records[0].Action)
	invoke(http.MethodDelete, "/bulk", `{"ids":[3,4]}`, http.StatusOK)
	for _, id := range []int64{3, 4} {
		records, err = history.AuditTrail(ctx, id, 0, 100)
		require.NoError(t, err)
		require.Equal(t, 1, len(records))
		require.Equal(t, actual, records[0].Action)
		stored, err := repository.GetWithDeleted(ctx, id, spec.All())
		if mode == crud.DeleteSoft {
			require.NoError(t, err)
			require.True(t, any(stored).(domain.ISoftDeletable).IsDeleted())
		} else {
			require.Truef(t, errors.Is(err, errors.NotFound), "%d: %v", id, err)
		}
	}
}
