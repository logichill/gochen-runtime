package authz

import (
	"context"
	"strings"

	"gochen-runtime/internal/normalize"
	"gochen/auth/action"
	"gochen/contextx"
	"gochen/errors"
)

// ExecutionMetadata 表达后台执行或重放授权的元数据。
type ExecutionMetadata struct {
	TraceID    string
	RequestID  string
	DecisionID string
	JobID      string
	EventID    string
	Source     string
}

type principalContextKey struct{}

// Principal 表达当前操作主体的最小授权语义。
type Principal struct {
	SubjectID       int64
	HomeTenantID    int64
	HomeScopeID     int64
	ActiveScopeID   int64
	ActiveBindingID int64
	Roles           []string
	Permissions     []string
	// PermissionScopes 由可信认证入口解析，限定权限可使用的授权域；不改变数据可见范围。
	PermissionScopes []string
	IsSystem         bool
}

func (p Principal) Clone() Principal {
	cloned := Principal{
		SubjectID:        normalize.PositiveID(p.SubjectID),
		HomeTenantID:     normalize.PositiveID(p.HomeTenantID),
		HomeScopeID:      normalize.PositiveID(p.HomeScopeID),
		ActiveScopeID:    normalize.PositiveID(p.ActiveScopeID),
		ActiveBindingID:  normalize.PositiveID(p.ActiveBindingID),
		Roles:            normalize.Strings(p.Roles),
		Permissions:      normalize.Strings(p.Permissions),
		PermissionScopes: normalize.Strings(p.PermissionScopes),
		IsSystem:         p.IsSystem,
	}
	return cloned
}

// HasPermission 判断主体是否显式持有某个权限。
func (p Principal) HasPermission(permission string) bool {
	permission = strings.TrimSpace(permission)
	if permission == "" {
		return false
	}
	for _, granted := range p.Permissions {
		if strings.TrimSpace(granted) == permission {
			return true
		}
	}
	return false
}

// AllowsPermission 判断主体是否满足某个权限要求。
//
// 约定：
// - 空权限名按 fail-closed 处理；
// - IsSystem 主体直接放行；
// - 允许显式授予全通配权限 `*:*:*`（常用于平台管理员）；
// - 其他通配 pattern（如 `user:api:*`）按段匹配。
func (p Principal) AllowsPermission(permission string) bool {
	permission = strings.TrimSpace(permission)
	if permission == "" {
		return false
	}
	if p.IsSystem {
		return true
	}
	if p.HasPermission(permission) {
		return true
	}
	for _, granted := range p.Permissions {
		if action.PatternMatches(granted, permission) {
			return true
		}
	}
	return false
}

// WithPrincipal 将主体信息写入 context。
func WithPrincipal(ctx context.Context, principal Principal) (context.Context, error) {
	ctx, err := contextx.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	principal = principal.Clone()
	ctx, err = contextx.WithUserID(ctx, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, principalContextKey{}, principal)
	return ctx, nil
}

// PrincipalFromContext 从 context 中读取主体。
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	if principal, ok := ctx.Value(principalContextKey{}).(Principal); ok {
		return principal.Clone(), true
	}
	principal := Principal{SubjectID: contextx.UserID(ctx)}
	if principal.SubjectID == 0 {
		return Principal{}, false
	}
	return principal.Clone(), true
}

// RequirePrincipal 从 context 中读取主体；缺失时返回 Unauthorized。
func RequirePrincipal(ctx context.Context) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, errors.NewCode(errors.Unauthorized, "principal is required")
	}
	return principal, nil
}

type activeScopeContextKey string

const (
	contextKeyActiveScopeID   activeScopeContextKey = "auth_active_scope_id"
	contextKeyActiveScopeKind activeScopeContextKey = "auth_active_scope_kind"
)

// BindActiveScopeContext 将 active scope 元数据写入通用 context，便于 service/runtime 读取。
func BindActiveScopeContext(ctx context.Context, scopeID int64, scopeKind string) context.Context {
	if ctx == nil {
		return nil
	}
	if scopeID > 0 {
		ctx = context.WithValue(ctx, contextKeyActiveScopeID, scopeID)
	}
	if scopeKind = strings.TrimSpace(scopeKind); scopeKind != "" {
		ctx = context.WithValue(ctx, contextKeyActiveScopeKind, scopeKind)
	}
	return ctx
}

// ClearActiveScopeContext 清空通用 context 上的 active scope 元数据。
func ClearActiveScopeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	ctx = context.WithValue(ctx, contextKeyActiveScopeID, int64(0))
	ctx = context.WithValue(ctx, contextKeyActiveScopeKind, "")
	return ctx
}

// ActiveScopeIDFromContext 从通用 context 读取当前生效的 active scope ID。
func ActiveScopeIDFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	if val := ctx.Value(contextKeyActiveScopeID); val != nil {
		if scopeID, ok := val.(int64); ok {
			return scopeID
		}
	}
	return 0
}

// ActiveScopeKindFromContext 从通用 context 读取当前生效的 active scope 类型。
func ActiveScopeKindFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val := ctx.Value(contextKeyActiveScopeKind); val != nil {
		if scopeKind, ok := val.(string); ok {
			return strings.TrimSpace(scopeKind)
		}
	}
	return ""
}

type executionMetadataContextKey struct{}

// WithExecutionMetadata 将后台执行元数据写入 context。
func WithExecutionMetadata(ctx context.Context, meta ExecutionMetadata) (context.Context, error) {
	ctx, err := contextx.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	if meta.TraceID != "" {
		ctx, err = contextx.WithTraceID(ctx, meta.TraceID)
		if err != nil {
			return nil, err
		}
	}
	if meta.RequestID != "" {
		ctx, err = contextx.WithRequestID(ctx, meta.RequestID)
		if err != nil {
			return nil, err
		}
	}
	return context.WithValue(ctx, executionMetadataContextKey{}, meta), nil
}

// ExecutionMetadataFromContext 从 context 中读取后台执行元数据。
func ExecutionMetadataFromContext(ctx context.Context) (ExecutionMetadata, bool) {
	if ctx == nil {
		return ExecutionMetadata{}, false
	}
	if meta, ok := ctx.Value(executionMetadataContextKey{}).(ExecutionMetadata); ok {
		return meta, true
	}
	return ExecutionMetadata{}, false
}
