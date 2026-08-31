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

// L2 跨隔离只读阀门的 SQL 侧行为特征测试。
//
// 与 gochen/testkit/memory_repository_cross_isolation_test.go 覆盖**同一组语义事实**：
//  1. 两道闸都合上才放行；
//  2. 缺任何一道都按常规隔离处理；
//  3. 放行必然留痕，留痕失败即拒绝；
//  4. 写路径永不放行；
//  5. 资源边界解析永不放行。

type recordingCrossIsolationAudit struct {
	records  []CrossIsolationRead
	failWith error
}

func (a *recordingCrossIsolationAudit) RecordCrossIsolationRead(_ context.Context, read CrossIsolationRead) error {
	if a.failWith != nil {
		return a.failWith
	}
	a.records = append(a.records, read)
	return nil
}

func newCrossIsolationRepo(tb testing.TB, audit ICrossIsolationAudit) *Repo[*sqlIsoDoc, int64] {
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
		WithCrossIsolationReads[*sqlIsoDoc, int64](audit),
	)
	require.NoError(tb, err)
	return repository
}

func crossIsolationCtx(tb testing.TB) context.Context {
	tb.Helper()
	ctx, err := contextx.WithCrossIsolation(context.Background(), contextx.CrossIsolation{
		Reason:   "monthly_billing_reconciliation",
		Operator: "platform_cron_worker",
	})
	require.NoError(tb, err)
	return ctx
}

func seedTwoTenants(tb testing.TB, repository *Repo[*sqlIsoDoc, int64]) {
	tb.Helper()
	require.NoError(tb, repository.Create(sqlTenantCtx(tb, "tenant-a"), &sqlIsoDoc{ID: 1, Title: "a"}))
	require.NoError(tb, repository.Create(sqlTenantCtx(tb, "tenant-b"), &sqlIsoDoc{ID: 2, Title: "b"}))
}

// 1) 两道闸都合上：读跨越全部隔离空间，且留下一条痕迹。
func TestSQLCrossIsolation_ReadsSpanAllSpacesAndAreAudited(t *testing.T) {
	audit := &recordingCrossIsolationAudit{}
	repository := newCrossIsolationRepo(t, audit)
	seedTwoTenants(t, repository)

	docs, err := repository.ListAll(crossIsolationCtx(t))
	require.NoError(t, err)
	require.Equal(t, 2, len(docs))

	require.Equal(t, 1, len(audit.records))
	require.Equal(t, "iso_docs", audit.records[0].Table)
	require.Equal(t, "tenant_id", audit.records[0].IsolationColumn)
	require.Equal(t, "monthly_billing_reconciliation", audit.records[0].Reason)
	require.Equal(t, "platform_cron_worker", audit.records[0].Operator)

	count, err := repository.Count(crossIsolationCtx(t))
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.Equal(t, 2, len(audit.records))

	// 单体读同样放行：数据迁移与巡检需要按 ID 取到别的隔离空间的记录。
	got, err := repository.Get(crossIsolationCtx(t), 2)
	require.NoError(t, err)
	require.Equal(t, "b", got.Title)
}

// 2) 缺请求期凭据：声明了阀门的仓储在普通请求上依然严格隔离，且不留痕。
func TestSQLCrossIsolation_WithoutCredentialStaysIsolated(t *testing.T) {
	audit := &recordingCrossIsolationAudit{}
	repository := newCrossIsolationRepo(t, audit)
	seedTwoTenants(t, repository)

	docs, err := repository.ListAll(sqlTenantCtx(t, "tenant-a"))
	require.NoError(t, err)
	require.Equal(t, 1, len(docs))
	require.Equal(t, "a", docs[0].Title)

	_, err = repository.Get(sqlTenantCtx(t, "tenant-b"), 1)
	require.True(t, errors.Is(err, errors.NotFound))

	// 无租户且无凭据仍然 fail-closed。
	_, err = repository.Count(context.Background())
	require.True(t, errors.Is(err, errors.Forbidden))

	require.Equal(t, 0, len(audit.records))
}

// 3) 缺构造期声明：凭据落到未声明阀门的仓储上完全惰性。
func TestSQLCrossIsolation_CredentialIsInertWithoutDeclaration(t *testing.T) {
	repository := newSQLIsolatedRepo(t)
	seedTwoTenants(t, repository)

	_, err := repository.Count(crossIsolationCtx(t))
	require.True(t, errors.Is(err, errors.Forbidden))

	ctx, err := contextx.WithTenantID(crossIsolationCtx(t), "tenant-a")
	require.NoError(t, err)
	docs, err := repository.ListAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, len(docs))
}

