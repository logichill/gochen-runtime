package authz

import (
	"context"
	"testing"

	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/errors"
	"gochen/testkit/require"
)

// 默认 L1 / L3 判定实现的行为特征测试。

func principalCtx(t *testing.T, principal Principal) context.Context {
	t.Helper()
	ctx, err := WithPrincipal(context.Background(), principal)
	require.NoError(t, err)
	return ctx
}

// ---------- L1 NewActionChecker ----------

func TestActionChecker_GrantsExactAndWildcardCodes(t *testing.T) {
	checker := NewActionChecker()
	ctx := principalCtx(t, Principal{
		SubjectID:   7,
		Permissions: []string{"order:api:read", "user:api:*"},
	})

	require.NoError(t, checker.RequireAction(ctx, "order:api:read"))
	require.NoError(t, checker.RequireAction(ctx, "user:api:update"))
	require.True(t, errors.Is(checker.RequireAction(ctx, "order:api:delete"), errors.Forbidden))
}

func TestActionChecker_SystemPrincipalBypasses(t *testing.T) {
	checker := NewActionChecker()
	ctx := principalCtx(t, Principal{SubjectID: 1, IsSystem: true})
	require.NoError(t, checker.RequireAction(ctx, "order:api:delete"))
}

// 错误码必须与 action.IActionChecker 声明的契约一致：缺主体是 401 不是 403，
// 码拼错是 400 不是 403——否则下游把装配错误当权限问题排查。
func TestActionChecker_ErrorContract(t *testing.T) {
	checker := NewActionChecker()

	err := checker.RequireAction(context.Background(), "order:api:read")
	require.True(t, errors.Is(err, errors.Unauthorized))

	ctx := principalCtx(t, Principal{SubjectID: 7, Permissions: []string{"order:api:read"}})
	err = checker.RequireAction(ctx, "not-a-three-segment-code")
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// ---------- L3 NewEvaluator ----------

func TestEvaluator_DeniesWithoutPrincipal(t *testing.T) {
	decision, err := NewEvaluator().Evaluate(context.Background(), "order:api:read", nil)
	require.NoError(t, err)
	require.False(t, decision.IsActionAllowed())
	require.Equal(t, scoped.ReasonPrincipalMissing, decision.ReasonCode)
}

func TestEvaluator_DeniesUngrantedAction(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:read"}})
	decision, err := NewEvaluator().Evaluate(ctx, "order:api:delete", []scoped.Resource{
		{Kind: "order", ID: "1", ManagedScopeID: 101},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonActionDenied, decision.ReasonCode)
}

// 列表读没有具体目标：动作通过即放行，且**不得**回填占位资源。
// 回填会让 IsAllowed 为真，等于发出一张"空 allow 通行证"。
func TestEvaluator_ListReadAllowsActionWithoutFabricatingResources(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:list"}})
	decision, err := NewEvaluator().Evaluate(ctx, "order:api:list", nil)
	require.NoError(t, err)
	require.True(t, decision.IsActionAllowed())
	require.False(t, decision.IsAllowed())
	require.Equal(t, 0, len(decision.AuthorizedResources))
}

// 有目标时按 managed scope 归属判定，且 ActiveScopeID 是默认锚点。
func TestEvaluator_ScopeMembershipDecidesTargetedAccess(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:update"}})
	evaluator := NewEvaluator()

	decision, err := evaluator.Evaluate(ctx, "order:api:update", []scoped.Resource{
		{Kind: "order", ID: "1", ManagedScopeID: 101, Revision: "3"},
	})
	require.NoError(t, err)
	require.True(t, decision.IsAllowed())
	require.Equal(t, "3", decision.WriteConstraint().Resources[0].Revision)

	decision, err = evaluator.Evaluate(ctx, "order:api:update", []scoped.Resource{
		{Kind: "order", ID: "2", ManagedScopeID: 999},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonOutOfScope, decision.ReasonCode)
}

// 一批资源里有一条越界即整体拒绝——部分放行会让调用方以为整批都被授权。
func TestEvaluator_BatchDeniesWhenAnyResourceIsOutOfScope(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:delete"}})
	decision, err := NewEvaluator().Evaluate(ctx, "order:api:delete", []scoped.Resource{
		{Kind: "order", ID: "1", ManagedScopeID: 101},
		{Kind: "order", ID: "2", ManagedScopeID: 202},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonOutOfScope, decision.ReasonCode)
}

// 归属未知的资源一律拒绝：框架不拿"当前唯一可见范围"替业务决定数据落在哪儿。
func TestEvaluator_RefusesResourceWithoutManagedScope(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:create"}})
	decision, err := NewEvaluator().Evaluate(ctx, "order:api:create", []scoped.Resource{
		{Kind: "order", ID: "1"},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonNoResource, decision.ReasonCode)
}

// 平台级资源只对可见范围为整棵树的主体开放，否则租户内的 update 权限能改到平台配置。
func TestEvaluator_GlobalScopeResourceRequiresGlobalVisibility(t *testing.T) {
	evaluator := NewEvaluator()
	resource := scoped.Resource{Kind: "menu", ID: "1", GlobalScope: true}

	tenantCtx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"menu:api:update"}})
	decision, err := evaluator.Evaluate(tenantCtx, "menu:api:update", []scoped.Resource{resource})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())

	systemCtx := principalCtx(t, Principal{SubjectID: 1, IsSystem: true})
	decision, err = evaluator.Evaluate(systemCtx, "menu:api:update", []scoped.Resource{resource})
	require.NoError(t, err)
	require.True(t, decision.IsAllowed())
}

// 隔离归属不一致即拒绝：这是一致性断言（真正的执行点在仓储），
// 解析器串行或入参被构造时不该走到放行。
func TestEvaluator_RefusesResourceFromAnotherIsolationSpace(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:read"}})
	ctx, err := contextx.WithTenantID(ctx, "tenant-a")
	require.NoError(t, err)

	decision, err := NewEvaluator().Evaluate(ctx, "order:api:read", []scoped.Resource{
		{Kind: "order", ID: "1", ManagedScopeID: 101, TenantID: "tenant-b"},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonOutOfScope, decision.ReasonCode)
}

// 显式 resolver 覆盖默认推导，用于表达"整棵子树可见"。
func TestEvaluator_ExplicitResolverOverridesDerivedScope(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:read"}})
	evaluator := NewEvaluator(WithEvaluatorDataScope(
		scoped.DataScopeResolverFunc(func(context.Context) (scoped.DataScope, error) {
			return scoped.Filtered(101, 202, 303), nil
		})))

	decision, err := evaluator.Evaluate(ctx, "order:api:read", []scoped.Resource{
		{Kind: "order", ID: "2", ManagedScopeID: 202},
	})
	require.NoError(t, err)
	require.True(t, decision.IsAllowed())
}

// 推不出范围就拒绝：既没有绑定范围、又不是 system、也没有 ActiveScopeID。
func TestEvaluator_FailsClosedWithoutAnyScope(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 7, Permissions: []string{"order:api:read"}})
	decision, err := NewEvaluator().Evaluate(ctx, "order:api:read", []scoped.Resource{
		{Kind: "order", ID: "1", ManagedScopeID: 101},
	})
	require.NoError(t, err)
	require.False(t, decision.IsAllowed())
	require.Equal(t, scoped.ReasonOutOfScope, decision.ReasonCode)
}

