package repo

import (
	"context"
	"database/sql"
	"testing"

	"gochen/app/crud"
	secscoped "gochen/app/security/scoped"
	"gochen/auth/action"
	authscoped "gochen/auth/scoped"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
	"gochen/testkit/require"
)

// L3 端到端回归：app/security/scoped 装饰器 → 基础 Application → 本包 SQL 仓储。
//
// 为什么必须有这一层用例：此前 L3 的两侧各自测得很全，但**没有任何用例横跨两侧**——
// gochen 侧一律用 testkit 内存仓储（不解析 DataScope），本包侧则在测试里手工
// 预先绑好范围。结果是装饰器只投放写约束却不绑定数据范围这个缺陷，在两边全绿的
// 情况下潜伏了下来：真实装配下每一次 L3 写都会被仓储以
// "data scope resolver is required" 拒绝。
//
// 下列用例一律从**装饰器入口**发起调用，ctx 只有 Background——即真实调用方的形态。

// --- 端到端专用 fake model ---

// l3Model 是一个可控的 orm.IModel：能回放 First 结果、记录写入条件与结果行数。
type l3Model struct {
	first *l3Entity

	created      any
	lastSaveOpts orm.QueryOptions
	saveCalls    int
	savedRows    int64
}

func newL3Model(first *l3Entity) *l3Model { return &l3Model{first: first, savedRows: 1} }

func (m *l3Model) Meta() *orm.ModelMeta {
	return &orm.ModelMeta{
		Table: "documents",
		Fields: []orm.FieldMeta{
			{Name: "ID", Column: "id"},
			{Name: "ManagedScopeID", Column: "managed_scope_id"},
			{Name: "Version", Column: "version"},
		},
	}
}
func (m *l3Model) Capabilities() orm.Capabilities { return nil }
func (m *l3Model) Dialect() repoTestDialect       { return unknownRepoTestDialect() }

func (m *l3Model) First(_ context.Context, dest any, _ ...orm.QueryOption) error {
	if m.first == nil {
		return errors.NewCode(errors.NotFound, "not found")
	}
	entity, ok := dest.(*l3Entity)
	if !ok {
		return errors.NewCode(errors.InvalidInput, "unexpected destination")
	}
	*entity = *m.first
	return nil
}

func (m *l3Model) Find(context.Context, any, ...orm.QueryOption) error { return nil }
func (m *l3Model) Count(context.Context, ...orm.QueryOption) (int64, error) {
	return 0, nil
}

func (m *l3Model) Create(_ context.Context, entities ...any) error {
	if len(entities) > 0 {
		m.created = entities[0]
	}
	return nil
}

func (m *l3Model) Save(_ context.Context, _ any, opts ...orm.QueryOption) error {
	m.saveCalls++
	m.lastSaveOpts = orm.CollectQueryOptions(opts...)
	return nil
}

func (m *l3Model) SaveWithResult(ctx context.Context, entity any, opts ...orm.QueryOption) (sql.Result, error) {
	if err := m.Save(ctx, entity, opts...); err != nil {
		return nil, err
	}
	return fakeResult(m.savedRows), nil
}

func (m *l3Model) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	return nil
}
func (m *l3Model) UpdateValuesWithResult(context.Context, map[string]any, ...orm.QueryOption) (sql.Result, error) {
	return fakeResult(1), nil
}
func (m *l3Model) Delete(context.Context, ...orm.QueryOption) error { return nil }
func (m *l3Model) DeleteWithResult(context.Context, ...orm.QueryOption) (sql.Result, error) {
	return fakeResult(1), nil
}
func (m *l3Model) Association(any, string) orm.IAssociation { return nil }

// l3Orm / l3Session 提供 Application 写流程所需的最小事务能力，
// 且始终把同一个 l3Model 交回去，使事务内外看到的是同一份记录。
type l3Orm struct{ model *l3Model }

type l3Session struct {
	model      *l3Model
	dispatcher contextx.IAfterCommitDispatcher
}

func (o *l3Orm) Capabilities() orm.Capabilities           { return nil }
func (o *l3Orm) WithContext(context.Context) orm.IOrm     { return o }
func (o *l3Orm) Model(*orm.ModelMeta) (orm.IModel, error) { return o.model, nil }
func (o *l3Orm) Begin(context.Context) (orm.IOrmSession, error) {
	return &l3Session{model: o.model}, nil
}
func (o *l3Orm) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return &l3Session{model: o.model}, nil
}
func (o *l3Orm) Database() db.IDatabase { return nil }

