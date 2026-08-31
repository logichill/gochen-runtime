package repo

import (
	"context"
	"database/sql"
	"testing"

	"gochen/testkit/require"

	"gochen/app/query"
	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
)

type scopedRepoEntity struct {
	ID             int64  `json:"id"`
	Version        uint64 `json:"version"`
	ManagedScopeID int64  `json:"managed_scope_id"`
	OwnerID        string `json:"owner_id"`
	Name           string `json:"name"`
}

func (e *scopedRepoEntity) GetID() int64       { return e.ID }
func (e *scopedRepoEntity) GetVersion() uint64 { return e.Version }

type inferredScopedRepoEntity struct {
	ID             int64
	Version        uint64
	ManagedScopeID int64
	OwnerID        string
	Name           string
}

func (e *inferredScopedRepoEntity) GetID() int64       { return e.ID }
func (e *inferredScopedRepoEntity) GetVersion() uint64 { return e.Version }

type inferredTenantRepoEntity struct {
	ID       int64
	TenantID string
	Name     string
}

func (e *inferredTenantRepoEntity) GetID() int64       { return e.ID }
func (e *inferredTenantRepoEntity) GetVersion() uint64 { return 0 }

// 声明隔离列的实体必须实现 ITenantEntity（构造期断言）。
func (e *inferredTenantRepoEntity) GetTenantID() string         { return e.TenantID }
func (e *inferredTenantRepoEntity) SetTenantID(tenantID string) { e.TenantID = tenantID }

type inferredTenantOwnerRepoEntity struct {
	ID             int64
	Version        uint64
	TenantID       string
	ManagedScopeID int64
	OwnerID        string
	Name           string
}

func (e *inferredTenantOwnerRepoEntity) GetID() int64       { return e.ID }
func (e *inferredTenantOwnerRepoEntity) GetVersion() uint64 { return e.Version }

type recursiveScopedEntity struct {
	ID             int64                  `json:"id"`
	ManagedScopeID int64                  `json:"managed_scope_id"`
	Parent         *recursiveScopedEntity `json:"parent,omitempty"`
}

func (e *recursiveScopedEntity) GetID() int64       { return e.ID }
func (e *recursiveScopedEntity) GetVersion() uint64 { return 0 }

type customTaggedScopedEntity struct {
	ID           int64  `gorm:"column:id"`
	ManagedScope int64  `gorm:"column:scope_ref"`
	Tenant       string `gorm:"column:tenant_ref"`
	Owner        string `gorm:"column:owner_ref"`
	Revision     uint64 `gorm:"column:revision_no"`
}

func (e *customTaggedScopedEntity) GetID() int64       { return e.ID }
func (e *customTaggedScopedEntity) GetVersion() uint64 { return e.Revision }

