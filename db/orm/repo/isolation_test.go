package repo

import (
	"context"
	"testing"

	"gochen/app/query"
	"gochen/contextx"
	"gochen/errors"
	"gochen/testkit/require"
)

// isolationAwareEntity 实现 ITenantEntity，可承载隔离归属。
type isolationAwareEntity struct {
	ID       int64  `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
}

func (e *isolationAwareEntity) GetID() int64                { return e.ID }
func (e *isolationAwareEntity) GetVersion() uint64          { return 0 }
func (e *isolationAwareEntity) GetTenantID() string         { return e.TenantID }
func (e *isolationAwareEntity) SetTenantID(tenantID string) { e.TenantID = tenantID }

// isolationUnawareEntity 带 tenant_id 列但**未**实现 ITenantEntity。
type isolationUnawareEntity struct {
	ID       int64  `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
}

func (e *isolationUnawareEntity) GetID() int64       { return e.ID }
func (e *isolationUnawareEntity) GetVersion() uint64 { return 0 }

// §1.5.1 Fail-Closed 铁律：声明隔离列但实体无法承载归属 → 构造期 fail-fast，
// 而不是等到 Create 时才发现 tenant_id 写不进去。
func TestNewRepo_Isolation_FailsFastWhenEntityNotTenantAware(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*isolationUnawareEntity, int64](ormEngine, "entities",
		WithIsolation[*isolationUnawareEntity, int64](IsolationCols{Column: "tenant_id"}),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Unsupported))
}

func TestNewRepo_Isolation_AcceptsTenantAwareEntity(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*isolationAwareEntity, int64](ormEngine, "entities",
		WithIsolation[*isolationAwareEntity, int64](IsolationCols{Column: "tenant_id"}),
	)
	require.NoError(t, err)
}

// 未声明隔离时，实体是否 tenant-aware 都不做要求（L0/L1 不背负 L2 心智）。
func TestNewRepo_WithoutIsolation_NoEntityRequirement(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*isolationUnawareEntity, int64](ormEngine, "entities")
	require.NoError(t, err)
}

func TestNewRepo_RejectsInvalidWriteMissMode(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*isolationUnawareEntity, int64](ormEngine, "entities",
		WithWriteMissMode[*isolationUnawareEntity, int64](WriteMissMode(99)),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 核心回归：实体带 tenant_id 字段、ctx 也绑定了租户，
// 但未经 WithIsolation 声明 → **不得**追加任何隔离过滤。
// 这正是取代旧“隐式字段名推断”的行为。
func TestRepo_NoImplicitIsolationWithoutDeclaration(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*isolationAwareEntity, int64]{model: model}

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	require.Empty(t, model.lastFindOpts.Where, "未声明隔离列时不得注入 tenant 过滤")
}

// 显式声明后隔离过滤必须生效。
func TestRepo_ExplicitIsolationAppliesFilter(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*isolationAwareEntity, int64]{
		model:         model,
		accessColumns: accessColumns{isolation: "tenant_id"},
	}

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

// orgScopedEntity 按 org_id 隔离，且**不是** tenant-aware——
// 这正是自定义 Resolve 要支撑的形态。
type orgScopedEntity struct {
	ID    int64  `json:"id"`
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

func (e *orgScopedEntity) GetID() int64       { return e.ID }
func (e *orgScopedEntity) GetVersion() uint64 { return 0 }

type orgKey struct{}

func withOrg(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, orgKey{}, org)
}

func orgResolver(ctx context.Context) (string, error) {
	org, _ := ctx.Value(orgKey{}).(string)
	return org, nil
}

// 自定义 Resolve 时不再要求实体实现 ITenantEntity：隔离键按定义就不是租户，
// 归属由 Column 映射的字段承载（映射失败仍由 validateAccessColumns 拦截）。
func TestNewRepo_Isolation_CustomResolveDropsTenantEntityRequirement(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*orgScopedEntity, int64](ormEngine, "entities",
		WithIsolation[*orgScopedEntity, int64](IsolationCols{
			Column:  "org_id",
			Resolve: orgResolver,
		}),
	)
	require.NoError(t, err)
}

