package repo

import (
	"context"
	"reflect"
	"strings"

	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/errors"
)

type dataScopeField struct {
	column string
	index  []int
}

func (f dataScopeField) hasField() bool {
	return len(f.index) > 0
}

type accessColumns struct {
	managedScope string
	isolation    string
	ownerID      string
	version      string
}

type dataScopeSchema struct {
	managedScope dataScopeField
	isolation    dataScopeField
	ownerID      dataScopeField
	version      dataScopeField
}

func (r *Repo[T, ID]) applyDataScope(quer *queryBuilder, policy isolationPolicy) (*queryBuilder, error) {
	if quer == nil {
		return nil, nil
	}
	quer, isolationApplied, err := r.applyIsolation(quer, policy)
	if err != nil {
		return nil, err
	}
	schema := r.accessSchema()
	// bounded 表达的是"本次查询真的被隔离条件收窄了"，而不是"声明了隔离列"。
	// 跨隔离开闸时隔离条件并未落地，此处必须如实为 false，否则 require-scope
	// 仓储会凭一条不存在的边界放过整表查询。
	bounded := isolationApplied
	if schema.managedScope.column == "" {
		if r.requireScope && !bounded {
			return nil, errors.NewCode(errors.Forbidden,
				"scope-required repository has no enforceable data boundary for this request").
				WithContext("resource_kind", r.resourceKind)
		}
		return quer, nil
	}
	scope, err := r.resolveDataScope(quer.ctx)
	if err != nil {
		return nil, err
	}
	if scope.Kind == scoped.ScopeGlobal {
		if r.requireScope && !bounded && !r.allowGlobalScope {
			return nil, r.globalScopeForbiddenError()
		}
		return quer, nil
	}
	visibleScopeIDs := normalizeVisibleScopeIDs(scope)
	if len(visibleScopeIDs) == 0 {
		// ScopeDenyAll 或空 ScopeFiltered：语义是"拒绝访问"而非"输入错误"，
		// 错误码与隔离路径统一为 Forbidden（§4.1 三态铁律 / §4.2）。
		return nil, errors.NewCode(errors.Forbidden, "data scope denies all access").
			WithContext("resource_kind", r.resourceKind)
	}
	if len(visibleScopeIDs) == 1 {
		return quer.Where(schema.managedScope.column+" = ?", visibleScopeIDs[0]), nil
	}
	return quer.Where(schema.managedScope.column+" IN ?", int64SliceToAny(visibleScopeIDs)), nil
}

// applyIsolation 强制执行 L2 隔离边界，但不应用 L3 managed scope。
// 资源边界解析需要跳过 DataScope 交给 PDP 判定，隔离则不能被任何查询入口绕过。
//
// 返回的 bool 表示隔离条件是否**真的落地**：未声明隔离、或 isolationRelaxable
// 档下两道闸都合上时为 false。调用方据此判断本次查询有没有被边界收窄。
func (r *Repo[T, ID]) applyIsolation(quer *queryBuilder, policy isolationPolicy) (*queryBuilder, bool, error) {
	if quer == nil {
		return nil, false, nil
	}
	schema := r.accessSchema()
	if schema.isolation.column == "" {
		return quer, false, nil
	}
	if policy == isolationRelaxable {
		relaxed, err := r.crossIsolationRelaxed(quer.ctx, schema)
		if err != nil {
			return nil, false, err
		}
		if relaxed {
			return quer, false, nil
		}
	}
	key, err := r.resolveIsolationKey(quer.ctx, schema)
	if err != nil {
		return nil, false, err
	}
	if key == "" {
		return nil, false, r.isolationKeyMissingError(schema)
	}
	return quer.Where(schema.isolation.column+" = ?", key), true, nil
}

// isolationKeyMissingError 是隔离键解析不到时的 fail-closed 错误。
//
// 错误里同时点出列名与取值来源：只说"缺 isolation key"排查者不知道去哪儿找，
// 只说"缺 tenant"又在自定义 Resolve 时是错的。
func (r *Repo[T, ID]) isolationKeyMissingError(schema dataScopeSchema) error {
	source := "contextx.TenantID (default)"
	if r.isolationResolve != nil {
		source = "the configured IsolationCols.Resolve"
	}
	return errors.NewCode(errors.Forbidden,
		"isolated repository requires an isolation key for this request").
		WithContext("resource_kind", r.resourceKind).
		WithContext("isolation_column", schema.isolation.column).
		WithContext("isolation_source", source)
}

// validateScopeEnforcement 在启用 WithRequireScope 时确认实体具备可用的隔离边界列。
func (r *Repo[T, ID]) validateScopeEnforcement() error {
	if !r.requireScope {
		return nil
	}
	schema := r.accessSchema()
	hasBoundary := false
	if schema.managedScope.column != "" {
		if !schema.managedScope.hasField() {
			return errors.NewCode(errors.InvalidInput,
				"scope-required repository managed scope column must map to an entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", schema.managedScope.column)
		}
		hasBoundary = true
	}
	if schema.isolation.column != "" {
		if !schema.isolation.hasField() {
			return errors.NewCode(errors.InvalidInput,
				"scope-required repository isolation column must map to an entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", schema.isolation.column)
		}
		hasBoundary = true
	}
	if hasBoundary {
		return nil
	}
	return errors.NewCode(errors.InvalidInput,
		"scope-required repository must expose a managed scope or isolation boundary column").
		WithContext("resource_kind", r.resourceKind)
}

