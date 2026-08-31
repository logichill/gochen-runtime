package repo

import (
	"context"
	"database/sql"
	"testing"

	"gochen/testkit/require"

	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
)

type guardedWriteModel struct {
	lastSaveOpts         orm.QueryOptions
	lastDeleteOpts       orm.QueryOptions
	lastUpdateValuesOpts orm.QueryOptions
	created              any
	saveAffected         int64
	deleteAffected       int64
	updateAffected       int64
	rawCount             int64
	scopedVisible        bool
	visibleEntity        *scopedRepoEntity
	firstCalls           int
}

type guardedWriteSession struct {
	model      orm.IModel
	dispatcher contextx.IAfterCommitDispatcher
}

type tenantWriteEntity struct {
	ID       int64
	Version  uint64
	TenantID string
}

func (e *tenantWriteEntity) GetID() int64       { return e.ID }
func (e *tenantWriteEntity) GetVersion() uint64 { return e.Version }

type tenantOwnerWriteEntity struct {
	ID             int64
	Version        uint64
	TenantID       string
	ManagedScopeID int64
	OwnerID        string
}

func (e *tenantOwnerWriteEntity) GetID() int64       { return e.ID }
func (e *tenantOwnerWriteEntity) GetVersion() uint64 { return e.Version }

func (m *guardedWriteModel) Meta() *orm.ModelMeta           { return &orm.ModelMeta{Table: "scoped_entities"} }
func (m *guardedWriteModel) Capabilities() orm.Capabilities { return nil }
func (m *guardedWriteModel) Dialect() repoTestDialect       { return unknownRepoTestDialect() }
func (m *guardedWriteModel) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	m.firstCalls++
	_ = ctx
	_ = opts
	if !m.scopedVisible {
		return errors.NewCode(errors.NotFound, "record not found")
	}
	switch typed := dest.(type) {
	case **scopedRepoEntity:
		if m.visibleEntity == nil {
			return errors.NewCode(errors.NotFound, "record not found")
		}
		*typed = m.visibleEntity
	case *scopedRepoEntity:
		if m.visibleEntity == nil {
			return errors.NewCode(errors.NotFound, "record not found")
		}
		*typed = *m.visibleEntity
	}
	return nil
}
func (m *guardedWriteModel) Find(context.Context, any, ...orm.QueryOption) error { return nil }
func (m *guardedWriteModel) Count(ctx context.Context, opts ...orm.QueryOption) (int64, error) {
	_ = ctx
	collected := orm.CollectQueryOptions(opts...)
	if hasWhereExpr(collected, "managed_scope_id = ?") {
		if m.scopedVisible {
			return 1, nil
		}
		return 0, nil
	}
	return m.rawCount, nil
}
func (m *guardedWriteModel) Create(ctx context.Context, entities ...any) error {
	_ = ctx
	if len(entities) > 0 {
		m.created = entities[0]
	}
	return nil
}
func (s *guardedWriteSession) Capabilities() orm.Capabilities           { return nil }
func (s *guardedWriteSession) WithContext(context.Context) orm.IOrm     { return s }
func (s *guardedWriteSession) Model(*orm.ModelMeta) (orm.IModel, error) { return s.model, nil }
func (s *guardedWriteSession) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *guardedWriteSession) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *guardedWriteSession) Database() db.IDatabase { return nil }
func (s *guardedWriteSession) Commit() error          { return nil }
func (s *guardedWriteSession) Rollback() error        { return nil }
func (s *guardedWriteSession) AfterCommitDispatcher() contextx.IAfterCommitDispatcher {
	if s.dispatcher == nil {
		s.dispatcher = contextx.NewAfterCommitDispatcher()
	}
	return s.dispatcher
}
func (m *guardedWriteModel) Save(context.Context, any, ...orm.QueryOption) error { panic("not used") }
func (m *guardedWriteModel) SaveWithResult(ctx context.Context, entity any, opts ...orm.QueryOption) (sql.Result, error) {
	_ = ctx
	_ = entity
	m.lastSaveOpts = orm.CollectQueryOptions(opts...)
	return fakeResult(m.saveAffected), nil
}
func (m *guardedWriteModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	panic("not used")
}
func (m *guardedWriteModel) UpdateValuesWithResult(ctx context.Context, values map[string]any, opts ...orm.QueryOption) (sql.Result, error) {
	_ = ctx
	_ = values
	m.lastUpdateValuesOpts = orm.CollectQueryOptions(opts...)
	return fakeResult(m.updateAffected), nil
}
func (m *guardedWriteModel) Delete(context.Context, ...orm.QueryOption) error { panic("not used") }
func (m *guardedWriteModel) DeleteWithResult(ctx context.Context, opts ...orm.QueryOption) (sql.Result, error) {
	_ = ctx
	m.lastDeleteOpts = orm.CollectQueryOptions(opts...)
	return fakeResult(m.deleteAffected), nil
}
func (m *guardedWriteModel) Association(any, string) orm.IAssociation { return nil }

