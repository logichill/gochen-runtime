package authz

import (
	"gochen/errors"
	"sort"
)

// ModuleCatalogSyncFunc 定义模块 authz 目录同步钩子。
type ModuleCatalogSyncFunc func(ModuleRegistration) error

type moduleCatalogSnapshot struct {
	registration ModuleRegistration
	revision     uint64
}

func cloneModuleCatalogRegistrations(registrations map[string]moduleCatalogSnapshot) []moduleCatalogSnapshot {
	if len(registrations) == 0 {
		return nil
	}

	moduleIDs := make([]string, 0, len(registrations))
	for moduleID := range registrations {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)

	out := make([]moduleCatalogSnapshot, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		snapshot := registrations[moduleID]
		out = append(out, moduleCatalogSnapshot{
			registration: cloneModuleRegistration(snapshot.registration),
			revision:     snapshot.revision,
		})
	}
	return out
}

// InstallModuleCatalogSync 安装当前 Registry 实例的模块 authz 目录同步钩子。
func (r *Registry) InstallModuleCatalogSync(fn ModuleCatalogSyncFunc) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "authz registry is nil")
	}

	r.mu.Lock()
	if fn == nil {
		r.catalogHooks = nil
		r.catalogModules = nil
		r.mu.Unlock()
		return nil
	}
	r.catalogHooks = append(r.catalogHooks, fn)
	modules := cloneModuleCatalogRegistrations(r.catalogModules)
	r.mu.Unlock()

	var errs []error
	for _, snapshot := range modules {
		if !r.moduleCatalogRevisionMatches(snapshot.registration.ModuleID, snapshot.revision) {
			continue
		}
		if err := fn(snapshot.registration); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Registry) rememberModuleCatalogLocked(reg ModuleRegistration) {
	if r == nil || reg.ModuleID == "" {
		return
	}
	if r.catalogModules == nil {
		r.catalogModules = make(map[string]moduleCatalogSnapshot)
	}
	snapshot := r.catalogModules[reg.ModuleID]
	snapshot.registration = cloneModuleRegistration(reg)
	snapshot.revision++
	r.catalogModules[reg.ModuleID] = snapshot
}

func (r *Registry) moduleCatalogRevisionMatches(moduleID string, revision uint64) bool {
	if r == nil || moduleID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot, ok := r.catalogModules[moduleID]
	if !ok {
		return false
	}
	return snapshot.revision == revision
}

// SyncModuleCatalog 将模块 authz 目录同步到当前 Registry 实例的治理钩子。
func (r *Registry) SyncModuleCatalog(reg ModuleRegistration) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "authz registry is nil")
	}
	r.mu.Lock()
	r.rememberModuleCatalogLocked(reg)
	hooks := append([]ModuleCatalogSyncFunc(nil), r.catalogHooks...)
	r.mu.Unlock()
	if len(hooks) == 0 {
		return nil
	}

	var errs []error
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if err := hook(reg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
