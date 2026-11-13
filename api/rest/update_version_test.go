package rest

import (
	"context"
	"net/http"
	"testing"

	"gochen/app/crud"
	"gochen/auth"
	"gochen/testkit"
	"gochen/testkit/require"
)

func TestPlainUpdatesRequireCallerVersion(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, field := range []string{"", `,"version":null`, `,"version":-1`, `,"version":18446744073709551616`, `,"version":0`, `,"version":1`} {
			t.Run(field+map[bool]string{true: "/bulk", false: "/single"}[bulk], func(t *testing.T) {
				ctx := context.Background()
				repo, err := testkit.NewMemoryRepository[*timestampHandlerEntity, int64](testkit.NewInt64Sequence(1))
				require.NoError(t, err)
				app, err := crud.NewApplication(repo, crud.Options[*timestampHandlerEntity, int64]{DeleteMode: crud.DeletePurge, Resource: "items", Authorizer: auth.Allow()})
				require.NoError(t, err)
				entity := &timestampHandlerEntity{Name: "initial"}
				require.NoError(t, app.Create(ctx, entity))
				entity.Name = "latest"
				require.NoError(t, app.Update(ctx, entity))
				router := testkit.NewRecordingRouter()
				require.NoError(t, Mount(router, app, WithBasePath[*timestampHandlerEntity, int64]("/items")))
				path, url, body := "/items/:id", "/items/1", `{"id":1,"name":"submitted"`+field+`}`
				if bulk {
					path, url, body = "/items/bulk", "/items/bulk", "["+body+"]"
				}
				response := callRecordedHandler(t, router, ctx, http.MethodPut, path, url, body)
				status := http.StatusBadRequest
				if field == `,"version":0` {
					status = http.StatusConflict
				}
				if field == `,"version":1` {
					status = http.StatusOK
				}
				require.Equal(t, status, response.Code)
				stored, err := app.Get(ctx, entity.ID)
				require.NoError(t, err)
				if status == http.StatusOK {
					require.Equal(t, "submitted", stored.Name)
					require.Equal(t, uint64(2), stored.Version)
				} else {
					require.Equal(t, "latest", stored.Name)
					require.Equal(t, uint64(1), stored.Version)
				}
			})
		}
	}
}

func TestPlainBulkVersionConflictRollsBackEarlierItems(t *testing.T) {
	ctx := context.Background()
	repo, err := testkit.NewMemoryRepository[*timestampHandlerEntity, int64](testkit.NewInt64Sequence(1))
	require.NoError(t, err)
	app, err := crud.NewApplication(repo, crud.Options[*timestampHandlerEntity, int64]{DeleteMode: crud.DeletePurge, Resource: "items", Authorizer: auth.Allow()})
	require.NoError(t, err)
	first, second := &timestampHandlerEntity{Name: "first"}, &timestampHandlerEntity{Name: "second"}
	require.NoError(t, app.Create(ctx, first))
	require.NoError(t, app.Create(ctx, second))
	second.Name = "latest"
	require.NoError(t, app.Update(ctx, second))
	router := testkit.NewRecordingRouter()
	require.NoError(t, Mount(router, app, WithBasePath[*timestampHandlerEntity, int64]("/items")))
	response := callRecordedHandler(t, router, ctx, http.MethodPut, "/items/bulk", "/items/bulk",
		`[{"id":1,"name":"changed","version":0},{"id":2,"name":"stale","version":0}]`)
	require.Equal(t, http.StatusConflict, response.Code)
	stored, err := app.Get(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, "first", stored.Name)
	require.Equal(t, uint64(0), stored.Version)
	stored, err = app.Get(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, "latest", stored.Name)
	require.Equal(t, uint64(1), stored.Version)
	response = callRecordedHandler(t, router, ctx, http.MethodPut, "/items/:id", "/items/1", `{"name":"changed","version":0}`)
	require.Equal(t, http.StatusOK, response.Code)
	stored, err = app.Get(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, "changed", stored.Name)
	require.Equal(t, uint64(1), stored.Version)
}