func TestRepo_CreateWithConstraint_AlignsManagedScope(t *testing.T) {
	model := &guardedWriteModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 7}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	constraint := scoped.Allow(scoped.Resource{Kind: "document", ManagedScopeID: 701}).WriteConstraint()

	err = repository.CreateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	require.Equal(t, int64(701), entity.ManagedScopeID)
	require.Same(t, entity, model.created)
}

func TestRepo_UpdateWithConstraint_AppliesRevisionAndManagedScopePredicates(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected:  1,
		scopedVisible: true,
		visibleEntity: &scopedRepoEntity{ID: 7, Version: 5, ManagedScopeID: 701},
		rawCount:      1,
	}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 7, Version: 5}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	constraint := scoped.Allow(scoped.Resource{
		Kind:           "document",
		ID:             "7",
		ManagedScopeID: 701,
		Revision:       "5",
	}).WriteConstraint()

	err = repository.UpdateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastSaveOpts.Where, "managed_scope_id = ?", int64(701))
	requireCondition(t, model.lastSaveOpts.Where, "version = ?", uint64(5))
}

func TestRepo_CreateWithConstraint_AlignsTenantID(t *testing.T) {
	model := &guardedWriteModel{}
	repository := &Repo[*tenantWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", version: "version"}}
	entity := &tenantWriteEntity{ID: 7}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		TenantID: "tenant-a",
	}).WriteConstraint()

	err := repository.CreateWithConstraint(mustTenantCtx(t, "tenant-a"), entity, constraint)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", entity.TenantID)
	require.Same(t, entity, model.created)
}

func TestRepo_CreateWithConstraint_AlignsTenantWhenOwnerColumnIsExplicit(t *testing.T) {
	model := &guardedWriteModel{}
	repository := &Repo[*tenantOwnerWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	WithScope[*tenantOwnerWriteEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"})(repository)
	entity := &tenantOwnerWriteEntity{ID: 7, OwnerID: "owner-a"}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		TenantID: "tenant-a",
	}).WriteConstraint()
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	err = repository.CreateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", entity.TenantID)
	require.Equal(t, int64(701), entity.ManagedScopeID)
	require.Equal(t, "owner-a", entity.OwnerID)
	require.Same(t, entity, model.created)
}

func TestRepo_UpdateWithConstraint_AppliesTenantPredicate(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected: 1,
	}
	repository := &Repo[*tenantWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", version: "version"}}
	entity := &tenantWriteEntity{ID: 7, Version: 5, TenantID: "tenant-a"}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		ID:       "7",
		TenantID: "tenant-a",
		Revision: "5",
	}).WriteConstraint()
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	err = repository.UpdateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastSaveOpts.Where, "tenant_id = ?", "tenant-a")
	requireCondition(t, model.lastSaveOpts.Where, "version = ?", uint64(5))
}

