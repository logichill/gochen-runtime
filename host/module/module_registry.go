package module

import (
	"slices"
	"sort"
	"strings"
	"sync"

	"gochen/errors"
)

// ModuleInfo 表示对外暴露的模块目录项。
//
// 它只保留稳定标识与展示名称，适合给管理界面、调试接口或文档目录使用。
type ModuleInfo struct {
	ID       string
	Name     string
	Features []PublicFeature
}

// ModuleRegistry 维护一份按模块 ID 去重的模块目录。
type ModuleRegistry struct {
	mu      sync.RWMutex
	modules map[string]ModuleInfo
}

// NewModuleRegistry 创建空的模块目录注册表。
func NewModuleRegistry() *ModuleRegistry {
	return &ModuleRegistry{
		modules: make(map[string]ModuleInfo),
	}
}

// Register 注册模块目录项，并校验同一 ID 下名称是否稳定。
//
// 重复注册同一组 `ID/Name` 会被视为幂等；只有同 ID 对应不同 Name 时才会报冲突。
func (r *ModuleRegistry) Register(info ModuleInfo) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "module registry is nil")
	}

	id, err := NormalizeModuleID(info.ID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid module id")
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		return errors.NewCode(errors.InvalidInput, "module name cannot be empty").WithContext("id", id)
	}
	features, err := normalizePublicFeatures(info.Features)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid module public features").WithContext("id", id)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.modules[id]; ok {
		if current.Name != name || !slices.Equal(current.Features, features) {
			return errors.NewCode(errors.Conflict, "module registry entry conflict").
				WithContext("id", id).
				WithContext("current_name", current.Name).
				WithContext("incoming_name", name)
		}
		return nil
	}

	r.modules[id] = ModuleInfo{ID: id, Name: name, Features: features}
	return nil
}

// Modules 返回按模块 ID 排序后的目录快照。
func (r *ModuleRegistry) Modules() []ModuleInfo {
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

	out := make([]ModuleInfo, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		info := r.modules[moduleID]
		info.Features = append([]PublicFeature(nil), info.Features...)
		out = append(out, info)
	}
	return out
}