// 声明的隔离列映射不到实体字段时，构造期仍然 fail-fast——
// 放宽 ITenantEntity 断言不等于放弃归属保障。
func TestNewRepo_Isolation_CustomResolveStillRequiresMappedColumn(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*orgScopedEntity, int64](ormEngine, "entities",
		WithIsolation[*orgScopedEntity, int64](IsolationCols{
			Column:  "no_such_column",
			Resolve: orgResolver,
		}),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 隔离键取自自定义来源，而不是 contextx.TenantID。
func TestRepo_Isolation_CustomResolveDrivesFilter(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*orgScopedEntity, int64]{
		model:            model,
		accessColumns:    accessColumns{isolation: "org_id"},
		isolationResolve: orgResolver,
	}

	// ctx 里同时放了一个**不同**的租户：过滤必须用 org，不能被租户槽位污染。
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	ctx = withOrg(ctx, "org-7")

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "org_id = ?", "org-7")
}

// 自定义来源解析不到 → fail-closed，且错误里点明来源，便于排查。
func TestRepo_Isolation_CustomResolveMissingKeyFailsClosed(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*orgScopedEntity, int64]{
		model:            model,
		accessColumns:    accessColumns{isolation: "org_id"},
		isolationResolve: orgResolver,
	}

	// 租户在，但 org 不在——旧实现会拿租户顶上，现在必须拒绝。
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	// 错误上下文必须点明取值来源，否则排查者会去 contextx 里找一个根本没用上的租户。
	appErr, ok := errors.AsType[errors.IAppError](err)
	require.True(t, ok)
	details := appErr.Details()
	require.Equal(t, "org_id", details["isolation_column"])
	require.Contains(t, details["isolation_source"].(string), "IsolationCols.Resolve")
}

// 解析器自身报错必须原样上抛，绝不能被当成"无隔离"而放行。
func TestRepo_Isolation_ResolverErrorIsNotSwallowed(t *testing.T) {
	model := &capturingDataScopeModel{}
	sentinel := errors.NewCode(errors.ServiceUnavailable, "shard directory is down")
	repository := &Repo[*orgScopedEntity, int64]{
		model:         model,
		accessColumns: accessColumns{isolation: "org_id"},
		isolationResolve: func(context.Context) (string, error) {
			return "", sentinel
		},
	}

	_, err := repository.Query(context.Background(), query.QueryOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.ServiceUnavailable))
}

// --- IsolationCols 的默认值语义（R9）---
//
// 区分两个"默认"：**是否启用**隔离没有默认（不调用 WithIsolation 就完全不启用）；
// 一旦调用了 WithIsolation，就已经显式声明了隔离意图，此时列名与来源才谈得上默认。

