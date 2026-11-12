package authz

import (
	"context"
	"strings"

	"gochen-runtime/internal/normalize"
	coreauth "gochen/auth"
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

// principalDetails 将权限资料绑定到建立它们时的主体、租户与当前范围。
type principalDetails struct {
	bound     context.Context
	principal Principal
}

// Principal 表达当前操作主体的最小授权语义；主体与租户使用不透明字符串标识。
type Principal struct {
	SubjectID       string
	HomeTenantID    string
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
		SubjectID:        strings.TrimSpace(p.SubjectID),
		HomeTenantID:     strings.TrimSpace(p.HomeTenantID),
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

// WithPrincipal 在可信入口建立根身份，并绑定 Runtime 权限资料。
// 当前租户来自 contextx.TenantID；HomeTenantID 保留归属资料。
func WithPrincipal(ctx context.Context, principal Principal) (context.Context, error) {
	ctx, err := contextx.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	principal = principal.Clone()
	identity := coreauth.Principal{
		SubjectID: principal.SubjectID,
		TenantID:  contextx.TenantID(ctx),
		ScopeID:   principal.ActiveScopeID,
		System:    principal.IsSystem,
	}
	ctx, err = coreauth.WithPrincipal(ctx, identity)
	if err != nil {
		return nil, err
	}
	ctx, err = contextx.WithUserID(ctx, principal.SubjectID)
	if err != nil {
		return nil, err
	}
	details := principalDetails{
		bound:     ctx,
		principal: principal,
	}
	// 身份由根 Principal 读取；附属资料只保留权限及归属事实。
	details.principal.SubjectID = ""
	details.principal.ActiveScopeID = 0
	details.principal.IsSystem = false
	ctx = context.WithValue(ctx, principalContextKey{}, details)
	return ctx, nil
}

// PrincipalFromContext 从根身份读取主体，并附加仍匹配该身份的权限资料。
// 主体、租户或当前范围切换后，可信入口应重新建立权限资料。
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	identity, ok := coreauth.PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, false
	}
	var principal Principal
	if details, ok := ctx.Value(principalContextKey{}).(principalDetails); ok &&
		coreauth.HasSamePrincipalBinding(ctx, details.bound) {
		principal = details.principal.Clone()
	}
	principal.SubjectID = identity.SubjectID
	principal.ActiveScopeID = identity.ScopeID
	principal.IsSystem = identity.System
	return principal, true
}

// RequirePrincipal 从 context 中读取主体；缺失时返回 Unauthorized。
func RequirePrincipal(ctx context.Context) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, errors.NewCode(errors.Unauthorized, "principal is required")
	}
	return principal, nil
}

type activeScopeContextKey struct{}

type activeScopeBinding struct {
	bound context.Context
	id    int64
	kind  string
}

// BindActiveScopeContext 将 active scope 元数据写入通用 context，便于 service/runtime 读取。
func BindActiveScopeContext(ctx context.Context, scopeID int64, scopeKind string) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, activeScopeContextKey{}, activeScopeBinding{
		bound: ctx, id: normalize.PositiveID(scopeID), kind: strings.TrimSpace(scopeKind),
	})
}

// ClearActiveScopeContext 清空通用 context 上的 active scope 元数据。
func ClearActiveScopeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return BindActiveScopeContext(ctx, 0, "")
}

// ActiveScopeIDFromContext 从通用 context 读取当前生效的 active scope ID。
func ActiveScopeIDFromContext(ctx context.Context) int64 {
	return activeScopeFromContext(ctx).id
}

// ActiveScopeKindFromContext 从通用 context 读取当前生效的 active scope 类型。
func ActiveScopeKindFromContext(ctx context.Context) string {
	return activeScopeFromContext(ctx).kind
}

func activeScopeFromContext(ctx context.Context) activeScopeBinding {
	if ctx == nil {
		return activeScopeBinding{}
	}
	binding, ok := ctx.Value(activeScopeContextKey{}).(activeScopeBinding)
	current, currentOK := coreauth.PrincipalFromContext(ctx)
	bound, boundOK := coreauth.PrincipalFromContext(binding.bound)
	if !ok || currentOK != boundOK || !contextx.HasSameTenantBinding(ctx, binding.bound) {
		return activeScopeBinding{}
	}
	if currentOK && (!coreauth.HasSamePrincipalBinding(ctx, binding.bound) || current.System != bound.System ||
		(binding.id > 0 && binding.id != current.ScopeID)) {
		return activeScopeBinding{}
	}
	return binding
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
