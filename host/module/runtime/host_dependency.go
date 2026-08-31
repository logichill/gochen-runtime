package runtime

import (
	"sort"

	"gochen-runtime/host/module"
	"gochen/errors"
)

// ensureModuleIDs 确保模块IDs。
func (s *Host) ensureModuleIDs() error {
	if s == nil {
		return nil
	}
	if len(s.modules) == 0 {
		s.moduleIDs = map[module.IModule]string{}
		return nil
	}
	seen := make(map[string]struct{}, len(s.modules))
	mapping := make(map[module.IModule]string, len(s.modules))
	for _, m := range s.modules {
		if m == nil {
			continue
		}
		id, err := module.NormalizeModuleID(m.ID())
		if err != nil {
			return errors.Wrap(err, errors.InvalidInput, "invalid module id").
				WithContext("module", m.Name()).
				WithContext("raw_id", m.ID())
		}
		if _, ok := seen[id]; ok {
			return errors.NewCode(errors.Conflict, "duplicate module id").WithContext("id", id)
		}
		seen[id] = struct{}{}
		mapping[m] = id
	}
	s.moduleIDs = mapping
	return nil
}

// sortModulesByDependencies 排序Modules按Dependencies。
func (s *Host) sortModulesByDependencies() error {
	if s == nil || len(s.modules) == 0 {
		return nil
	}
	if len(s.moduleIDs) == 0 {
		return nil
	}

	modulesByID := make(map[string]module.IModule, len(s.modules))
	originalIndex := make(map[string]int, len(s.modules))
	for i, m := range s.modules {
		if m == nil {
			continue
		}
		id := s.moduleIDs[m]
		if id == "" {
			continue
		}
		modulesByID[id] = m
		originalIndex[id] = i
	}
	deps := make(map[string][]string, len(modulesByID))
	dependents := make(map[string][]string, len(modulesByID))
	indegree := make(map[string]int, len(modulesByID))
	for id := range modulesByID {
		indegree[id] = 0
	}

	for _, m := range s.modules {
		if m == nil {
			continue
		}
		id := s.moduleIDs[m]
		if id == "" {
			continue
		}

		provider, ok := m.(module.IModuleDependencyProvider)
		if !ok || provider == nil {
			continue
		}

		rawDeps := provider.DependsOn()
		if len(rawDeps) == 0 {
			continue
		}

		seen := make(map[string]struct{}, len(rawDeps))
		for _, raw := range rawDeps {
			depID, err := module.NormalizeModuleID(raw)
			if err != nil {
				return errors.Wrap(err, errors.InvalidInput, "invalid module dependency id").
					WithContext("module", id).
					WithContext("depends_on_raw", raw)
			}
			if depID == id {
				return errors.NewCode(errors.InvalidInput, "module cannot depend on itself").
					WithContext("module", id).
					WithContext("depends_on", depID)
			}
			if _, exists := modulesByID[depID]; !exists {
				return errors.NewCode(errors.InvalidInput, "module dependency not found").
					WithContext("module", id).
					WithContext("depends_on", depID)
			}
			if _, dup := seen[depID]; dup {
				continue
			}
			seen[depID] = struct{}{}
			deps[id] = append(deps[id], depID)
		}
	}

	for id, ds := range deps {
		indegree[id] = len(ds)
		for _, dep := range ds {
			dependents[dep] = append(dependents[dep], id)
		}
	}

	ready := make([]string, 0, len(modulesByID))
	for id, deg := range indegree {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return originalIndex[ready[i]] < originalIndex[ready[j]] })

	order := make([]string, 0, len(modulesByID))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)

		for _, child := range dependents[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Slice(ready, func(i, j int) bool { return originalIndex[ready[i]] < originalIndex[ready[j]] })
			}
		}
	}

	if len(order) != len(modulesByID) {
		remain := make([]string, 0, len(modulesByID))
		for id, deg := range indegree {
			if deg > 0 {
				remain = append(remain, id)
			}
		}
		sort.Slice(remain, func(i, j int) bool { return originalIndex[remain[i]] < originalIndex[remain[j]] })
		return errors.NewCode(errors.InvalidInput, "module dependency cycle detected").WithContext("modules", remain)
	}

	sorted := make([]module.IModule, 0, len(order))
	for _, id := range order {
		if m := modulesByID[id]; m != nil {
			sorted = append(sorted, m)
		}
	}
	s.modules = sorted
	return nil
}

// normalizeModuleHTTPConfigKeys 规范化模块HTTP配置Keys。