func TestRepo_UpdateWithConstraint_AppliesExplicitTenantColumn(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected: 1,
	}
	repository := &Repo[*customTaggedScopedEntity, int64]{
		model:        model,
		resourceKind: "document",
	}
	WithIsolation[*customTaggedScopedEntity, int64](IsolationCols{Column: "tenant_ref"})(repository)
	WithScope[*customTaggedScopedEntity, int64](ScopeCols{Revision: "revision_no"})(repository)
	entity := &customTaggedScopedEntity{ID: 7, Revision: 5, Tenant: "tenant-a"}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		ID:       "7",
		TenantID: "tenant-a",
		Revision: "5",
	}).WriteConstraint()

	err := repository.UpdateWithConstraint(mustTenantCtx(t, "tenant-a"), entity, constraint)
	require.NoError(t, err)
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastSaveOpts.Where, "tenant_ref = ?", "tenant-a")
	requireCondition(t, model.lastSaveOpts.Where, "revision_no = ?", uint64(5))
}

func TestRepo_UpdateWithConstraint_AppliesTenantPredicateWhenOwnerColumnIsExplicit(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected: 1,
	}
	repository := &Repo[*tenantOwnerWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	WithScope[*tenantOwnerWriteEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"})(repository)
	entity := &tenantOwnerWriteEntity{ID: 7, Version: 5, TenantID: "tenant-a", OwnerID: "owner-a"}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		ID:       "7",
		TenantID: "tenant-a",
		Revision: "5",
	}).WriteConstraint()
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	err = repository.UpdateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastSaveOpts.Where, "tenant_id = ?", "tenant-a")
	requireCondition(t, model.lastSaveOpts.Where, "version = ?", uint64(5))
}

// 约束不得被跨租户请求绕过：请求来自 tenant-b，但约束只授权 tenant-a。
//
// 注意：实体自带的 TenantID 不参与判定——写路径会先按请求租户强制标记归属
// （归属不可被请求体篡改），随后约束对齐发现请求租户与授权租户不符而拒绝。
func TestRepo_UpdateWithConstraint_RejectsCrossTenantRequest(t *testing.T) {
	model := &guardedWriteModel{saveAffected: 1}
	repository := &Repo[*tenantWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", version: "version"}}
	entity := &tenantWriteEntity{ID: 7, Version: 5}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		ID:       "7",
		TenantID: "tenant-a",
		Revision: "5",
	}).WriteConstraint()

	err := repository.UpdateWithConstraint(mustTenantCtx(t, "tenant-b"), entity, constraint)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
}

func TestRepo_DeleteWithConstraint_AppliesTenantPredicate(t *testing.T) {
	model := &guardedWriteModel{deleteAffected: 1}
	repository := &Repo[*tenantWriteEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", version: "version"}}
	constraint := scoped.Allow(scoped.Resource{
		Kind:     "document",
		ID:       "7",
		TenantID: "tenant-a",
		Revision: "5",
	}).WriteConstraint()

	err := repository.DeleteWithConstraint(mustTenantCtx(t, "tenant-a"), 7, constraint)
	require.NoError(t, err)
	requireCondition(t, model.lastDeleteOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastDeleteOpts.Where, "tenant_id = ?", "tenant-a")
	requireCondition(t, model.lastDeleteOpts.Where, "version = ?", uint64(5))
}

func TestRepo_UpdateWithConstraint_VersionOmittedAlignsToRevision(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected:  1,
		scopedVisible: true,
		visibleEntity: &scopedRepoEntity{ID: 7, Version: 5, ManagedScopeID: 701},
		rawCount:      1,
	}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	// version 省略：客户端未回传版本，实体 Version 为 0。
	entity := &scopedRepoEntity{ID: 7, ManagedScopeID: 701}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	constraint := scoped.Allow(scoped.Resource{
		Kind:           "document",
		ID:             "7",
		ManagedScopeID: 701,
		Revision:       "5",
	}).WriteConstraint()

	err = repository.UpdateWithConstraint(ctx, entity, constraint)
	require.NoError(t, err)
	// WHERE 按真实 revision 命中。
	requireCondition(t, model.lastSaveOpts.Where, "version = ?", uint64(5))
	// 实体版本被对齐到 revision，避免 SaveWithResult 写回 0 造成版本回退（N→0）。
	require.Equal(t, uint64(5), entity.Version)
}