func (s *l3Session) Capabilities() orm.Capabilities           { return nil }
func (s *l3Session) WithContext(context.Context) orm.IOrm     { return s }
func (s *l3Session) Model(*orm.ModelMeta) (orm.IModel, error) { return s.model, nil }
func (s *l3Session) Begin(context.Context) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "nested begin")
}
func (s *l3Session) BeginTx(context.Context, *sql.TxOptions) (orm.IOrmSession, error) {
	return nil, errors.NewCode(errors.Unsupported, "nested begin")
}
func (s *l3Session) Database() db.IDatabase { return nil }
func (s *l3Session) Rollback() error        { return nil }
func (s *l3Session) Commit() error {
	if s.dispatcher != nil {
		if err := s.dispatcher.RunAfterCommit(); err != nil {
			return contextx.WrapAfterCommitError(err)
		}
	}
	return nil
}
func (s *l3Session) AfterCommitDispatcher() contextx.IAfterCommitDispatcher {
	if s.dispatcher == nil {
		s.dispatcher = contextx.NewAfterCommitDispatcher()
	}
	return s.dispatcher
}

type l3Entity struct {
	ID             int64  `json:"id"`
	ManagedScopeID int64  `json:"managed_scope_id"`
	Version        uint64 `json:"version"`
	Name           string `json:"name"`
}

func (e *l3Entity) GetID() int64       { return e.ID }
func (e *l3Entity) GetVersion() uint64 { return e.Version }
func (e *l3Entity) Validate() error    { return nil }

// --- 装配 ---

// newL3Repo 构造一个「显式声明了范围列 + revision 列」的仓储，
// 且**不注入** WithDataScopeResolver——范围必须由 L3 装饰器负责绑定。
func newL3Repo(t *testing.T, model *l3Model) *Repo[*l3Entity, int64] {
	t.Helper()
	repository, err := NewRepo[*l3Entity, int64](&l3Orm{model: model}, "documents",
		WithResourceKind[*l3Entity, int64]("document"),
		WithScope[*l3Entity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", Revision: "version"}))
	require.NoError(t, err)
	return repository
}

// l3Authorizer 是一个如实回显授权资源的 PDP：不为了让用例通过而伪造资源。
func l3Authorizer(t *testing.T, allowedScope int64) authscoped.IAuthorizer {
	t.Helper()
	evaluator := authscoped.EvaluatorFunc(
		func(_ context.Context, _ string, resources []authscoped.Resource) (authscoped.Decision, error) {
			if len(resources) == 0 {
				// 列表类判定：只表态动作允许，不回传资源清单。
				return authscoped.Decision{Effect: authscoped.EffectAllow}, nil
			}
			for _, resource := range resources {
				if resource.ManagedScopeID != 0 && resource.ManagedScopeID != allowedScope {
					return authscoped.Deny(authscoped.ReasonOutOfScope), nil
				}
			}
			return authscoped.Allow(resources...), nil
		})
	resolver := authscoped.ResourceResolverFunc(func(target any) (authscoped.Resource, bool) {
		entity, ok := target.(*l3Entity)
		if !ok {
			return authscoped.Resource{}, false
		}
		return authscoped.Resource{
			Kind:           "document",
			ID:             formatResourceID(entity.ID),
			ManagedScopeID: entity.ManagedScopeID,
		}, true
	})
	authorizer, err := authscoped.NewAuthorizer(resolver, evaluator)
	require.NoError(t, err)
	return authorizer
}

func newL3App(t *testing.T, model *l3Model, allowedScope int64) crud.IApplication[*l3Entity, int64] {
	t.Helper()
	plain, err := crud.NewApplication[*l3Entity, int64](newL3Repo(t, model), nil)
	require.NoError(t, err)
	secured, err := secscoped.New[*l3Entity, int64](plain, l3Authorizer(t, allowedScope), secscoped.Config{
		EntityType: "document",
		Policy: action.OperationPolicy{
			Create: "document:api:create",
			Read:   "document:api:read",
			Update: "document:api:update",
			Delete: "document:api:delete",
			List:   "document:api:list",
		},
		ScopeResolver: authscoped.DataScopeResolverFunc(
			func(context.Context) (authscoped.DataScope, error) {
				return authscoped.Filtered(allowedScope), nil
			}),
	})
	require.NoError(t, err)
	return secured
}

// --- 用例 ---

// 经 L3 装饰器发起的创建必须真的落库：装饰器要同时投放写约束与数据范围，
// 缺任何一个都会被仓储 fail-closed 拒绝。
func TestL3EndToEnd_CreateReachesRepository(t *testing.T) {
	model := newL3Model(nil)
	secured := newL3App(t, model, 101)

	err := secured.Create(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101, Name: "spec"})

	require.NoError(t, err)
	require.NotNil(t, model.created)
	created, ok := model.created.(*l3Entity)
	require.True(t, ok)
	// 范围戳由仓储依据 ctx 中的数据范围写入。
	require.Equal(t, int64(101), created.ManagedScopeID)
}

// 更新的授权输入必须取自仓储里的真实行，而不是调用方提交的实体。
// 这里客户端谎称自己属于 101，真实行却在 999——PDP 必须看到 999 并拒绝。
func TestL3EndToEnd_UpdateAuthorizesAgainstStoredBoundary(t *testing.T) {
	model := newL3Model(&l3Entity{ID: 7, ManagedScopeID: 999, Version: 3})
	secured := newL3App(t, model, 101)

	err := secured.Update(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101, Version: 3})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Equal(t, 0, model.saveCalls)
}

