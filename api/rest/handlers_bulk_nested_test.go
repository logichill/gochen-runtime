package rest

import (
	"net/http"
	"testing"

	"gochen/app"
	"gochen/app/crud"
	"gochen/auth"
	domcrud "gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/testkit"
	"gochen/testkit/require"
)

type BulkAccess struct {
	Department int64 `json:"department" dim:"department"`
}

type nestedDimensionEntity struct {
	domcrud.Entity[int64]
	BulkAccess `json:"access"`
	Name       string `json:"name"`
}

func TestBulkUpdateNestedDimensions(t *testing.T) {
	for _, mutable := range []bool{false, true} {
		name := "protected"
		contract := app.ResourceContract{}
		if mutable {
			name = "mutable"
			contract.Mutable = []meta.Dimension{"department"}
		}
		t.Run(name, func(t *testing.T) {
			repository, err := testkit.NewMemoryRepository[*nestedDimensionEntity, int64](testkit.NewInt64Sequence(1),
				testkit.WithMemoryResourceContract[*nestedDimensionEntity, int64](contract))
			require.NoError(t, err)
			application, err := crud.NewApplication(repository, crud.Options[*nestedDimensionEntity, int64]{DeleteMode: crud.DeletePurge, Resource: "items", Authorizer: auth.Allow()})
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, application.Create(t.Context(), &nestedDimensionEntity{BulkAccess: BulkAccess{Department: 7}, Name: "original"}))
			}
			router := testkit.NewRecordingRouter()
			require.NoError(t, Mount(router, application, WithBasePath[*nestedDimensionEntity, int64]("/items")))
			response := callRecordedHandler(t, router, t.Context(), http.MethodPut, "/items/bulk", "/items/bulk", `[{"id":1,"version":0,"name":"omitted","access":{}}]`)
			require.Equal(t, http.StatusOK, response.Code)
			first, err := application.Get(t.Context(), 1)
			require.NoError(t, err)
			require.Equal(t, int64(7), first.Department)
			response = callRecordedHandler(t, router, t.Context(), http.MethodPut, "/items/bulk", "/items/bulk", `[{"id":1,"version":1,"name":"changed","access":{"department":7}},{"id":2,"version":0,"name":"explicit","ACCESS":{"DEPARTMENT":0}}]`)
			status := http.StatusForbidden
			if mutable {
				status = http.StatusOK
			}
			require.Equal(t, status, response.Code)
			first, err = application.Get(t.Context(), 1)
			require.NoError(t, err)
			second, err := application.Get(t.Context(), 2)
			require.NoError(t, err)
			if mutable {
				require.Equal(t, "changed", first.Name)
				require.Equal(t, uint64(2), first.Version)
				require.Equal(t, int64(0), second.Department)
				require.Equal(t, uint64(1), second.Version)
			} else {
				require.Equal(t, "omitted", first.Name)
				require.Equal(t, uint64(1), first.Version)
				require.Equal(t, int64(7), second.Department)
				require.Equal(t, uint64(0), second.Version)
			}
		})
	}
}