func TestRepo_UpdateWithConstraint_RejectsManagedScopeMismatch(t *testing.T) {
	model := &guardedWriteModel{saveAffected: 1}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 7, Version: 5, ManagedScopeID: 999}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(999, 1000))
	require.NoError(t, err)
	constraint := scoped.Allow(scoped.Resource{Kind: "document", ID: "7", ManagedScopeID: 701, Revision: "5"}).WriteConstraint()

	err = repository.UpdateWithConstraint(ctx, entity, constraint)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
}

func TestRepo_BatchConstraintTargetsForEntities_FillsManagedScopeWithResourceID(t *testing.T) {
	repository := &Repo[*scopedRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	targets := repository.batchConstraintTargetsForEntities(ctx, []*scopedRepoEntity{{ID: 7, Version: 5}})

	require.Len(t, targets, 1)
	require.Equal(t, "document", targets[0].Kind)
	require.Equal(t, "7", targets[0].ID)
	require.Equal(t, "5", targets[0].Revision)
	require.Equal(t, int64(701), targets[0].ManagedScopeID)
}

func TestRepo_BatchConstraintTargetsForIDs_FillsManagedScopeWithResourceID(t *testing.T) {
	repository := &Repo[*scopedRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)

	targets := repository.batchConstraintTargetsForIDs(ctx, []int64{7}, scoped.WriteConstraint{})

	require.Len(t, targets, 1)
	require.Equal(t, "document", targets[0].Kind)
	require.Equal(t, "7", targets[0].ID)
	require.Equal(t, int64(701), targets[0].ManagedScopeID)
}

func TestRepo_BatchConstraintTargetsForIDs_PrefersConstraintManagedScope(t *testing.T) {
	repository := &Repo[*scopedRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(101))
	require.NoError(t, err)
	constraint := scoped.Allow(scoped.Resource{Kind: "document", ID: "7", ManagedScopeID: 701, Revision: "5"}).WriteConstraint()

	targets := repository.batchConstraintTargetsForIDs(ctx, []int64{7}, constraint)

	require.Len(t, targets, 1)
	require.Equal(t, int64(701), targets[0].ManagedScopeID)
	require.Equal(t, "5", targets[0].Revision)
}

func TestTakeBatchConstraintResourceForIDConsumesDuplicateResourcesInOrder(t *testing.T) {
	resourcesByID := batchConstraintResourcesByID([]scoped.ResourceConstraint{
		{ResourceID: "7", ManagedScopeID: 701, Revision: "5"},
		{ResourceID: "8", ManagedScopeID: 801, Revision: "6"},
		{ResourceID: "7", ManagedScopeID: 702, Revision: "7"},
	})

	first, ok := takeBatchConstraintResourceForID(resourcesByID, "7")
	require.True(t, ok)
	require.Equal(t, int64(701), first.ManagedScopeID)
	require.Equal(t, "5", first.Revision)

	second, ok := takeBatchConstraintResourceForID(resourcesByID, "7")
	require.True(t, ok)
	require.Equal(t, int64(702), second.ManagedScopeID)
	require.Equal(t, "7", second.Revision)

	_, ok = takeBatchConstraintResourceForID(resourcesByID, "7")
	require.False(t, ok)
}

func TestRepo_PurgeWithConstraint_AppliesRevisionAndManagedScopePredicates(t *testing.T) {
	model := &guardedWriteModel{
		deleteAffected: 1,
		scopedVisible:  true,
		visibleEntity:  &scopedRepoEntity{ID: 7, Version: 5, ManagedScopeID: 701},
		rawCount:       1,
	}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)
	constraint := scoped.Allow(scoped.Resource{Kind: "document", ID: "7", ManagedScopeID: 701, Revision: "5"}).WriteConstraint()

	err = repository.purgeWithConstraint(ctx, 7, constraint)

	require.NoError(t, err)
	requireCondition(t, model.lastDeleteOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastDeleteOpts.Where, "managed_scope_id = ?", int64(701))
	requireCondition(t, model.lastDeleteOpts.Where, "version = ?", uint64(5))
}

