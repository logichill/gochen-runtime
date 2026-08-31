// Package security 提供安全能力渐进分层的**标准装配入口**（Profile）。
//
// 存在意义：
//   - 固化包装顺序，禁止业务层自行拼接——顺序错了安全语义会被绕过；
//   - 把装配错误集中暴露在**启动期**，而不是等到某个请求路径第一次被调用；
//   - 让"这个 Application 处在哪个安全档位"成为一行可审计的声明。
//
// 档位与装配方式：
//
//	L0 Plain    直接裸用 crud.NewApplication，不 import 本包
//	L1 Action   security.Action(app, checker, policy)
//	L2 Isolated Repo 构造期声明：ormrepo.NewRepo(orm, table, ormrepo.WithIsolation(...))
//	L3 Scoped   security.Scoped(app, authorizer, cfg)  —— Repo 需 WithScope(...)
//	L4 Governed security.Governed(authorizer, opts...) 装饰 Authorizer 后传给 Scoped
package security

import (
	secaction "gochen/app/security/action"
	secscoped "gochen/app/security/scoped"

	"gochen/app/crud"
	authaction "gochen/auth/action"
	"gochen/auth/governance"
	authscoped "gochen/auth/scoped"
	"gochen/domain"
	"gochen/errors"
)

// Action 装配 L1：动作级权限校验。
//
// 装配期校验 checker 非空且 policy 中权限码合法。
func Action[T domain.IEntity[ID], ID comparable](
	app crud.IApplication[T, ID],
	checker authaction.IActionChecker,
	policy authaction.OperationPolicy,
) (crud.IApplication[T, ID], error) {
	return secaction.New[T, ID](app, checker, policy)
}

// ScopedConfig 复用 app/security/scoped 的编排配置。
type ScopedConfig = secscoped.Config

// Scoped 装配 L3：资源授权 + 受控约束通道。
//
// 装配期断言底层仓储已声明范围列（ormrepo.WithScope），
// 未声明即 fail-fast——否则写约束会被静默丢弃、请求全部放行。
func Scoped[T domain.IEntity[ID], ID comparable](
	app crud.IApplication[T, ID],
	authorizer authscoped.IAuthorizer,
	config ScopedConfig,
) (crud.IApplication[T, ID], error) {
	return secscoped.New[T, ID](app, authorizer, config)
}

// Governed 装配 L4：以治理外壳装饰 Authorizer。
//
// 注意装饰的是 Authorizer 而不是 Application——治理不使用时不参与编译。
// 返回值应传给 Scoped 而不是再包一层 Application。
func Governed(base authscoped.IAuthorizer, opts ...governance.Option) (authscoped.IAuthorizer, error) {
	return governance.Wrap(base, opts...)
}

// Profile 描述一次完整的安全装配意图。
//
// 用法：只填需要的档位，Build 会按固定顺序装配并做启动期校验。
type Profile[T domain.IEntity[ID], ID comparable] struct {
	// ActionChecker 非空时启用 L1 动作级快速校验。
	ActionChecker authaction.IActionChecker

	// Policy 是 CRUD 操作 → 动作码映射，L1 与 L3 共用。
	Policy authaction.OperationPolicy

	// Authorizer 非空时启用 L3 资源授权。
	//
	// 若需要 L4 治理，请先用 Governed(...) 包装后再填入这里。
	Authorizer authscoped.IAuthorizer

	// EntityType 是 L3 写约束投放的目标实体标识，启用 L3 时必填。
	EntityType string

	// ScopeResolver 解析 L3 读路径的数据范围。
	ScopeResolver authscoped.IDataScopeResolver
}

// Build 按固定顺序装配安全档位并返回最终 Application。
//
// 包装顺序（外 → 内）：Action(Scoped(app))。
//   - Action 在外：动作码不通过时立即拒绝，省掉后续资源解析与 PDP 开销；
//   - Scoped 在内：紧贴基础 Application，保证写约束投放的 ctx
//     不会被外层再次派生而丢失。
//
// 顺序由本方法固化，调用方不得自行拼接（§4.3 风险控制）。
func (p Profile[T, ID]) Build(app crud.IApplication[T, ID]) (crud.IApplication[T, ID], error) {
	if app == nil {
		return nil, errors.NewCode(errors.InvalidInput, "application cannot be nil")
	}
	if p.ActionChecker == nil && p.Authorizer == nil {
		return nil, errors.NewCode(errors.InvalidInput,
			"security profile enables no capability; use crud.NewApplication directly for L0")
	}

	secured := app
	var err error

	if p.Authorizer != nil {
		secured, err = Scoped[T, ID](secured, p.Authorizer, ScopedConfig{
			EntityType:    p.EntityType,
			Policy:        p.Policy,
			ScopeResolver: p.ScopeResolver,
		})
		if err != nil {
			return nil, err
		}
	}
	if p.ActionChecker != nil {
		secured, err = Action[T, ID](secured, p.ActionChecker, p.Policy)
		if err != nil {
			return nil, err
		}
	}
	return secured, nil
}
