// Package authz 提供 Host 的**可选**授权目录扩展。
//
// 设计意图：Host 基础容器（runtime/host、host/config、
// host/module、host/capability）对授权语义零认知、零编译依赖。
// 只有显式装配本包的应用才会把 gochen/auth 引入依赖图。
//
// 装配方式：
//
//	registry := auth.NewRegistry()
//	svc := host.New(
//	    host.WithModules(orderModule),
//	    config.WithCatalogRegistrar(authz.NewCatalogRegistrar(registry)),
//	)
//
// 模块侧声明：
//
//	host.Module("order").Extension(authz.Catalog{
//	    Permissions: []string{"order:api:read"},
//	})
package authz

import (
	"context"

	"gochen-runtime/host/capability"
	"gochen/errors"
)

// IAuthzRegistry 表示授权目录的登记能力。
//
// 说明：ModuleRegistration 与 authscoped.Resource 是跨模块注册目录的值对象快照，
// 用于在 Host 和治理钩子之间传递不可变目录数据，不代表依赖具体 registry 实现。
type IAuthzRegistry interface {
	RegisterModule(ModuleRegistration) error
	Module(moduleID string) (ModuleRegistration, bool)
	Modules() []ModuleRegistration
	Permissions() []string
	InstallModuleCatalogSync(ModuleCatalogSyncFunc) error
	SyncModuleCatalog(ModuleRegistration) error
}

// IModuleAuthzProvider 暴露模块声明式 authz 目录。
//
// 模块既可以实现本接口，也可以通过 Catalog 扩展载荷声明目录；
// 两者都被 CatalogRegistrar 识别，实现本接口时优先。
type IModuleAuthzProvider interface {
	AuthzRegistration() ModuleRegistration
}

// Catalog 是模块权限目录的声明式扩展载荷。
//
// 经 host.Builder.Extension(...) 附加到模块描述符，由 CatalogRegistrar 收集。
type Catalog struct {
	// Permissions 模块声明的权限码目录（三段式 `resource:type:action`）。
	Permissions []string

	// PermissionDefinitions 模块声明的权限元数据目录。
	PermissionDefinitions []PermissionDefinition
}

// IsEmpty 判断目录是否没有任何声明。
func (c Catalog) IsEmpty() bool {
	return len(c.Permissions) == 0 &&
		len(c.PermissionDefinitions) == 0
}

// catalogRegistrar 把模块声明的权限目录登记进 IAuthzRegistry。
type catalogRegistrar struct {
	registry IAuthzRegistry
}

var _ capability.IModuleCatalogRegistrar = (*catalogRegistrar)(nil)

// NewCatalogRegistrar 创建授权目录登记器，经 config.WithCatalogRegistrar 注入 Host。
func NewCatalogRegistrar(registry IAuthzRegistry) capability.IModuleCatalogRegistrar {
	if registry == nil {
		return nil
	}
	return &catalogRegistrar{registry: registry}
}

// RegisterModuleCatalog 收集各模块声明的权限目录并同步进注册表。
func (r *catalogRegistrar) RegisterModuleCatalog(_ context.Context, entries []capability.ModuleCatalogEntry) error {
	if r == nil || r.registry == nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Module == nil {
			continue
		}
		reg, ok := registrationFor(entry)
		if !ok {
			continue
		}
		if err := r.registry.RegisterModule(reg); err != nil {
			return err
		}
		snapshot, ok := r.registry.Module(reg.ModuleID)
		if !ok {
			return errors.NewCode(errors.Internal, "registered authz module snapshot not found").
				WithContext("module_id", reg.ModuleID)
		}
		if err := r.registry.SyncModuleCatalog(snapshot); err != nil {
			return err
		}
	}
	return nil
}

// registrationFor 解析单个模块的授权目录声明。
//
// 优先使用模块自身实现的 IModuleAuthzProvider；否则聚合其 Catalog 扩展载荷。
func registrationFor(entry capability.ModuleCatalogEntry) (ModuleRegistration, bool) {
	moduleID := entry.ID
	if moduleID == "" {
		moduleID = entry.Module.ID()
	}

	if provider, ok := entry.Module.(IModuleAuthzProvider); ok {
		reg := provider.AuthzRegistration()
		reg.ModuleID = moduleID
		if len(reg.Permissions) == 0 && len(reg.PermissionDefinitions) == 0 {
			return ModuleRegistration{}, false
		}
		return reg, true
	}

	catalog, ok := catalogFrom(entry)
	if !ok {
		return ModuleRegistration{}, false
	}

	definitions := MergePermissionDefinitions(
		PermissionDefinitionsFromCodes(catalog.Permissions...),
		catalog.PermissionDefinitions,
	)
	return ModuleRegistration{
		ModuleID:              moduleID,
		ModuleName:            entry.Module.Name(),
		Permissions:           PermissionCodes(definitions...),
		PermissionDefinitions: definitions,
	}, true
}

// catalogFrom 聚合模块扩展载荷中的全部 Catalog 声明。
func catalogFrom(entry capability.ModuleCatalogEntry) (Catalog, bool) {
	provider, ok := entry.Module.(interface{ ModuleExtensions() []any })
	if !ok {
		return Catalog{}, false
	}
	var merged Catalog
	for _, ext := range provider.ModuleExtensions() {
		catalog, ok := ext.(Catalog)
		if !ok {
			continue
		}
		merged.Permissions = append(merged.Permissions, catalog.Permissions...)
		merged.PermissionDefinitions = append(merged.PermissionDefinitions, catalog.PermissionDefinitions...)
	}
	if merged.IsEmpty() {
		return Catalog{}, false
	}
	return merged, true
}