// 4) 留痕失败即拒绝：记不下的开闸不得降级为普通隔离读，那会让审计沉默表现为一切正常。
func TestSQLCrossIsolation_AuditFailureBlocksRead(t *testing.T) {
	audit := &recordingCrossIsolationAudit{
		failWith: errors.NewCode(errors.Database, "audit sink unavailable"),
	}
	repository := newCrossIsolationRepo(t, audit)
	seedTwoTenants(t, repository)

	_, err := repository.ListAll(crossIsolationCtx(t))
	require.True(t, errors.Is(err, errors.Forbidden))
}

// 5) 写路径永不放行，且报错说清"开闸只含读"。
func TestSQLCrossIsolation_WritesAreAlwaysRefused(t *testing.T) {
	audit := &recordingCrossIsolationAudit{}
	repository := newCrossIsolationRepo(t, audit)
	seedTwoTenants(t, repository)

	ctx := crossIsolationCtx(t)
	require.True(t, errors.Is(repository.Create(ctx, &sqlIsoDoc{ID: 3, Title: "c"}), errors.Forbidden))
	require.True(t, errors.Is(repository.Update(ctx, &sqlIsoDoc{ID: 1, Title: "tampered"}), errors.Forbidden))
	require.True(t, errors.Is(repository.Delete(ctx, 1), errors.Forbidden))
	require.True(t, errors.Is(repository.Purge(ctx, 1), errors.Forbidden))
	require.True(t, errors.Is(repository.CreateAll(ctx, []*sqlIsoDoc{{ID: 4}}), errors.Forbidden))
	require.True(t, errors.Is(repository.UpdateAll(ctx, []*sqlIsoDoc{{ID: 1}}), errors.Forbidden))
	require.True(t, errors.Is(repository.DeleteAll(ctx, []int64{1}), errors.Forbidden))

	// 带着隔离键的写同样被拒——否则一次本以为是全局的批处理会静默只写中一个空间。
	writeCtx, err := contextx.WithTenantID(crossIsolationCtx(t), "tenant-a")
	require.NoError(t, err)
	require.True(t, errors.Is(repository.Delete(writeCtx, 1), errors.Forbidden))

	// 数据一条都没动。
	docs, err := repository.ListAll(crossIsolationCtx(t))
	require.NoError(t, err)
	require.Equal(t, 2, len(docs))
	require.Equal(t, "a", docs[0].Title)
}

// 6) 资源边界解析永不放行：它是 L3 判定的输入，必须始终限定在本隔离空间内。
func TestSQLCrossIsolation_ResourceBoundaryStaysIsolated(t *testing.T) {
	audit := &recordingCrossIsolationAudit{}
	repository := newCrossIsolationRepo(t, audit)
	seedTwoTenants(t, repository)

	ctx := crossIsolationCtx(t)
	_, err := repository.ResolveResourceByID(ctx, 1)
	require.True(t, errors.Is(err, errors.Forbidden))

	_, err = repository.ResolveResourcesByID(ctx, []int64{1})
	require.True(t, errors.Is(err, errors.Forbidden))

	require.Equal(t, 0, len(audit.records))
}

// 7) 构造期 fail-fast：没有留痕出口、或没有隔离可放的声明一律报错。
func TestSQLCrossIsolation_DeclarationIsValidatedAtConstruction(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	_, err = database.Exec(context.Background(),
		`CREATE TABLE iso_docs (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '')`)
	require.NoError(t, err)
	ormEngine, err := lite.New(database)
	require.NoError(t, err)

	_, err = NewRepo[*sqlIsoDoc, int64](ormEngine, "iso_docs",
		WithIsolation[*sqlIsoDoc, int64](IsolationCols{Column: "tenant_id"}),
		WithCrossIsolationReads[*sqlIsoDoc, int64](nil),
	)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = NewRepo[*sqlIsoDoc, int64](ormEngine, "iso_docs",
		WithCrossIsolationReads[*sqlIsoDoc, int64](&recordingCrossIsolationAudit{}),
	)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 8) require-scope 仓储不能靠开闸绕过边界：隔离条件没落地就等于没有边界。
func TestSQLCrossIsolation_RequireScopeStillNeedsABoundary(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1,
	})
	require.NoError(t, err)
	defer func() { _ = database.Close() }()
	_, err = database.Exec(context.Background(),
		`CREATE TABLE iso_docs (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '')`)
	require.NoError(t, err)
	ormEngine, err := lite.New(database)
	require.NoError(t, err)

	audit := &recordingCrossIsolationAudit{}
	repository, err := NewRepo[*sqlIsoDoc, int64](ormEngine, "iso_docs",
		WithIsolation[*sqlIsoDoc, int64](IsolationCols{Column: "tenant_id"}),
		WithCrossIsolationReads[*sqlIsoDoc, int64](audit),
		WithRequireScope[*sqlIsoDoc, int64](),
	)
	require.NoError(t, err)

	_, err = repository.Count(crossIsolationCtx(t))
	require.True(t, errors.Is(err, errors.Forbidden))
}
