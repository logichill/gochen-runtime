package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen-runtime/http/nethttp"
	appcrud "gochen/app/crud"
	appaudit "gochen/app/crud/audit"
	domaudited "gochen/audit"
	"gochen/auth"
	"gochen/contextx"
	"gochen/errors"
	"gochen/testkit"
	"gochen/testkit/require"
)

// readSurface 只暴露读取契约，刻意不实现写入、批量配额或审计能力。
type readSurface struct {
	ReadApplication[*mountEntity, int64]
	descriptor auth.Descriptor
}

func (a readSurface) Descriptor() auth.Descriptor { return a.descriptor }

func TestMountAcceptsReadCapabilityWithoutAuditOrWrites(t *testing.T) {
	application := readSurface{ReadApplication: &stubApplication{}, descriptor: auth.Descriptor{
		Resource: "user",
		Actions:  []auth.Action{{Name: actionRead, Kind: auth.KindRead}, {Name: actionList, Kind: auth.KindList}},
	}}
	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount(router, application, WithBasePath[*mountEntity, int64]("/users")))
	requireRoutes(t, router.RecordedRoutes(), "GET /users", "GET /users/:id")
	if _, ok := any(application).(appaudit.IAuditReader[int64]); ok {
		t.Fatal("read-only fixture unexpectedly implements audit")
	}
}

func TestMountRejectsMissingDeclaredCapabilitiesBeforeRegistering(t *testing.T) {
	for _, action := range []auth.Action{
		{Name: actionCreate, Kind: auth.KindCreate},
		{Name: actionUpdate, Kind: auth.KindUpdate},
		{Name: actionDelete, Kind: auth.KindDelete},
		{Name: actionAuditRead, Kind: auth.KindRead},
	} {
		t.Run(action.Name, func(t *testing.T) {
			application := readSurface{ReadApplication: &stubApplication{}, descriptor: auth.Descriptor{
				Resource: "user", Actions: []auth.Action{{Name: actionRead, Kind: auth.KindRead}, action},
			}}
			router := testkit.NewRecordingRouter()
			err := Mount(router, application)
			require.Truef(t, errors.Is(err, errors.InvalidInput), "Mount error = %v", err)
			require.Equal(t, 0, len(router.RecordedRoutes()))
			_, _, err = Describe(application)
			require.Truef(t, errors.Is(err, errors.InvalidInput), "Describe error = %v", err)
		})
	}
}

type writeWithoutExecutor struct {
	readSurface
	appcrud.IWriteApplication[*mountEntity, int64]
}

func TestMountRequiresBulkExecutionForDeclaredCreateAndUpdate(t *testing.T) {
	for _, action := range []auth.Action{{Name: actionCreate, Kind: auth.KindCreate}, {Name: actionUpdate, Kind: auth.KindUpdate}} {
		application := writeWithoutExecutor{readSurface: readSurface{
			ReadApplication: &stubApplication{}, descriptor: auth.Descriptor{Resource: "user", Actions: []auth.Action{action}},
		}, IWriteApplication: &stubApplication{}}
		router := testkit.NewRecordingRouter()
		err := Mount(router, application)
		require.Truef(t, errors.Is(err, errors.InvalidInput), "%s: %v", action.Name, err)
		require.Equal(t, 0, len(router.RecordedRoutes()))
	}
}

func TestMountRejectsTypedNilReadApplication(t *testing.T) {
	var application *appcrud.ReadApplication[*mountEntity, int64]
	router := testkit.NewRecordingRouter()
	err := Mount(router, application)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "Mount error = %v", err)
	require.Equal(t, 0, len(router.RecordedRoutes()))
}

func TestReadApplicationAuditRoutePreservesDistinctAuthorization(t *testing.T) {
	repository, err := testkit.NewAuditedMemoryRepository[*handlerEntity, int64](testkit.NewInt64Sequence(1))
	require.NoError(t, err)
	writer, err := appcrud.NewApplication(repository, appcrud.Options[*handlerEntity, int64]{Resource: "items", Authorizer: auth.Allow(), Audit: repository})
	require.NoError(t, err)
	ctx, err := contextx.WithOperator(t.Context(), "tester")
	require.NoError(t, err)
	require.NoError(t, writer.Create(ctx, &handlerEntity{Name: "recorded"}))

	options := appcrud.Options[*handlerEntity, int64]{Resource: "items"}
	descriptor, err := appcrud.Describe(options, false)
	require.NoError(t, err)
	auditOptions := appaudit.Options{Resource: "items"}
	auditDescriptor, err := appaudit.Describe(auditOptions)
	require.NoError(t, err)
	allowAudit := false
	var permissions []string
	dependencies := auth.CompileOptions{Metadata: repository.ExecutionDescriptor().Metadata(), Checker: auth.ActionCheckerFunc(func(_ context.Context, permission string) error {
		permissions = append(permissions, permission)
		if permission == "items:api:audit_read" && !allowAudit {
			return errors.NewCode(errors.Forbidden, "audit access denied")
		}
		return nil
	})}
	options.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData}, dependencies)
	require.NoError(t, err)
	auditOptions.Authorizer, err = auth.Compile(auth.Declaration{Descriptor: auditDescriptor, ResourceAccess: auth.ResourceAccessData}, dependencies)
	require.NoError(t, err)
	application, err := appcrud.NewReadApplication(repository, options)
	require.NoError(t, err)
	reader, err := appaudit.NewReader[int64](repository, repository, auditOptions)
	require.NoError(t, err)
	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount(router, application, WithBasePath[*handlerEntity, int64]("/items"), WithAudit[*handlerEntity, int64](reader)))
	requireRoutes(t, router.RecordedRoutes(), "GET /items", "GET /items/:id", "GET /items/:id/audit")

	invoke := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		for _, route := range router.RecordedRoutes() {
			if route.Path != path {
				continue
			}
			response := httptest.NewRecorder()
			c, err := nethttp.NewBaseContext(response, httptest.NewRequest(http.MethodGet, "/items/1", nil).WithContext(ctx))
			require.NoError(t, err)
			c.SetParam("id", "1")
			require.NoError(t, route.Handler(c))
			return response
		}
		t.Fatalf("missing route %s", path)
		return nil
	}
	require.Equal(t, http.StatusOK, invoke("/items/:id").Code)
	require.Equal(t, http.StatusForbidden, invoke("/items/:id/audit").Code)
	allowAudit = true
	response := invoke("/items/:id/audit")
	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Data []domaudited.AuditRecord `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	require.Equal(t, appcrud.ActionCreate, body.Data[0].Action)
	require.Equal(t, []string{"items:api:read", "items:api:audit_read", "items:api:audit_read"}, permissions)
}
