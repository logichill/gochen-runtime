package quick

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gochen-runtime/host/authz"
	"gochen-runtime/security"
	"gochen/app/crud"
	"gochen/auth/action"
	"gochen/auth/scoped"
	"gochen/domain"
	domcrud "gochen/domain/crud"
	"gochen/errors"
)

// App 创建使用默认标准配置的 CRUD Application 实例。
//
// 默认注入标准 ServiceConfig (AutoValidate=true, MaxBatchSize=1000, MaxPageSize=1000)。
func App[T domain.IEntity[ID], ID comparable](repository domcrud.IRepository[T, ID]) (*crud.Application[T, ID], error) {
	if repository == nil || isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	return crud.NewApplication[T, ID](repository, nil)
}

// ScopedApp 快速装配 L3 数据范围资源授权应用服务（包装已有 Application）。
//
// 内部自动组装 TypedResourceResolver、默认 Evaluator (authz.NewEvaluator)、
// 基于 Principal.ActiveScopeID 的默认 ScopeResolver 与标准 Authorizer，
// 并自动派生三段式 CRUD 动作权限码（如 <kind>:api:create 等）。
func ScopedApp[T domain.IEntity[ID], ID comparable](
	app crud.IApplication[T, ID],
	kind string,
	scopeFn func(T) int64,
	opts ...ScopedAppOption,
) (crud.IApplication[T, ID], error) {
	if app == nil || isNil(app) {
		return nil, errors.NewCode(errors.InvalidInput, "app cannot be nil")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, errors.NewCode(errors.InvalidInput, "resource kind cannot be empty")
	}
	if scopeFn == nil {
		return nil, errors.NewCode(errors.InvalidInput, "scopeFn cannot be nil")
	}

	return buildScopedApp[T, ID](app, kind, scopeFn, opts...)
}

// ScopedAppFromRepo 快速基于 IRepository 构造基础 App 并装配 L3 数据范围资源授权服务。
func ScopedAppFromRepo[T domain.IEntity[ID], ID comparable](
	repository domcrud.IRepository[T, ID],
	kind string,
	scopeFn func(T) int64,
	opts ...ScopedAppOption,
) (crud.IApplication[T, ID], error) {
	if repository == nil || isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	baseApp, err := App[T, ID](repository)
	if err != nil {
		return nil, err
	}
	return ScopedApp[T, ID](baseApp, kind, scopeFn, opts...)
}

// ActionApp 快速装配 L1 动作级权限校验应用服务（包装已有 Application）。
//
// 根据 prefix 自动推导标准 CRUD 动作权限码（如 <prefix>:api:create 等）并挂载动作拦截。
func ActionApp[T domain.IEntity[ID], ID comparable](
	app crud.IApplication[T, ID],
	checker action.IActionChecker,
	prefix string,
) (crud.IApplication[T, ID], error) {
	if app == nil || isNil(app) {
		return nil, errors.NewCode(errors.InvalidInput, "app cannot be nil")
	}
	if checker == nil || isNil(checker) {
		return nil, errors.NewCode(errors.InvalidInput, "checker cannot be nil")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, errors.NewCode(errors.InvalidInput, "prefix cannot be empty")
	}

	policy := deriveOperationPolicy(prefix)
	return security.Action[T, ID](app, checker, policy)
}

// ActionAppFromRepo 快速基于 IRepository 构造基础 App 并装配 L1 动作级权限校验服务。
func ActionAppFromRepo[T domain.IEntity[ID], ID comparable](
	repository domcrud.IRepository[T, ID],
	checker action.IActionChecker,
	prefix string,
) (crud.IApplication[T, ID], error) {
	if repository == nil || isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	baseApp, err := App[T, ID](repository)
	if err != nil {
		return nil, err
	}
	return ActionApp[T, ID](baseApp, checker, prefix)
}

// ScopedAppOption 用于配置 ScopedApp 的可选装配行为。
type ScopedAppOption func(*scopedAppOptions)

type scopedAppOptions struct {
	policy        *action.OperationPolicy
	scopeResolver scoped.IDataScopeResolver
	evaluator     scoped.IEvaluator
	actionChecker action.IActionChecker
	authorizer    scoped.IAuthorizer
}

// WithScopedPolicy 覆写默认派生的 CRUD 动作权限策略。
func WithScopedPolicy(policy action.OperationPolicy) ScopedAppOption {
	return func(o *scopedAppOptions) {
		o.policy = &policy
	}
}

