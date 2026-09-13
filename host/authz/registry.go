package authz

import (
	"reflect"
	"sort"
	"strings"
	"sync"

	"gochen-runtime/internal/normalize"
	"gochen/auth/action"
	"gochen/auth/scoped"
	"gochen/errors"
)

// ModuleRegistration 定义单个模块向 auth 注册表暴露的授权元数据。

// 一个模块可以同时声明权限码、标准权限定义以及资源解析器；注册表会把这些信息
// 合并成全局目录，并在冲突时拒绝后写入的一方。
type ModuleRegistration struct {
	ModuleID              string
	ModuleName            string
	Permissions           []string
	PermissionDefinitions []PermissionDefinition
	ResourceResolvers     []scoped.ITypedResourceResolver
}

type resolverRegistration struct {
	moduleID string
	resolver scoped.ITypedResourceResolver
}

// Registry 聚合所有模块的权限目录与资源解析器。
type Registry struct {
	mu             sync.RWMutex
	modules        map[string]ModuleRegistration
	permissions    map[string]string
	resolverTypes  map[reflect.Type]string
	resolvers      []resolverRegistration
	catalogHooks   []ModuleCatalogSyncFunc
	catalogModules map[string]moduleCatalogSnapshot
}

// NewRegistry 创建空的 auth 注册表。
func NewRegistry() *Registry {
	return &Registry{
		modules:       make(map[string]ModuleRegistration),
		permissions:   make(map[string]string),
		resolverTypes: make(map[reflect.Type]string),
	}
}

// RegisterModule 合并一个模块的授权目录，并阻止跨模块的权限码/解析器类型冲突。
//
// 同一模块可多次注册，权限与解析器会按“去重后合并”的方式累积；但若不同模块
// 声明了同一权限码，或为同一目标类型注册了解析器，则会直接返回冲突错误。
func (r *Registry) RegisterModule(reg ModuleRegistration) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "authz registry is nil")
	}

	reg, err := normalizeModuleRegistration(reg)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.modules[reg.ModuleID]; ok {
		if current.ModuleName != reg.ModuleName {
			return errors.NewCode(errors.Conflict, "authz module name conflict").
				WithContext("module_id", reg.ModuleID).
				WithContext("current_name", current.ModuleName).
				WithContext("incoming_name", reg.ModuleName)
		}
	}

	for _, permission := range reg.Permissions {
		if owner, ok := r.permissions[permission]; ok && owner != reg.ModuleID {
			return errors.NewCode(errors.Conflict, "permission already registered by another module").
				WithContext("permission", permission).
				WithContext("current_module", owner).
				WithContext("incoming_module", reg.ModuleID)
		}
	}

	for _, resolver := range reg.ResourceResolvers {
		targetType := resolver.TargetType()
		if owner, ok := r.resolverTypes[targetType]; ok && owner != reg.ModuleID {
			return errors.NewCode(errors.Conflict, "resource resolver target already registered by another module").
				WithContext("target_type", targetType.String()).
				WithContext("current_module", owner).
				WithContext("incoming_module", reg.ModuleID)
		}
	}

	current := r.modules[reg.ModuleID]
	current.ModuleID = reg.ModuleID
	current.ModuleName = reg.ModuleName
	current.Permissions = mergeStrings(current.Permissions, reg.Permissions)
	current.PermissionDefinitions = MergePermissionDefinitions(current.PermissionDefinitions, reg.PermissionDefinitions)
	current.ResourceResolvers = mergeTypedResolvers(current.ResourceResolvers, reg.ResourceResolvers)
	r.modules[reg.ModuleID] = current

	for _, permission := range reg.Permissions {
		r.permissions[permission] = reg.ModuleID
	}
	for _, resolver := range reg.ResourceResolvers {
		targetType := resolver.TargetType()
		r.resolverTypes[targetType] = reg.ModuleID
	}
	r.rebuildResolversLocked()

	return nil
}

// Resolve 按模块 ID 排序后的稳定顺序依次尝试资源解析器，并返回首个命中结果。
func (r *Registry) Resolve(target any) (scoped.Resource, bool) {
	if r == nil {
		return scoped.Resource{}, false
	}

	r.mu.RLock()
	resolvers := append([]resolverRegistration(nil), r.resolvers...)
	r.mu.RUnlock()

	for _, entry := range resolvers {
		resource, ok := entry.resolver.Resolve(target)
		if !ok {
			continue
		}
		return resource.Normalize(), true
	}
	return scoped.Resource{}, false
}

