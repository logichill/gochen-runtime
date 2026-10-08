package security_test

import (
	"context"
	"testing"

	"gochen-runtime/security"

	"gochen/app/crud"
	"gochen/auth/action"
	"gochen/auth/scoped"
	domcrud "gochen/domain/crud"
	"gochen/errors"
	"gochen/testkit"
	"gochen/testkit/require"
)

type order struct {
	domcrud.Entity[int64]
	Name string
}

func (o *order) Validate() error { return nil }

// scopeAwareRepo 模拟已声明范围列的仓储。
type scopeAwareRepo struct {
	*testkit.MemoryRepository[*order, int64]
	declared bool
	sawWrite bool
}

func (r *scopeAwareRepo) HasScopeDeclaration() bool { return r.declared }

// ResourceKind 与 Profile.EntityType 保持一致；两者不一致时约束恒匹配不到，
// 装配层会在构造期拦下。
func (r *scopeAwareRepo) ResourceKind() string { return "order" }

func (r *scopeAwareRepo) Create(ctx context.Context, e *order) error {
	if _, ok := scoped.ConstraintFrom(ctx, "order"); !ok {
		return errors.NewCode(errors.Forbidden, "write constraint is required but missing")
	}
	r.sawWrite = true
	return r.MemoryRepository.Create(ctx, e)
}

func newRepo(declared bool) *scopeAwareRepo {
	return &scopeAwareRepo{
		MemoryRepository: testkit.NewMemoryRepository[*order, int64](testkit.NewInt64Sequence(1)),
		declared:         declared,
	}
}

func newApp(t *testing.T, repo domcrud.IRepository[*order, int64]) crud.IApplication[*order, int64] {
	t.Helper()
	app, err := crud.NewApplication[*order, int64](repo, nil)
	require.NoError(t, err)
	return app
}

func policy() action.OperationPolicy {
	return action.OperationPolicy{
		Create: "order:api:create",
		Read:   "order:api:read",
		Update: "order:api:update",
		Delete: "order:api:delete",
		List:   "order:api:list",
	}
}

func allowAuthorizer(t *testing.T) scoped.IAuthorizer {
	t.Helper()
	authorizer, err := scoped.NewAuthorizer(
		scoped.ResourceResolverFunc(func(any) (scoped.Resource, bool) {
			return scoped.Resource{Kind: "order", ID: "1", ManagedScopeID: 101}, true
		}),
		scoped.EvaluatorFunc(func(_ context.Context, _ string, resources []scoped.Resource) (scoped.Decision, error) {
			if len(resources) == 0 {
				return scoped.Allow(scoped.Resource{Kind: "order", ID: "*", ManagedScopeID: 101}), nil
			}
			return scoped.Allow(resources...), nil
		}))
	require.NoError(t, err)
	return authorizer
}

type grantChecker struct {
	granted map[string]bool
	seen    []string
}

func (c *grantChecker) RequireAction(_ context.Context, code string) error {
	c.seen = append(c.seen, code)
	if c.granted[code] {
		return nil
	}
	return errors.NewCode(errors.Forbidden, "action denied")
}

// 不启用任何能力时必须报错——L0 应直接裸用 crud.NewApplication，
// 而不是构造一个什么都不做的"安全"包装。
func TestProfileRejectsEmptyCapabilitySet(t *testing.T) {
	_, err := security.Profile[*order, int64]{}.Build(newApp(t, newRepo(true)))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestProfileRejectsNilApplication(t *testing.T) {
	_, err := security.Profile[*order, int64]{
		ActionChecker: &grantChecker{},
		Policy:        policy(),
	}.Build(nil)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// L1 单档位装配。
func TestProfileActionOnly(t *testing.T) {
	checker := &grantChecker{granted: map[string]bool{"order:api:list": true}}
	secured, err := security.Profile[*order, int64]{
		ActionChecker: checker,
		Policy:        policy(),
	}.Build(newApp(t, newRepo(false)))
	require.NoError(t, err)

	_, err = secured.List(context.Background(), 0, 10)
	require.NoError(t, err)
	require.True(t, errors.Is(secured.Create(context.Background(), &order{}), errors.Forbidden))
}

// L3 装配：未声明范围列的仓储必须在**启动期**被拒绝。
func TestProfileScopedRequiresScopeDeclaration(t *testing.T) {
	_, err := security.Profile[*order, int64]{
		Authorizer: allowAuthorizer(t),
		EntityType: "order",
		Policy:     policy(),
	}.Build(newApp(t, newRepo(false)))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// L1 + L3 组合：顺序为 Action(Scoped(app))，动作不通过时不进入资源编排。
func TestProfileActionWrapsScoped(t *testing.T) {
	repo := newRepo(true)
	checker := &grantChecker{granted: map[string]bool{}} // 全部拒绝
	secured, err := security.Profile[*order, int64]{
		ActionChecker: checker,
		Authorizer:    allowAuthorizer(t),
		EntityType:    "order",
		Policy:        policy(),
	}.Build(newApp(t, repo))
	require.NoError(t, err)

	err = secured.Create(context.Background(), &order{Name: "n1"})
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Equal(t, []string{"order:api:create"}, checker.seen)
	require.False(t, repo.sawWrite, "动作校验未通过时不得进入资源编排与写入")
}

// 动作通过后，L3 的写约束必须仍能到达仓储——外层包装不得截断受控通道。
func TestProfileConstraintSurvivesBothLayers(t *testing.T) {
	repo := newRepo(true)
	checker := &grantChecker{granted: map[string]bool{"order:api:create": true}}
	secured, err := security.Profile[*order, int64]{
		ActionChecker: checker,
		Authorizer:    allowAuthorizer(t),
		EntityType:    "order",
		Policy:        policy(),
	}.Build(newApp(t, repo))
	require.NoError(t, err)

	require.NoError(t, secured.Create(context.Background(), &order{Name: "n1"}))
	require.True(t, repo.sawWrite, "写约束必须穿过两层装饰到达仓储")
}

// 组合装配同样不得丢失底层可选能力接口。
func TestProfilePreservesOptionalCapabilities(t *testing.T) {
	secured, err := security.Profile[*order, int64]{
		ActionChecker: &grantChecker{granted: map[string]bool{}},
		Authorizer:    allowAuthorizer(t),
		EntityType:    "order",
		Policy:        policy(),
	}.Build(newApp(t, newRepo(true)))
	require.NoError(t, err)

	_, ok := secured.(crud.IBatchWriter[*order, int64])
	require.True(t, ok)
	_, ok = secured.(crud.IRepositoryProvider[*order, int64])
	require.False(t, ok)
}

// 非法权限码必须在装配期暴露。
func TestProfileValidatesPolicyAtBuildTime(t *testing.T) {
	_, err := security.Profile[*order, int64]{
		ActionChecker: &grantChecker{},
		Policy:        action.OperationPolicy{Create: "api:order"},
	}.Build(newApp(t, newRepo(true)))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}