// 目标行确实在授权范围内时，更新照常执行，且 WHERE 带上范围与 revision。
func TestL3EndToEnd_UpdatePushesScopeAndRevision(t *testing.T) {
	model := newL3Model(&l3Entity{ID: 7, ManagedScopeID: 101, Version: 3})
	secured := newL3App(t, model, 101)

	err := secured.Update(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101, Version: 3})

	require.NoError(t, err)
	require.Equal(t, 1, model.saveCalls)
	requireCondition(t, model.lastSaveOpts.Where, "id = ?", int64(7))
	requireCondition(t, model.lastSaveOpts.Where, "managed_scope_id = ?", int64(101))
	requireCondition(t, model.lastSaveOpts.Where, "version = ?", uint64(3))
}

// 调用方拿着过期版本提交：这是并发冲突，不是权限问题——
// 报 Forbidden 会让客户端以为自己无权修改，从而不去重试。
func TestL3EndToEnd_StaleVersionReportsConcurrency(t *testing.T) {
	model := newL3Model(&l3Entity{ID: 7, ManagedScopeID: 101, Version: 5})
	secured := newL3App(t, model, 101)

	err := secured.Update(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101, Version: 3})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Concurrency))
	require.Equal(t, 0, model.saveCalls)
}

// 主体范围为 DenyAll 时，即便 PDP 放行也必须整体拒绝（范围求交，不是二选一）。
func TestL3EndToEnd_SubjectScopeIntersectsDecision(t *testing.T) {
	model := newL3Model(nil)
	plain, err := crud.NewApplication[*l3Entity, int64](newL3Repo(t, model), nil)
	require.NoError(t, err)
	secured, err := secscoped.New[*l3Entity, int64](plain, l3Authorizer(t, 101), secscoped.Config{
		EntityType: "document",
		Policy:     action.OperationPolicy{Create: "document:api:create", List: "document:api:list"},
		ScopeResolver: authscoped.DataScopeResolverFunc(
			func(context.Context) (authscoped.DataScope, error) {
				return authscoped.DenyAll(), nil
			}),
	})
	require.NoError(t, err)

	err = secured.Create(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)
}

// 绕过 L3 PEP 直接用裸 Application 写：仓储取不到约束，fail-closed。
func TestL3EndToEnd_BypassingDecoratorIsRejected(t *testing.T) {
	model := newL3Model(nil)
	plain, err := crud.NewApplication[*l3Entity, int64](newL3Repo(t, model), nil)
	require.NoError(t, err)

	err = plain.Create(context.Background(), &l3Entity{ID: 7, ManagedScopeID: 101})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Nil(t, model.created)
}

// --- 构造期 fail-fast ---

type l3NoVersionEntity struct {
	ID             int64 `json:"id"`
	ManagedScopeID int64 `json:"managed_scope_id"`
}

func (e *l3NoVersionEntity) GetID() int64       { return e.ID }
func (e *l3NoVersionEntity) GetVersion() uint64 { return 0 }
func (e *l3NoVersionEntity) Validate() error    { return nil }

// 实体缺少 revision 列：约束写做不了乐观锁求交，构造期就该拒绝。
func TestNewRepo_ScopeWithoutRevisionColumnFailsFast(t *testing.T) {
	_, err := NewRepo[*l3NoVersionEntity, int64](&l3Orm{model: newL3Model(nil)}, "documents",
		WithScope[*l3NoVersionEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id"}))

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 声明的安全列在实体上没有对应字段：过滤会静默失效，构造期就该拒绝。
func TestNewRepo_UnmappedSecurityColumnFailsFast(t *testing.T) {
	_, err := NewRepo[*l3Entity, int64](&l3Orm{model: newL3Model(nil)}, "documents",
		WithScope[*l3Entity, int64](ScopeCols{ManagedScopeID: "no_such_column", Revision: "version"}))

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 成对声明范围列与 revision 列时构造成功，并如实报告已声明范围。
func TestNewRepo_ScopeWithRevisionSucceeds(t *testing.T) {
	repository, err := NewRepo[*l3Entity, int64](&l3Orm{model: newL3Model(nil)}, "documents",
		WithScope[*l3Entity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", Revision: "version"}))

	require.NoError(t, err)
	require.True(t, repository.HasScopeDeclaration())
}