func (r *Repo[T, ID]) applyDataScopeToEntity(ctx context.Context, entity T) error {
	schema := r.accessSchema()
	value := reflect.ValueOf(entity)
	if !value.IsValid() {
		return nil
	}
	bounded := false
	key, err := r.resolveIsolationKey(ctx, schema)
	if err != nil {
		return err
	}
	if schema.isolation.column != "" {
		// 与 applyIsolation 保持同一条 fail-closed 口径：读路径解析不到隔离键会
		// Forbidden，写路径若在此放行，落库的就是一行隔离键为空的记录——它对任何
		// 隔离空间都不可见（后续查询恒带 `isolation = 非空值`），成为永久孤儿数据；
		// 一旦有不经本 Repo 的读路径，它还会对所有人可见。
		if key == "" {
			return r.isolationKeyMissingError(schema)
		}
		if err := assignStringConstraintField(value, schema.isolation, key, "isolation"); err != nil {
			return err
		}
		bounded = true
	}
	if schema.managedScope.column == "" {
		if r.requireScope && !bounded {
			return errors.NewCode(errors.Forbidden,
				"scope-required repository has no enforceable data boundary for this request").
				WithContext("resource_kind", r.resourceKind)
		}
		return nil
	}
	scope, err := r.resolveDataScope(ctx)
	if err != nil {
		return err
	}
	if scope.Kind == scoped.ScopeGlobal {
		if r.requireScope && !bounded && !r.allowGlobalScope {
			return r.globalScopeForbiddenError()
		}
		return nil
	}
	visibleScopeIDs := normalizeVisibleScopeIDs(scope)
	if len(visibleScopeIDs) == 0 {
		return errors.NewCode(errors.Forbidden, "data scope denies all access").
			WithContext("resource_kind", r.resourceKind)
	}
	if len(visibleScopeIDs) == 1 {
		return assignInt64ConstraintField(value, schema.managedScope, visibleScopeIDs[0], "managed_scope_id")
	}
	managedScopeID, err := readRequiredInt64ConstraintField(value, schema.managedScope, "managed_scope_id")
	if err != nil {
		return err
	}
	if !containsInt64(visibleScopeIDs, managedScopeID) {
		return errors.NewCode(errors.Forbidden, "managed scope is outside data scope").
			WithContext("managed_scope_id", managedScopeID)
	}
	return nil
}

func (r *Repo[T, ID]) globalScopeForbiddenError() error {
	return errors.NewCode(errors.Forbidden,
		"global data scope is not allowed for scope-required repository").
		WithContext("resource_kind", r.resourceKind)
}

func (r *Repo[T, ID]) resolveDataScope(ctx context.Context) (scoped.DataScope, error) {
	scope, err := scoped.ResolveDataScope(ctx, r.dataScope)
	if err != nil {
		// 三态模型下解析失败即 Forbidden（fail-closed），不再改写错误码。
		return scoped.DenyAll(), err
	}
	return scope, nil
}

func (r *Repo[T, ID]) accessSchema() dataScopeSchema {
	return resolveDataScopeSchema[T](r.accessColumns)
}

func (r *Repo[T, ID]) versionColumn() string {
	schema := r.accessSchema()
	if schema.version.column != "" {
		return schema.version.column
	}
	return "version"
}

// resolveIsolationKey 解析本次请求的隔离键。
//
// 取值来源是**装配期显式决定**的：未配置 Resolve 时默认取 contextx.TenantID
// （绝大多数系统的隔离空间就是租户），配置了则完全由调用方决定。
// 这一层存在的意义是让 IsolationCols.Column 名副其实——列名可换而来源锁死
// 在租户槽位的话，`Column: "org_id"` 就是个假承诺。
func (r *Repo[T, ID]) resolveIsolationKey(ctx context.Context, schema dataScopeSchema) (string, error) {
	if schema.isolation.column == "" {
		return "", nil
	}
	if r.isolationResolve != nil {
		key, err := r.isolationResolve(ctx)
		if err != nil {
			// 解析器自身故障绝不能被当成"无隔离"而放行，原样上抛。
			return "", err
		}
		return strings.TrimSpace(key), nil
	}
	return strings.TrimSpace(contextx.TenantID(ctx)), nil
}

// normalizeVisibleScopeIDs 返回该范围下可见的 scope ID 集合。
//
// 三态模型下规范化已由 DataScope.Normalize 完成，这里只做取值。
func normalizeVisibleScopeIDs(scope scoped.DataScope) []int64 {
	normalized := scope.Normalize()
	if normalized.Kind != scoped.ScopeFiltered {
		return nil
	}
	return normalized.ScopeIDs
}

func containsInt64(values []int64, target int64) bool {
	target = normalizePositiveID(target)
	for _, value := range values {
		if normalizePositiveID(value) == target {
			return true
		}
	}
	return false
}

// normalizePositiveID 在 value 为正数时返回原值，否则返回 0。
func normalizePositiveID(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return value
}

func int64SliceToAny(values []int64) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