// ResourceRegistry 将 Registry 的解析器投影为 scoped.ResourceRegistry。
func (r *Registry) ResourceRegistry() *scoped.ResourceRegistry {
	if r == nil {
		return scoped.NewResourceRegistry()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	resolvers := make([]scoped.IResourceResolver, 0, len(r.resolvers))
	for _, reg := range r.resolvers {
		resolvers = append(resolvers, reg.resolver)
	}
	return scoped.NewResourceRegistry(resolvers...)
}

func (r *Registry) Permissions() []string {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.permissions))
	for permission := range r.permissions {
		out = append(out, permission)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Module(moduleID string) (ModuleRegistration, bool) {
	if r == nil {
		return ModuleRegistration{}, false
	}

	moduleID = strings.TrimSpace(moduleID)
	if moduleID == "" {
		return ModuleRegistration{}, false
	}

	r.mu.RLock()
	current, ok := r.modules[moduleID]
	r.mu.RUnlock()
	if !ok {
		return ModuleRegistration{}, false
	}
	return cloneModuleRegistration(current), true
}

// Modules 返回全部模块的注册目录快照，并按模块 ID 排序。
func (r *Registry) Modules() []ModuleRegistration {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	moduleIDs := make([]string, 0, len(r.modules))
	for moduleID := range r.modules {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)

	out := make([]ModuleRegistration, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		out = append(out, cloneModuleRegistration(r.modules[moduleID]))
	}
	return out
}

// normalizeModuleRegistration 归一化模块注册内容，并补齐从权限码推导出的标准定义。
func normalizeModuleRegistration(reg ModuleRegistration) (ModuleRegistration, error) {
	reg.ModuleID = strings.TrimSpace(reg.ModuleID)
	reg.ModuleName = strings.TrimSpace(reg.ModuleName)
	if reg.ModuleID == "" {
		return ModuleRegistration{}, errors.NewCode(errors.InvalidInput, "authz module id is required")
	}
	if reg.ModuleName == "" {
		return ModuleRegistration{}, errors.NewCode(errors.InvalidInput, "authz module name is required").
			WithContext("module_id", reg.ModuleID)
	}
	reg.Permissions = normalize.Strings(reg.Permissions)
	reg.PermissionDefinitions = MergePermissionDefinitions(
		PermissionDefinitionsFromCodes(reg.Permissions...),
		reg.PermissionDefinitions,
	)
	reg.Permissions = PermissionCodes(reg.PermissionDefinitions...)
	for _, code := range reg.Permissions {
		if !action.IsValidCode(code) {
			return ModuleRegistration{}, errors.NewCode(errors.InvalidInput, "invalid permission code").
				WithContext("module_id", reg.ModuleID).
				WithContext("permission", code)
		}
	}

	if len(reg.ResourceResolvers) > 0 {
		resolvers := make([]scoped.ITypedResourceResolver, 0, len(reg.ResourceResolvers))
		for _, resolver := range reg.ResourceResolvers {
			if resolver == nil {
				continue
			}
			targetType := resolver.TargetType()
			if targetType == nil {
				return ModuleRegistration{}, errors.NewCode(errors.InvalidInput, "resource resolver target type is nil").
					WithContext("module_id", reg.ModuleID)
			}
			resolvers = append(resolvers, resolver)
		}
		reg.ResourceResolvers = resolvers
	}

	return reg, nil
}

// mergeStrings 合并两个字符串集合，去空、去重并保持排序稳定。
func mergeStrings(current []string, incoming []string) []string {
	if len(incoming) == 0 {
		return append([]string(nil), current...)
	}
	seen := make(map[string]struct{}, len(current)+len(incoming))
	out := make([]string, 0, len(current)+len(incoming))
	for _, value := range current {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, value := range incoming {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneModuleRegistration(reg ModuleRegistration) ModuleRegistration {
	return ModuleRegistration{
		ModuleID:              reg.ModuleID,
		ModuleName:            reg.ModuleName,
		Permissions:           append([]string(nil), reg.Permissions...),
		PermissionDefinitions: clonePermissionDefinitions(reg.PermissionDefinitions),
		ResourceResolvers:     append([]scoped.ITypedResourceResolver(nil), reg.ResourceResolvers...),
	}
}

// clonePermissionDefinitions 深拷贝权限定义，避免调用方修改内部切片。
func clonePermissionDefinitions(definitions []PermissionDefinition) []PermissionDefinition {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]PermissionDefinition, 0, len(definitions))
	for _, definition := range definitions {
		definition.Scopes = append([]string(nil), definition.Scopes...)
		out = append(out, definition)
	}
	return out
}

// mergeTypedResolvers 以目标类型为键合并解析器，后者覆盖前者。
func mergeTypedResolvers(current []scoped.ITypedResourceResolver, incoming []scoped.ITypedResourceResolver) []scoped.ITypedResourceResolver {
	typeMap := make(map[reflect.Type]scoped.ITypedResourceResolver, len(current)+len(incoming))
	for _, resolver := range current {
		if resolver == nil || resolver.TargetType() == nil {
			continue
		}
		typeMap[resolver.TargetType()] = resolver
	}
	for _, resolver := range incoming {
		if resolver == nil || resolver.TargetType() == nil {
			continue
		}
		typeMap[resolver.TargetType()] = resolver
	}

	targetTypes := make([]reflect.Type, 0, len(typeMap))
	for targetType := range typeMap {
		targetTypes = append(targetTypes, targetType)
	}
	sort.Slice(targetTypes, func(i, j int) bool {
		return targetTypes[i].String() < targetTypes[j].String()
	})

	out := make([]scoped.ITypedResourceResolver, 0, len(targetTypes))
	for _, targetType := range targetTypes {
		out = append(out, typeMap[targetType])
	}
	return out
}

// rebuildResolversLocked 重建按模块顺序展开的 resolver 列表，供 Resolve 快速遍历。
func (r *Registry) rebuildResolversLocked() {
	moduleIDs := make([]string, 0, len(r.modules))
	for moduleID := range r.modules {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)

	resolvers := make([]resolverRegistration, 0)
	for _, moduleID := range moduleIDs {
		current := r.modules[moduleID]
		for _, resolver := range current.ResourceResolvers {
			if resolver == nil {
				continue
			}
			resolvers = append(resolvers, resolverRegistration{
				moduleID: moduleID,
				resolver: resolver,
			})
		}
	}
	r.resolvers = resolvers
}