// 零值 IsolationCols 即"按租户隔离"，等价于 Column: "tenant_id"。
func TestWithIsolation_ZeroValueDefaultsToTenantColumn(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	repository, err := NewRepo[*isolationAwareEntity, int64](ormEngine, "entities",
		WithIsolation[*isolationAwareEntity, int64](IsolationCols{}),
	)
	require.NoError(t, err)

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

// 零值声明同样 fail-closed：无租户即拒绝，绝不退化为"看全部"。
func TestWithIsolation_ZeroValueStillFailsClosed(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	repository, err := NewRepo[*isolationAwareEntity, int64](ormEngine, "entities",
		WithIsolation[*isolationAwareEntity, int64](IsolationCols{}),
	)
	require.NoError(t, err)

	_, err = repository.Query(context.Background(), query.QueryOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
}

// 配了 Resolve 却没给 Column → 构造期报错。
//
// 这里**不能**回落到 tenant_id 默认值：自定义来源多半不是租户，
// 把分片键悄悄写进租户列比报错更糟。
func TestWithIsolation_CustomResolveWithoutColumnFailsFast(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	_, err := NewRepo[*isolationAwareEntity, int64](ormEngine, "entities",
		WithIsolation[*isolationAwareEntity, int64](IsolationCols{Resolve: orgResolver}),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "no IsolationCols.Column")
}

// 默认列同样要能映射到实体字段，否则构造期报错——
// 默认值是便利，不是豁免。
func TestWithIsolation_ZeroValueStillRequiresMappedColumn(t *testing.T) {
	ormEngine := &staticRepoOrm{model: &capturingDataScopeModel{}}

	// isoNoTenantColumnEntity 实现了 ITenantEntity，但没有 tenant_id 列。
	_, err := NewRepo[*isoNoTenantColumnEntity, int64](ormEngine, "entities",
		WithIsolation[*isoNoTenantColumnEntity, int64](IsolationCols{}),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

type isoNoTenantColumnEntity struct {
	ID    int64  `json:"id"`
	Owner string `json:"owner"`
}

func (e *isoNoTenantColumnEntity) GetID() int64         { return e.ID }
func (e *isoNoTenantColumnEntity) GetVersion() uint64   { return 0 }
func (e *isoNoTenantColumnEntity) GetTenantID() string  { return e.Owner }
func (e *isoNoTenantColumnEntity) SetTenantID(v string) { e.Owner = v }

// --- 写路径的 fail-closed 口径必须与读路径一致 ---
//
// 读路径（applyIsolation）解析不到隔离键会 Forbidden；写路径若在同样条件下放行，
// 落库的就是一行隔离键为空的记录：它对任何隔离空间都不可见（后续查询恒带
// `isolation = 非空值`），成为永久孤儿数据；一旦有不经本 Repo 的读路径，
// 它还会对所有人可见。

// 默认来源（contextx.TenantID）取不到租户 → Create 必须拒绝，且不得触库。
func TestRepo_Isolation_CreateWithoutTenantFailsClosed(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*isolationAwareEntity, int64]{
		model:         model,
		resourceKind:  "entities",
		accessColumns: accessColumns{isolation: "tenant_id"},
	}
	entity := &isolationAwareEntity{ID: 1, Name: "demo"}

	err := repository.Create(context.Background(), entity)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)

	appErr, ok := errors.AsType[errors.IAppError](err)
	require.True(t, ok)
	details := appErr.Details()
	require.Equal(t, "tenant_id", details["isolation_column"])
	require.Contains(t, details["isolation_source"].(string), "contextx.TenantID")
}

// 自定义来源解析不到 → 同样拒绝，错误里点明的是 Resolve 而不是租户槽位。
func TestRepo_Isolation_CreateWithoutCustomKeyFailsClosed(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*orgScopedEntity, int64]{
		model:            model,
		resourceKind:     "entities",
		accessColumns:    accessColumns{isolation: "org_id"},
		isolationResolve: orgResolver,
	}
	entity := &orgScopedEntity{ID: 1, Name: "demo"}

	// 租户在、org 不在：绝不能拿租户顶上，也不能静默写入空 org。
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	err = repository.Create(ctx, entity)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)
	require.Equal(t, "", entity.OrgID)

	appErr, ok := errors.AsType[errors.IAppError](err)
	require.True(t, ok)
	require.Contains(t, appErr.Details()["isolation_source"].(string), "IsolationCols.Resolve")
}

// 隔离键正常解析时写路径照旧盖戳落库——上面两条不是把写路径整体锁死。
func TestRepo_Isolation_CreateStampsResolvedKey(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*isolationAwareEntity, int64]{
		model:         model,
		resourceKind:  "entities",
		accessColumns: accessColumns{isolation: "tenant_id"},
	}
	entity := &isolationAwareEntity{ID: 1, Name: "demo"}

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	err = repository.Create(ctx, entity)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", entity.TenantID)
	require.Same(t, entity, model.created)
}
