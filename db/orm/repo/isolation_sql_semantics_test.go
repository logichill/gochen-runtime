package repo

import (
	"context"
	"testing"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/contextx"
	"gochen/db"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

// L2 空间隔离的 SQL 侧行为特征测试。
//
// 与 gochen/testkit/memory_repository_isolation_test.go 覆盖**同一组语义事实**，
// 两侧共同保证跨存储一致。

type sqlIsoDoc struct {
	ID       int64  `json:"id"`
	TenantID string `json:"tenant_id"`
	Title    string `json:"title"`
}

func (d *sqlIsoDoc) GetID() int64                { return d.ID }
func (d *sqlIsoDoc) GetVersion() uint64          { return 0 }
func (d *sqlIsoDoc) GetTenantID() string         { return d.TenantID }
func (d *sqlIsoDoc) SetTenantID(tenantID string) { d.TenantID = tenantID }

func newSQLIsolatedRepo(tb testing.TB) *Repo[*sqlIsoDoc, int64] {
	tb.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(),
		`CREATE TABLE iso_docs (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '')`)
	require.NoError(tb, err)

	ormEngine, err := lite.New(database)
	require.NoError(tb, err)

	repository, err := NewRepo[*sqlIsoDoc, int64](ormEngine, "iso_docs",
		WithIsolation[*sqlIsoDoc, int64](IsolationCols{Column: "tenant_id"}),
	)
	require.NoError(tb, err)
	return repository
}

func sqlTenantCtx(tb testing.TB, tenantID string) context.Context {
	tb.Helper()
	ctx, err := contextx.WithTenantID(context.Background(), tenantID)
	require.NoError(tb, err)
	return ctx
}

// 1) Create 标记归属
func TestSQLIsolation_CreateStampsOwnership(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	doc := &sqlIsoDoc{ID: 1, Title: "a"}
	require.NoError(t, repository.Create(sqlTenantCtx(t, "tenant-a"), doc))
	require.Equal(t, "tenant-a", doc.TenantID)
}

// 2) 读路径按归属过滤，跨租户读为 NotFound
func TestSQLIsolation_ReadsAreScopedToTenant(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	ctxA, ctxB := sqlTenantCtx(t, "tenant-a"), sqlTenantCtx(t, "tenant-b")

	require.NoError(t, repository.Create(ctxA, &sqlIsoDoc{ID: 1, Title: "a"}))
	require.NoError(t, repository.Create(ctxB, &sqlIsoDoc{ID: 2, Title: "b"}))

	_, err := repository.Get(ctxB, 1)
	require.True(t, errors.Is(err, errors.NotFound))

	got, err := repository.Get(ctxA, 1)
	require.NoError(t, err)
	require.Equal(t, "a", got.Title)

	countA, err := repository.Count(ctxA)
	require.NoError(t, err)
	require.Equal(t, int64(1), countA)

	exists, err := repository.Exists(ctxB, 1)
	require.NoError(t, err)
	require.False(t, exists)
}

// 3) 跨租户写被守卫，且数据不被篡改
func TestSQLIsolation_CrossTenantWritesAreGuarded(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	ctxA, ctxB := sqlTenantCtx(t, "tenant-a"), sqlTenantCtx(t, "tenant-b")

	require.NoError(t, repository.Create(ctxA, &sqlIsoDoc{ID: 1, Title: "a"}))

	err := repository.Delete(ctxB, 1)
	require.Error(t, err)

	got, err := repository.Get(ctxA, 1)
	require.NoError(t, err)
	require.Equal(t, "a", got.Title)
}

// 4) Fail-Closed：无租户一律拒绝
func TestSQLIsolation_FailsClosedWithoutTenant(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	ctx := context.Background()

	_, err := repository.Get(ctx, 1)
	require.Error(t, err)

	_, err = repository.Count(ctx)
	require.Error(t, err)
}

// 资源边界查询会绕过 L3 DataScope 供 PDP 判定，但不得绕过更底层的 L2 tenant 隔离。
func TestSQLIsolation_ResourceBoundaryReadsAreScopedToTenant(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	ctxA, ctxB := sqlTenantCtx(t, "tenant-a"), sqlTenantCtx(t, "tenant-b")

	require.NoError(t, repository.Create(ctxA, &sqlIsoDoc{ID: 1, Title: "a"}))

	_, err := repository.ResolveResourceByID(ctxB, 1)
	require.True(t, errors.Is(err, errors.NotFound))

	_, err = repository.ResolveResourcesByID(ctxB, []int64{1})
	require.True(t, errors.Is(err, errors.NotFound))

	_, err = repository.ResolveResourceByIDIncludingDeleted(ctxB, 1)
	require.True(t, errors.Is(err, errors.NotFound))

	resource, err := repository.ResolveResourceByID(ctxA, 1)
	require.NoError(t, err)
	require.Equal(t, "1", resource.ID)
	require.Equal(t, "tenant-a", resource.TenantID)
}
