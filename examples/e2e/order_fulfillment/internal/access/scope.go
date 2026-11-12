// Package access 集中本示例的授权装配：范围解析器与请求身份绑定。
package access

import (
	"context"
	"strconv"
	"strings"

	"gochen-runtime/host/authz"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/httpx"
)

// Resource 是本示例声明的订单资源名，权限码形如 order:api:create。
const Resource = "order"

// 身份事实请求头。示例由此模拟可信认证入口的产物；生产入口由认证中间件写入同等信息。
const (
	TenantHeader     = "X-Tenant-ID"
	OperatorHeader   = "X-Operator"
	ScopeHeader      = "X-Scope-ID"
	PermissionHeader = "X-Permissions"
)

// DepartmentScope 把调用方当前部门映射为 department 维度条件。
//
// 读、改、删都只作用于当前范围；创建时输出服务端赋值，客户端预填的归属冲突会被拒绝。
type DepartmentScope struct{}

// Vocabulary 声明本解析器可能引用的字段与可赋值的维度。
func (DepartmentScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{
		Refs:       []meta.Ref{meta.ByDimension("department")},
		Assignable: []meta.Dimension{"department"},
	}
}

// ResolveSpec 返回范围条件与创建赋值。
func (DepartmentScope) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok || principal.ActiveScopeID <= 0 {
		return spec.Spec{}, nil, errors.NewCode(errors.Unauthorized, "active scope is required")
	}
	return spec.Eq(meta.ByDimension("department"), principal.ActiveScopeID),
		[]auth.Assignment{{Dimension: "department", Value: principal.ActiveScopeID}}, nil
}

// BindIdentity 从请求头建立租户、主体、当前范围与权限。
//
// 缺少身份事实时返回错误，请求在进入用例前就失败；仓储的隔离校验继续独立生效。
func BindIdentity() httpx.Middleware {
	return func(ctx httpx.IContext, next func() error) error {
		requestContext := ctx.RequestContext()
		if requestContext == nil {
			return errors.NewCode(errors.Internal, "request context is missing")
		}
		bound, err := WithIdentity(requestContext,
			ctx.Header(TenantHeader),
			ctx.Header(OperatorHeader),
			ctx.Header(ScopeHeader),
			ctx.Header(PermissionHeader),
		)
		if err != nil {
			return err
		}
		ctx.SetContext(requestContext.WithContext(bound))
		return next()
	}
}

// WithIdentity 写入租户、主体、当前范围与权限，供直接调用 Application 的场景复用。
func WithIdentity(ctx context.Context, tenant, operator, scope, permissions string) (context.Context, error) {
	scopeID, err := strconv.ParseInt(strings.TrimSpace(scope), 10, 64)
	if err != nil {
		return nil, errors.NewCode(errors.InvalidInput, "scope id must be an integer")
	}
	bound, err := contextx.WithTenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	bound, err = contextx.WithOperator(bound, operator)
	if err != nil {
		return nil, err
	}
	return authz.WithPrincipal(bound, authz.Principal{
		SubjectID:     strings.TrimSpace(operator),
		ActiveScopeID: scopeID,
		Permissions:   SplitPermissions(permissions),
	})
}

// SplitPermissions 把逗号分隔的权限码归一为切片。
func SplitPermissions(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