// 权限码拼错是装配错误，报 InvalidInput 而不是静默 403。
func TestEvaluator_InvalidActionCodeIsAWiringError(t *testing.T) {
	ctx := principalCtx(t, Principal{SubjectID: 1, IsSystem: true})
	_, err := NewEvaluator().Evaluate(ctx, "order:read", nil)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

// 与 scoped.NewAuthorizer 组合可用：这是下游真正的装配形态。
func TestEvaluator_ComposesWithAuthorizer(t *testing.T) {
	authorizer, err := scoped.NewAuthorizer(
		scoped.ResourceResolverFunc(func(target any) (scoped.Resource, bool) {
			id, ok := target.(string)
			return scoped.Resource{Kind: "order", ID: id, ManagedScopeID: 101}, ok
		}),
		NewEvaluator(),
	)
	require.NoError(t, err)

	ctx := principalCtx(t, Principal{SubjectID: 7, ActiveScopeID: 101, Permissions: []string{"order:api:update"}})
	require.NoError(t, authorizer.Require(ctx, "order:api:update", "1"))

	denied := principalCtx(t, Principal{SubjectID: 8, ActiveScopeID: 999, Permissions: []string{"order:api:update"}})
	require.True(t, errors.Is(authorizer.Require(denied, "order:api:update", "1"), errors.Forbidden))
}