// WithScopedScopeResolver 配置 L3 读路径的数据范围解析器。
func WithScopedScopeResolver(resolver scoped.IDataScopeResolver) ScopedAppOption {
	return func(o *scopedAppOptions) {
		o.scopeResolver = resolver
	}
}

// WithScopedEvaluator 覆写默认的策略评估器（缺省使用 authz.NewEvaluator）。
func WithScopedEvaluator(evaluator scoped.IEvaluator) ScopedAppOption {
	return func(o *scopedAppOptions) {
		o.evaluator = evaluator
	}
}

// WithScopedActionChecker 额外挂载 L1 动作级快速校验器。
func WithScopedActionChecker(checker action.IActionChecker) ScopedAppOption {
	return func(o *scopedAppOptions) {
		o.actionChecker = checker
	}
}

// WithScopedAuthorizer 覆写默认组装的 Authorizer（例如已套用 Governed 治理外壳的 Authorizer）。
func WithScopedAuthorizer(authorizer scoped.IAuthorizer) ScopedAppOption {
	return func(o *scopedAppOptions) {
		o.authorizer = authorizer
	}
}

func buildScopedApp[T domain.IEntity[ID], ID comparable](
	base crud.IApplication[T, ID],
	kind string,
	scopeFn func(T) int64,
	opts ...ScopedAppOption,
) (crud.IApplication[T, ID], error) {
	var options scopedAppOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	policy := deriveOperationPolicy(kind)
	if options.policy != nil {
		policy = *options.policy
	}

	entityType := kind
	if provider, ok := base.(crud.IRepositoryProvider[T, ID]); ok {
		repo := provider.Repository()
		if kindProbe, ok := any(repo).(scoped.IResourceKindProbe); ok && kindProbe != nil {
			if rk := strings.TrimSpace(kindProbe.ResourceKind()); rk != "" {
				entityType = rk
			}
		}
	}

	scopeResolver := options.scopeResolver
	if scopeResolver == nil {
		scopeResolver = scoped.DataScopeResolverFunc(func(ctx context.Context) (scoped.DataScope, error) {
			if p, ok := authz.PrincipalFromContext(ctx); ok {
				if p.IsSystem {
					return scoped.Global(), nil
				}
				if p.ActiveScopeID > 0 {
					return scoped.Filtered(p.ActiveScopeID), nil
				}
			}
			return scoped.DenyAll(), errors.NewCode(errors.Forbidden, "no data scope available")
		})
	}

	var authorizer scoped.IAuthorizer
	if options.authorizer != nil {
		authorizer = options.authorizer
	} else {
		var zeroID ID
		resolver := scoped.TypedResourceResolver[T](func(entity T) (scoped.Resource, bool) {
			val := reflect.ValueOf(entity)
			if !val.IsValid() || (val.Kind() == reflect.Ptr && val.IsNil()) {
				return scoped.Resource{}, false
			}
			scopeID := scopeFn(entity)
			var idStr string
			if entity.GetID() != zeroID {
				idStr = fmt.Sprint(entity.GetID())
			}
			return scoped.Resource{
				Kind:           entityType,
				ID:             idStr,
				ManagedScopeID: scopeID,
			}, true
		})

		evaluator := options.evaluator
		if evaluator == nil {
			evaluator = authz.NewEvaluator(authz.WithEvaluatorDataScope(scopeResolver))
		}

		var err error
		authorizer, err = scoped.NewAuthorizer(resolver, evaluator)
		if err != nil {
			return nil, err
		}
	}

	profile := security.Profile[T, ID]{
		ActionChecker: options.actionChecker,
		Policy:        policy,
		Authorizer:    authorizer,
		EntityType:    entityType,
		ScopeResolver: scopeResolver,
	}

	return profile.Build(base)
}

func deriveOperationPolicy(prefix string) action.OperationPolicy {
	prefix = strings.TrimSpace(prefix)
	if strings.Count(prefix, ":") == 0 {
		prefix = prefix + ":api"
	}
	return action.OperationPolicy{
		Create: prefix + ":create",
		Read:   prefix + ":read",
		Update: prefix + ":update",
		Delete: prefix + ":delete",
		List:   prefix + ":list",
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return val.IsNil()
	default:
		return false
	}
}
