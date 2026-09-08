package authz

import (
	"context"

	"gochen/auth/action"
	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/errors"
)

// 默认 L3 策略评估器。
//
// 补的是"判定"这一环：`scoped.NewAuthorizer(resolver, evaluator)` 要求注入
// evaluator，而框架此前一个实现都不提供，导致最容易写错、写错后果最严重的
// 那段代码由每个下游各写一遍。本实现覆盖 RBAC + 数据范围这条最常见路径；
// 接 OPA / Casbin / SpiceDB 的把 IEvaluator 换掉即可，其余装配不动。
//
// 判定三步（顺序即成本顺序，先便宜后昂贵）：
//  1. 动作：Principal.AllowsPermission，与 L1 同一套规则；
//  2. 无目标（列表读）：动作通过即 allow，且**不回填占位资源**——可见范围
//     随后由仓储的 DataScope 过滤决定（见 Decision.IsActionAllowed 的说明）；
//  3. 有目标：逐个资源核对隔离归属与 managed scope 归属，全部通过才 allow。
//
// 不做的事（都是刻意的）：
//   - 不猜 managed scope。资源的 ManagedScopeID 为 0 即拒绝，而不是拿"当前
//     可见范围里唯一那个"替它填上：写路径上这等于框架替业务决定数据落在哪个
//     范围里。目标范围由 IResourceResolver
//     显式给出；
//   - 不提供 owner 免检（资源属主跳过动作校验）。"能改自己的东西"是一条业务
//     策略而非通用规则，且它把"有没有权限"和"是不是本人"两件事绞在一起；
//     需要的话在本评估器外侧包一层，语义留在业务侧更清楚；
//   - 不提供"超管角色名"配置。Principal.IsSystem 已经承担这件事，角色名到
//     IsSystem 的映射属于 AuthN 阶段的职责，放在判定期会出现两套超管口径。

// EvaluatorOption 配置默认评估器。
type EvaluatorOption func(*principalEvaluator)

// WithEvaluatorDataScope 指定数据范围的解析来源。
//
// 不配置时按以下顺序推导（见 principalEvaluator.resolveScope）：
// ctx 上已绑定的 DataScope → IsSystem 主体视为全局 → Principal.ActiveScopeID
// 单范围 → 拒绝。
func WithEvaluatorDataScope(resolver scoped.IDataScopeResolver) EvaluatorOption {
	return func(e *principalEvaluator) {
		if e == nil || resolver == nil {
			return
		}
		e.scopeResolver = resolver
	}
}

// NewEvaluator 返回基于 ctx 中 Principal 与 DataScope 的默认策略评估器。
func NewEvaluator(opts ...EvaluatorOption) scoped.IEvaluator {
	evaluator := &principalEvaluator{}
	for _, opt := range opts {
		if opt != nil {
			opt(evaluator)
		}
	}
	return evaluator
}

type principalEvaluator struct {
	scopeResolver scoped.IDataScopeResolver
}

var _ scoped.IEvaluator = (*principalEvaluator)(nil)

// Evaluate 对目标资源做出授权判定。
func (e *principalEvaluator) Evaluate(
	ctx context.Context,
	actionCode string,
	resources []scoped.Resource,
) (scoped.Decision, error) {
	if !action.IsValidCode(actionCode) {
		// 装配期拼错的权限码不是"权限不足"，报 InvalidInput 让它在联调第一次
		// 请求就暴露，而不是变成一条查不出原因的 403。
		return scoped.Deny(scoped.ReasonActionDenied),
			errors.NewCode(errors.InvalidInput, "invalid action code").WithContext("action", actionCode)
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return scoped.Deny(scoped.ReasonPrincipalMissing), nil
	}
	if !principal.AllowsPermission(actionCode) {
		return scoped.Deny(scoped.ReasonActionDenied), nil
	}
	if len(resources) == 0 {
		// 列表读：调用方问的是"能不能列这类资源"。此处绝不能造一个占位资源来
		// 满足 Decision.IsAllowed，那正是"空 allow 通行证"。
		return scoped.Decision{Effect: scoped.EffectAllow}, nil
	}

	scope, err := e.resolveScope(ctx, principal)
	if err != nil {
		return scoped.Deny(scoped.ReasonOutOfScope), err
	}
	isolationKey := contextx.TenantID(ctx)
	for _, resource := range resources {
		if reason, allowed := permitsResource(resource.Normalize(), scope, isolationKey); !allowed {
			return scoped.Deny(reason), nil
		}
	}
	return scoped.Allow(resources...), nil
}

// resolveScope 解析本次判定使用的数据范围。
//
// 显式配置的 resolver 优先（ResolveDataScope 内部仍让 ctx 上已绑定的范围优先，
// 保证批处理里固定一次范围后不被逐条重算）。
//
// 未配置时的推导链只有三档，且最后一档是拒绝：
//   - ctx 已绑定范围 → 用它；
//   - IsSystem → 全局。IsSystem 已经让 AllowsPermission 无条件放行，再给它
//     一个受限范围没有意义；反过来说，不该看到全部数据的主体就不该被标记 IsSystem；
//   - ActiveScopeID > 0 → 该单范围。见 Core docs/architecture/layered-authz.md「隔离、范围与主体」：
//     "这次以哪个身份工作"由登录时显式激活，不靠推断；
//   - 其余 → ScopeDenyAll，判定拒绝。想要"整棵子树可见"（VisibleScopes）
//     必须显式提供 resolver，框架不替业务遍历范围树。
func (e *principalEvaluator) resolveScope(ctx context.Context, principal Principal) (scoped.DataScope, error) {
	if e.scopeResolver != nil {
		return scoped.ResolveDataScope(ctx, e.scopeResolver)
	}
	if scope, ok := scoped.DataScopeFromContext(ctx); ok {
		return scope, nil
	}
	if principal.IsSystem {
		return scoped.Global(), nil
	}
	if principal.ActiveScopeID > 0 {
		return scoped.Filtered(principal.ActiveScopeID), nil
	}
	return scoped.DenyAll(), nil
}

// permitsResource 核对单个资源是否落在本次请求的边界内。
func permitsResource(
	resource scoped.Resource,
	scope scoped.DataScope,
	isolationKey string,
) (string, bool) {
	// 隔离归属：两侧都有值且不一致即拒。
	//
	// 这是一致性断言，不是隔离的执行点——L2 隔离由仓储在存储层原子执行
	// 。放在这里是因为资源描述由下游 resolver 产出，一旦它
	// 报出了别的隔离空间的归属，那要么是解析器串了行，要么是入参被构造过，
	// 两种都不该走到 PDP 放行。
	if resource.TenantID != "" && isolationKey != "" && resource.TenantID != isolationKey {
		return scoped.ReasonOutOfScope, false
	}
	if resource.GlobalScope {
		// 平台级资源不属于任何 managed scope，因此只有可见范围是整棵树的主体
		// 才可以操作它。否则一个租户内的 update 权限就能改到平台配置。
		if scope.Normalize().Kind != scoped.ScopeGlobal {
			return scoped.ReasonOutOfScope, false
		}
		return "", true
	}
	if resource.ManagedScopeID <= 0 {
		// 归属未知的资源无法判定。见文件头："不猜 managed scope"。
		return scoped.ReasonNoResource, false
	}
	if !scope.Contains(resource.ManagedScopeID) {
		return scoped.ReasonOutOfScope, false
	}
	return "", true
}