// mustTenantCtx 构造带租户的 ctx。
//
// 声明了隔离列的 Repo 要求 ctx 携带租户（§4.1 Fail-Closed），
// 显式 WriteConstraint 中的租户须与请求租户一致。
func mustTenantCtx(tb testing.TB, tenantID string) context.Context {
	tb.Helper()
	ctx, err := contextx.WithTenantID(context.Background(), tenantID)
	require.NoError(tb, err)
	return ctx
}

// 极速防探测模式：约束写命中 0 行时不再发探针 SELECT，
// 按构造期配置的固定归因直接返回。
func TestRepo_ConstraintWriteMiss_FastModesSkipBoundaryProbe(t *testing.T) {
	cases := []struct {
		name     string
		mode     WriteMissMode
		wantCode errors.ErrorCode
	}{
		{"fast_not_found", WriteMissFastNotFound, errors.NotFound},
		{"fast_conflict", WriteMissFastConflict, errors.Conflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// scopedVisible 为 true 时探针能查到记录；一旦被调用，
			// 精准诊断模式会依据 version 归因，得到与 fast 模式不同的错误码。
			model := &guardedWriteModel{
				saveAffected:  0,
				scopedVisible: true,
				visibleEntity: &scopedRepoEntity{ID: 7, Version: 5, ManagedScopeID: 701},
				rawCount:      1,
			}
			repository := &Repo[*scopedRepoEntity, int64]{
				model:         model,
				resourceKind:  "document",
				accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
				writeMissMode: tc.mode,
			}
			ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
			require.NoError(t, err)
			constraint := scoped.Allow(scoped.Resource{
				Kind:           "document",
				ID:             "7",
				ManagedScopeID: 701,
				Revision:       "5",
			}).WriteConstraint()

			err = repository.UpdateWithConstraint(ctx, &scopedRepoEntity{ID: 7, Version: 5}, constraint)
			require.Error(t, err)
			require.True(t, errors.Is(err, tc.wantCode))
			require.Equal(t, 0, model.firstCalls, "fast mode must not issue a boundary probe")
		})
	}
}

// 默认（精准诊断）模式仍发探针：记录存在但 version 不符 → Conflict。
func TestRepo_ConstraintWriteMiss_DiagnosticModeProbesBoundary(t *testing.T) {
	model := &guardedWriteModel{
		saveAffected:  0,
		scopedVisible: true,
		visibleEntity: &scopedRepoEntity{ID: 7, Version: 9, ManagedScopeID: 701},
		rawCount:      1,
	}
	repository := &Repo[*scopedRepoEntity, int64]{
		model:         model,
		resourceKind:  "document",
		accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
	}
	ctx, err := scoped.WithDataScope(mustTenantCtx(t, "tenant-a"), scoped.Filtered(701))
	require.NoError(t, err)
	constraint := scoped.Allow(scoped.Resource{
		Kind:           "document",
		ID:             "7",
		ManagedScopeID: 701,
		Revision:       "5",
	}).WriteConstraint()

	err = repository.UpdateWithConstraint(ctx, &scopedRepoEntity{ID: 7, Version: 5}, constraint)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Conflict))
	require.Contains(t, err.Error(), "revision mismatch")
	require.Equal(t, 1, model.firstCalls)
}