type noAuthzFieldEntity struct {
	ID   int64  `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

func (e *noAuthzFieldEntity) GetID() int64       { return e.ID }
func (e *noAuthzFieldEntity) GetVersion() uint64 { return 0 }

type unsafeTaggedScopedEntity struct {
	ID             int64  `gorm:"column:id"`
	ManagedScopeID int64  `gorm:"column:scope-ref"`
	OwnerID        string `gorm:"column:owner_ref"`
	Version        uint64 `gorm:"column:version"`
}

func (e *unsafeTaggedScopedEntity) GetID() int64       { return e.ID }
func (e *unsafeTaggedScopedEntity) GetVersion() uint64 { return e.Version }

type capturingDataScopeModel struct {
	lastFindOpts  orm.QueryOptions
	lastFirstOpts orm.QueryOptions
	lastSaveOpts  orm.QueryOptions
	created       any
	firstCalls    int
}

func (m *capturingDataScopeModel) Meta() *orm.ModelMeta {
	return &orm.ModelMeta{Table: "scoped_entities"}
}
func (m *capturingDataScopeModel) Capabilities() orm.Capabilities { return nil }
func (m *capturingDataScopeModel) Dialect() repoTestDialect       { return unknownRepoTestDialect() }
func (m *capturingDataScopeModel) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	m.firstCalls++
	m.lastFirstOpts = orm.CollectQueryOptions(opts...)
	return nil
}
func (m *capturingDataScopeModel) Find(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	m.lastFindOpts = orm.CollectQueryOptions(opts...)
	return nil
}
func (m *capturingDataScopeModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return 0, nil
}
func (m *capturingDataScopeModel) Create(ctx context.Context, entities ...any) error {
	_ = ctx
	if len(entities) > 0 {
		m.created = entities[0]
	}
	return nil
}
func (m *capturingDataScopeModel) Save(ctx context.Context, entity any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = entity
	m.lastSaveOpts = orm.CollectQueryOptions(opts...)
	return nil
}

// SaveWithResult 让该 fake 具备 result 能力：写路径收敛后，
// 声明了范围列的仓储走受控约束路径，而约束更新要求 orm result 支持。
func (m *capturingDataScopeModel) SaveWithResult(ctx context.Context, entity any, opts ...orm.QueryOption) (sql.Result, error) {
	if err := m.Save(ctx, entity, opts...); err != nil {
		return nil, err
	}
	return fakeResult(1), nil
}

func (m *capturingDataScopeModel) UpdateValuesWithResult(context.Context, map[string]any, ...orm.QueryOption) (sql.Result, error) {
	panic("not used")
}

func (m *capturingDataScopeModel) DeleteWithResult(context.Context, ...orm.QueryOption) (sql.Result, error) {
	panic("not used")
}

func (m *capturingDataScopeModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	panic("not used")
}
func (m *capturingDataScopeModel) Delete(context.Context, ...orm.QueryOption) error {
	panic("not used")
}
func (m *capturingDataScopeModel) Association(any, string) orm.IAssociation { return nil }

type staticRepoOrm struct {
	model orm.IModel
}

func (s *staticRepoOrm) Capabilities() orm.Capabilities { return nil }
func (s *staticRepoOrm) WithContext(context.Context) orm.IOrm {
	return s
}
func (s *staticRepoOrm) Model(*orm.ModelMeta) (orm.IModel, error) {
	return s.model, nil
}
func (s *staticRepoOrm) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *staticRepoOrm) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "not implemented")
}
func (s *staticRepoOrm) Database() db.IDatabase { return nil }

type resourceBoundaryModel struct {
	first         *inferredTenantOwnerRepoEntity
	find          []*inferredTenantOwnerRepoEntity
	lastFirstOpts orm.QueryOptions
	lastFindOpts  orm.QueryOptions
}

func (m *resourceBoundaryModel) Meta() *orm.ModelMeta {
	return &orm.ModelMeta{
		Table: "scoped_entities",
		Fields: []orm.FieldMeta{
			{Name: "ID", Column: "id"},
			{Name: "TenantID", Column: "tenant_id"},
			{Name: "ManagedScopeID", Column: "managed_scope_id"},
			{Name: "OwnerID", Column: "owner_id"},
			{Name: "Version", Column: "version"},
		},
	}
}
func (m *resourceBoundaryModel) Capabilities() orm.Capabilities { return nil }
func (m *resourceBoundaryModel) Dialect() repoTestDialect       { return unknownRepoTestDialect() }
func (m *resourceBoundaryModel) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	m.lastFirstOpts = orm.CollectQueryOptions(opts...)
	if m.first == nil {
		return errors.NewCode(errors.NotFound, "not found")
	}
	entity, ok := dest.(*inferredTenantOwnerRepoEntity)
	if !ok {
		return errors.NewCode(errors.InvalidInput, "unexpected first destination")
	}
	*entity = *m.first
	return nil
}
func (m *resourceBoundaryModel) Find(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	m.lastFindOpts = orm.CollectQueryOptions(opts...)
	entities, ok := dest.(*[]*inferredTenantOwnerRepoEntity)
	if !ok {
		return errors.NewCode(errors.InvalidInput, "unexpected find destination")
	}
	*entities = append((*entities)[:0], m.find...)
	return nil
}
func (m *resourceBoundaryModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return 0, nil
}
func (m *resourceBoundaryModel) Create(context.Context, ...any) error { return nil }
func (m *resourceBoundaryModel) Save(context.Context, any, ...orm.QueryOption) error {
	return nil
}
func (m *resourceBoundaryModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	return nil
}
func (m *resourceBoundaryModel) Delete(context.Context, ...orm.QueryOption) error { return nil }
func (m *resourceBoundaryModel) Association(any, string) orm.IAssociation         { return nil }

// 未显式声明安全列时，即便实体带有 managed_scope_id / tenant_id 字段
// 也**不得**激活任何安全过滤（取代旧的隐式字段名推断）。
func TestResolveDataScopeSchema_NoImplicitActivation(t *testing.T) {
	schema := resolveDataScopeSchema[*inferredScopedRepoEntity](accessColumns{})

	require.Equal(t, "", schema.managedScope.column)
	require.Equal(t, "", schema.ownerID.column)
	require.Equal(t, "", schema.version.column)
	require.Equal(t, "", schema.isolation.column)
}

// 显式声明的列必须被解析出来，并绑定到实体上对应的字段（写路径回填归属所需）。
func TestResolveDataScopeSchema_ResolvesDeclaredColumns(t *testing.T) {
	schema := resolveDataScopeSchema[*inferredTenantOwnerRepoEntity](accessColumns{
		isolation:    "tenant_id",
		ownerID:      "owner_id",
		managedScope: "managed_scope_id",
		version:      "version",
	})

	require.Equal(t, "tenant_id", schema.isolation.column)
	require.Equal(t, "owner_id", schema.ownerID.column)
	require.Equal(t, "managed_scope_id", schema.managedScope.column)
	require.Equal(t, "version", schema.version.column)
	require.True(t, schema.isolation.hasField())
	require.True(t, schema.managedScope.hasField())
}

func TestRepo_Query_AppliesManagedScopeFilters(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(101, 202))
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "managed_scope_id IN ?", []any{int64(101), int64(202)})
}

func TestRepo_Query_AppliesManagedScopeFiltersFromInferredIDField(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredScopedRepoEntity, int64]{model: model, accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(101, 202))
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "managed_scope_id IN ?", []any{int64(101), int64(202)})
}

func TestRepo_Query_AppliesTenantFilterFromInferredTenantIDField(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredTenantRepoEntity, int64]{model: model, accessColumns: accessColumns{isolation: "tenant_id"}}
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

func TestRepo_Query_AppliesTenantFilterWhenOwnerColumnIsExplicit(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{model: model, accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	WithScope[*inferredTenantOwnerRepoEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"})(repository)
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	ctx, err = scoped.WithDataScope(ctx, scoped.Global())
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

func TestRepo_Query_AppliesExplicitTenantColumn(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*customTaggedScopedEntity, int64]{
		model: model,
	}
	WithIsolation[*customTaggedScopedEntity, int64](IsolationCols{Column: "tenant_ref"})(repository)
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_ref = ?", "tenant-a")
}

func TestRepo_RequireScopeAcceptsExplicitTenantColumn(t *testing.T) {
	repository := &Repo[*customTaggedScopedEntity, int64]{
		resourceKind: "document",
		requireScope: true,
	}
	WithIsolation[*customTaggedScopedEntity, int64](IsolationCols{Column: "tenant_ref"})(repository)

	require.NoError(t, repository.validateScopeEnforcement())
}

func TestRepo_ResourceFromEntity_PopulatesTenantIDFromInferredTenantField(t *testing.T) {
	repository := &Repo[*inferredTenantRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id"}}

	resource := repository.resourceFromEntity(&inferredTenantRepoEntity{
		ID:       7,
		TenantID: " tenant-a ",
	})

	require.Equal(t, "document", resource.Kind)
	require.Equal(t, "7", resource.ID)
	require.Equal(t, "tenant-a", resource.TenantID)
	require.Empty(t, resource.OwnerID)
}

func TestRepo_ResourceFromEntity_PopulatesTenantAndOwnerIndependently(t *testing.T) {
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	WithScope[*inferredTenantOwnerRepoEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"})(repository)

	resource := repository.resourceFromEntity(&inferredTenantOwnerRepoEntity{
		ID:             7,
		Version:        5,
		TenantID:       " tenant-a ",
		ManagedScopeID: 701,
		OwnerID:        " owner-a ",
	})

	require.Equal(t, "document", resource.Kind)
	require.Equal(t, "7", resource.ID)
	require.Equal(t, "tenant-a", resource.TenantID)
	require.Equal(t, "owner-a", resource.OwnerID)
	require.Equal(t, int64(701), resource.ManagedScopeID)
	require.Equal(t, "5", resource.Revision)
}

func TestRepo_OrderedResourceBoundariesRestoresInputOrder(t *testing.T) {
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	WithScope[*inferredTenantOwnerRepoEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"})(repository)

	resources, err := repository.orderedResourceBoundaries([]int64{2, 1}, []*inferredTenantOwnerRepoEntity{
		{ID: 1, TenantID: "tenant-a", ManagedScopeID: 101, OwnerID: "owner-a", Version: 11},
		{ID: 2, TenantID: "tenant-b", ManagedScopeID: 202, OwnerID: "owner-b", Version: 22},
	})

	require.NoError(t, err)
	require.Len(t, resources, 2)
	require.Equal(t, "2", resources[0].ID)
	require.Equal(t, int64(202), resources[0].ManagedScopeID)
	require.Equal(t, "22", resources[0].Revision)
	require.Equal(t, "1", resources[1].ID)
	require.Equal(t, int64(101), resources[1].ManagedScopeID)
	require.Equal(t, "11", resources[1].Revision)
}

func TestRepo_OrderedResourceBoundariesRejectsMissingID(t *testing.T) {
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{resourceKind: "document", accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}

	_, err := repository.orderedResourceBoundaries([]int64{1, 2}, []*inferredTenantOwnerRepoEntity{
		{ID: 1},
	})

	require.Truef(t, errors.Is(err, errors.NotFound), "expected NotFound, got %v", err)
}

func TestRepo_ResolveResourcesByID_ReturnsBoundariesInInputOrder(t *testing.T) {
	model := &resourceBoundaryModel{find: []*inferredTenantOwnerRepoEntity{
		{ID: 1, TenantID: "tenant-a", ManagedScopeID: 101, OwnerID: "owner-a", Version: 11},
		{ID: 2, TenantID: "tenant-b", ManagedScopeID: 202, OwnerID: "owner-b", Version: 22},
	}}
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{
		model:          model,
		resourceKind:   "document",
		softDelete:     true,
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at"},
		accessColumns:  accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
	}

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	resources, err := repository.ResolveResourcesByID(ctx, []int64{2, 1})

	require.NoError(t, err)
	require.Len(t, resources, 2)
	require.Equal(t, "2", resources[0].ID)
	require.Equal(t, int64(202), resources[0].ManagedScopeID)
	require.Equal(t, "22", resources[0].Revision)
	require.Equal(t, "1", resources[1].ID)
	require.Equal(t, int64(101), resources[1].ManagedScopeID)
	require.Equal(t, "11", resources[1].Revision)
	requireCondition(t, model.lastFindOpts.Where, "id IN ?", []int64{2, 1})
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
	requireCondition(t, model.lastFindOpts.Where, "deleted_at IS NULL", nil)
	require.ElementsMatch(t, []string{"id", "managed_scope_id", "tenant_id", "owner_id", "version"}, model.lastFindOpts.Select)
}

func TestRepo_ResolveResourceByIDIncludingDeleted_OmitsSoftDeleteFilter(t *testing.T) {
	model := &resourceBoundaryModel{first: &inferredTenantOwnerRepoEntity{
		ID:             7,
		TenantID:       "tenant-a",
		ManagedScopeID: 701,
		OwnerID:        "owner-a",
		Version:        5,
	}}
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{
		model:          model,
		resourceKind:   "document",
		softDelete:     true,
		softDeleteCols: softDeleteColumns{DeletedAt: "deleted_at"},
		accessColumns:  accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
	}

	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	resource, err := repository.ResolveResourceByIDIncludingDeleted(ctx, 7)

	require.NoError(t, err)
	require.Equal(t, scoped.Resource{
		Kind:           "document",
		ID:             "7",
		ManagedScopeID: 701,
		TenantID:       "tenant-a",
		OwnerID:        "owner-a",
		Revision:       "5",
	}, resource)
	requireCondition(t, model.lastFirstOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastFirstOpts.Where, "tenant_id = ?", "tenant-a")
	for _, condition := range model.lastFirstOpts.Where {
		require.NotEqual(t, "deleted_at IS NULL", condition.Expr)
	}
}

func TestRepo_Query_AppliesAppAccessManagedScopeFilters(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(303, 404))
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "managed_scope_id IN ?", []any{int64(303), int64(404)})
}

func TestRepo_Query_UsesExplicitAccessColumns(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*customTaggedScopedEntity, int64]{model: model}
	WithScope[*customTaggedScopedEntity, int64](ScopeCols{ManagedScopeID: "scope_ref", OwnerID: "owner_ref", Revision: "revision_no"})(repository)
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(505, 606))
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "scope_ref IN ?", []any{int64(505), int64(606)})
}

func TestNewRepo_FailsFastOnUnsafeAccessColumns(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	_, err := NewRepo[*customTaggedScopedEntity, int64](
		ormEngine,
		"scoped_entities",
		WithScope[*customTaggedScopedEntity, int64](ScopeCols{ManagedScopeID: "scope-ref", OwnerID: "owner_ref", Revision: "revision_no"}),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestNewRepo_RejectsNilORM(t *testing.T) {
	_, err := NewRepo[*scopedRepoEntity, int64](nil, "scoped_entities")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestNewRepo_FailsFastOnUnsafeTagDerivedColumns(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	_, err := NewRepo[*unsafeTaggedScopedEntity, int64](ormEngine, "scoped_entities")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestRepo_Create_InjectsSingleManagedScopeIntoEntity(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 1, Name: "demo"}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(909))
	require.NoError(t, err)
	ctx = withScopedWriteConstraint(ctx, "document", "1", 909)

	err = repository.Create(ctx, entity)
	require.NoError(t, err)
	require.Equal(t, int64(909), entity.ManagedScopeID)
	require.Same(t, entity, model.created)
}

func TestRepo_Create_MultiScopeRequiresExplicitManagedScope(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 1, Name: "demo"}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(1001, 1002))
	require.NoError(t, err)
	ctx = withScopedWriteConstraint(ctx, "document", "1", 0)

	err = repository.Create(ctx, entity)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestRepo_Update_AppliesManagedScopeFiltersAndNormalizesEntity(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	entity := &scopedRepoEntity{ID: 7, ManagedScopeID: 888}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(707))
	require.NoError(t, err)
	ctx = withScopedWriteConstraintRevision(ctx, "document", "7", 707, "0")

	err = repository.Update(ctx, entity)
	require.NoError(t, err)
	require.Equal(t, int64(707), entity.ManagedScopeID)
	requireCondition(t, model.lastSaveOpts.Where, "managed_scope_id = ?", int64(707))
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
}

// 声明了范围列却拿不到数据范围时必须拒绝（Forbidden），
// 且不得触达数据库——fail-closed 不是"输入错误"，是"拒绝访问"。
func TestRepo_Get_ScopedEntityRequiresDataScope(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}

	_, err := repository.Get(context.Background(), 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Equal(t, 0, model.firstCalls)
}

func TestRepo_Query_IgnoresRecursiveStructCycles(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*recursiveScopedEntity, int64]{model: model, accessColumns: accessColumns{managedScope: "managed_scope_id"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Filtered(1212))
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "managed_scope_id = ?", int64(1212))
}

func TestNewRepo_RequireScope_FailsFastWhenNoBoundaryColumn(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	_, err := NewRepo[*noAuthzFieldEntity, int64](
		ormEngine,
		"scoped_entities",
		WithRequireScope[*noAuthzFieldEntity, int64](),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestNewRepo_RequireScope_AllowsTenantBoundaryColumn(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	_, err := NewRepo[*inferredTenantRepoEntity, int64](
		ormEngine,
		"tenant_entities",
		WithIsolation[*inferredTenantRepoEntity, int64](IsolationCols{Column: "tenant_id"}),
		WithRequireScope[*inferredTenantRepoEntity, int64](),
	)
	require.NoError(t, err)
}

func TestRepo_RequireScope_FailsClosedWhenTenantMissing(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredTenantRepoEntity, int64]{model: model, requireScope: true, accessColumns: accessColumns{isolation: "tenant_id"}}

	_, err := repository.Query(context.Background(), query.QueryOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
}

func TestRepo_RequireScope_AllowsBoundedTenantQuery(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredTenantRepoEntity, int64]{model: model, requireScope: true, accessColumns: accessColumns{isolation: "tenant_id"}}
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

func TestRepo_RequireScope_RejectsGlobalManagedScopeWithoutOptIn(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", requireScope: true, accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Global())
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Empty(t, model.lastFindOpts.Where)
}

func TestRepo_RequireScope_AllowsGlobalManagedScopeWithOptIn(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{
		model:         model,
		resourceKind:  "document",
		requireScope:  true,
		accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"},
	}
	WithAllowGlobalScope[*scopedRepoEntity, int64]()(repository)
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Global())
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	require.Empty(t, model.lastFindOpts.Where)
}

func TestRepo_RequireScope_GlobalScopeUsesTenantBoundaryWhenPresent(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*inferredTenantOwnerRepoEntity, int64]{model: model, resourceKind: "document", requireScope: true, accessColumns: accessColumns{isolation: "tenant_id", managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-a")
	require.NoError(t, err)
	ctx, err = scoped.WithDataScope(ctx, scoped.Global())
	require.NoError(t, err)

	_, err = repository.Query(ctx, query.QueryOptions{})
	require.NoError(t, err)
	requireCondition(t, model.lastFindOpts.Where, "tenant_id = ?", "tenant-a")
}

func TestRepo_RequireScope_CreateRejectsGlobalManagedScopeWithoutOptIn(t *testing.T) {
	model := &capturingDataScopeModel{}
	repository := &Repo[*scopedRepoEntity, int64]{model: model, resourceKind: "document", requireScope: true, accessColumns: accessColumns{managedScope: "managed_scope_id", ownerID: "owner_id", version: "version"}}
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Global())
	require.NoError(t, err)

	err = repository.Create(ctx, &scopedRepoEntity{ID: 1, Name: "demo"})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)
}

func TestNewRepo_RequireScope_FailsWhenExplicitTenantColumnHasNoEntityField(t *testing.T) {
	model := &capturingDataScopeModel{}
	ormEngine := &staticRepoOrm{model: model}

	_, err := NewRepo[*noAuthzFieldEntity, int64](
		ormEngine,
		"tenant_entities",
		WithIsolation[*noAuthzFieldEntity, int64](IsolationCols{Column: "tenant_id"}),
		WithRequireScope[*noAuthzFieldEntity, int64](),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func requireCondition(t *testing.T, conditions []orm.Condition, expr string, want any) {
	t.Helper()
	for _, condition := range conditions {
		if condition.Expr != expr {
			continue
		}
		if want == nil {
			require.Len(t, condition.Args, 0)
			return
		}
		require.Len(t, condition.Args, 1)
		require.Equal(t, want, condition.Args[0])
		return
	}
	t.Fatalf("condition %quer not found in %#v", expr, conditions)
}

// withScopedWriteConstraint 为声明了范围列的仓储绑定受控通道写约束。
//
// 这类仓储的写路径一律要求约束，否则 fail-closed（constraint_channel.go）。
func withScopedWriteConstraint(ctx context.Context, entityType, resourceID string, managedScopeID int64) context.Context {
	return withScopedWriteConstraintRevision(ctx, entityType, resourceID, managedScopeID, "")
}

// withScopedWriteConstraintRevision 额外指定乐观锁基线版本（更新路径必填）。
func withScopedWriteConstraintRevision(ctx context.Context, entityType, resourceID string, managedScopeID int64, revision string) context.Context {
	provider := scoped.SingleEntityConstraint(entityType, scoped.WriteConstraint{
		Resources: []scoped.ResourceConstraint{{
			Kind:           entityType,
			ResourceID:     resourceID,
			ManagedScopeID: managedScopeID,
			Revision:       revision,
		}},
	})
	return scoped.WithConstraint(ctx, provider)
}
