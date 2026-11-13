package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/app/query"
	"gochen/audit"
	"gochen/auth"
	"gochen/testkit/require"
)

type paginationCall struct {
	offset int
	limit  int
}

type paginationReadSurface struct {
	ReadApplication[*mountEntity, int64]
	maxPageSize int
	call        paginationCall
}

func (a *paginationReadSurface) MaxPageSize() int { return a.maxPageSize }
func (a *paginationReadSurface) List(_ context.Context, offset, limit int) ([]*mountEntity, error) {
	a.call = paginationCall{offset: offset, limit: limit}
	return []*mountEntity{{ID: int64(offset + 1)}}, nil
}
func (a *paginationReadSurface) Query(ctx context.Context, options query.QueryOptions) ([]*mountEntity, error) {
	return a.List(ctx, options.Offset, options.Limit)
}
func (a *paginationReadSurface) QueryCount(context.Context, query.QueryOptions) (int64, error) {
	return 7, nil
}

type paginationAuditSurface struct {
	AuditApplication[int64]
	maxPageSize int
	call        paginationCall
}

func (a *paginationAuditSurface) Descriptor() auth.Descriptor {
	return auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: actionAuditRead, Kind: auth.KindRead}}}
}
func (a *paginationAuditSurface) MaxPageSize() int { return a.maxPageSize }
func (a *paginationAuditSurface) AuditTrail(_ context.Context, _ int64, offset, limit int) ([]audit.AuditRecord, error) {
	a.call = paginationCall{offset: offset, limit: limit}
	return []audit.AuditRecord{{ID: int64(offset + 1)}}, nil
}

type paginationDeletionSurface struct {
	DeletionApplication[*mountEntity, int64]
	maxPageSize int
	call        paginationCall
}

func (a *paginationDeletionSurface) Descriptor() auth.Descriptor {
	return auth.Descriptor{Resource: "user", Actions: []auth.Action{{Name: actionListDeleted, Kind: auth.KindList}}}
}
func (a *paginationDeletionSurface) MaxPageSize() int { return a.maxPageSize }
func (a *paginationDeletionSurface) MaxBulkSize() int { return 100 }
func (a *paginationDeletionSurface) ListDeleted(_ context.Context, offset, limit int) ([]*mountEntity, error) {
	a.call = paginationCall{offset: offset, limit: limit}
	return []*mountEntity{{ID: int64(offset + 1)}}, nil
}

func TestPaginationUsesOwningApplicationLimit(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		route, read, audit, deletion      int
		wantRead, wantAudit, wantDeletion int
	}{
		{name: "independent limits", route: 10, read: 3, audit: 1, deletion: 2, wantRead: 3, wantAudit: 1, wantDeletion: 2},
		{name: "read limit does not restrict capabilities", route: 10, read: 1, audit: 2, deletion: 3, wantRead: 1, wantAudit: 2, wantDeletion: 3},
		{name: "route limit", route: 1, read: 3, audit: 3, deletion: 3, wantRead: 1, wantAudit: 1, wantDeletion: 1},
		{name: "no application limit", route: 2, read: 0, audit: -1, deletion: 0, wantRead: 2, wantAudit: 2, wantDeletion: 2},
		{name: "no route limit", route: 0, read: 1, audit: 2, deletion: 3, wantRead: 1, wantAudit: 2, wantDeletion: 3},
		{name: "no limits", route: 0, read: 0, audit: 0, deletion: 0, wantRead: 3, wantAudit: 3, wantDeletion: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application := &paginationReadSurface{ReadApplication: &stubApplication{descriptor: auth.Descriptor{
				Resource: "user", Actions: []auth.Action{{Name: actionList, Kind: auth.KindList}},
			}}, maxPageSize: tc.read}
			history := &paginationAuditSurface{maxPageSize: tc.audit}
			trash := &paginationDeletionSurface{maxPageSize: tc.deletion}
			server := nethttp.NewServer(nil)
			builder, err := NewRouteBuilder(application,
				WithBasePath[*mountEntity, int64]("/users"),
				WithAudit[*mountEntity, int64](history), WithDeletion[*mountEntity, int64](trash),
				func(b *RouteBuilder[*mountEntity, int64]) {
					b.config.Query.DefaultPageSize = 3
					b.config.Query.MaxPageSize = tc.route
				})
			require.NoError(t, err)
			// 重复预检和注册不应把某一能力的上限写回共享配置。
			_, _, err = builder.Describe()
			require.NoError(t, err)
			require.NoError(t, builder.Register(server.Group("")))
			require.Equal(t, tc.route, builder.config.Query.MaxPageSize)

			for _, suffix := range []string{"", "&size=3", "&page_size=3", "&size=3&page_size=1", "&size=3&fields=id", "&size=3&sorts=id:asc"} {
				for _, endpoint := range []struct {
					path string
					want int
					call *paginationCall
				}{
					{path: "/users", want: tc.wantRead, call: &application.call},
					{path: "/users/1/audit", want: tc.wantAudit, call: &history.call},
					{path: "/users/deleted", want: tc.wantDeletion, call: &trash.call},
				} {
					response := httptest.NewRecorder()
					server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint.path+"?page=2"+suffix, nil))
					require.Equalf(t, http.StatusOK, response.Code, "%s: %s", endpoint.path, response.Body.String())
					require.Equal(t, paginationCall{offset: endpoint.want, limit: endpoint.want}, *endpoint.call)
					if endpoint.path == "/users" {
						var body struct {
							Data query.PagedResult[*mountEntity] `json:"data"`
						}
						require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
						require.Equal(t, endpoint.want, body.Data.Size)
						require.Equal(t, int64(7), body.Data.Total)
						require.Equal(t, 2, body.Data.Page)
						require.Equal(t, (7+endpoint.want-1)/endpoint.want, body.Data.TotalPages)
						require.Equal(t, int64(endpoint.want+1), body.Data.Data[0].ID)
						require.True(t, body.Data.HasNext)
						require.True(t, body.Data.HasPrev)
					}
				}
			}
		})
	}
}

func TestCapabilityPaginationValidatesOffsetAfterClamping(t *testing.T) {
	builder := &RouteBuilder[*mountEntity, int64]{config: DefaultRouteConfig[int64]()}
	maximum := math.MaxInt
	response := httptest.NewRecorder()
	c, err := nethttp.NewBaseContext(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/?page=%d&size=3", maximum), nil))
	require.NoError(t, err)
	options, err := builder.parsePaginationOptions(c, 1)
	require.NoError(t, err)
	require.Equal(t, 1, options.Size)
	require.Equal(t, maximum-1, options.Offset())
	_, err = builder.parsePaginationOptions(c, 2)
	require.Error(t, err)
}
