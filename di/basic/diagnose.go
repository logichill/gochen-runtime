package basic

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gochen-runtime/di"
)

// Diagnose 诊断容器状态，输出依赖链路追踪信息。
//
// 用于调试 DI 解析失败，包括：
// - 已注册的服务列表及其状态
// - 依赖图
// - 无法解析的依赖
// - 循环依赖检测
func (c *Container) Diagnose() *di.DiagnoseResult {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	totalServices := len(c.typedServices)
	result := &di.DiagnoseResult{
		RegisteredServices:       make([]di.ServiceInfo, 0, totalServices),
		DependencyGraph:          make(map[string][]string),
		UnresolvableDependencies: make([]di.UnresolvableDep, 0),
		CircularDependencies:     make([][]string, 0),
	}

	// 收集服务信息
	for _, registered := range c.registeredServicesLocked() {
		name := registered.name
		entry := registered.entry
		if entry == nil {
			continue
		}
		info := di.ServiceInfo{
			Name: name,
		}
		entry.mu.Lock()
		info.Created = entry.created
		entry.mu.Unlock()

		if entry.lifetime == lifetimeSingleton {
			info.Lifetime = "singleton"
			info.LifecycleMethods = lifecycleMethods(serviceEntryOutputType(entry))
		} else {
			info.Lifetime = "transient"
		}

		// 分析工厂函数
		if entry.factory != nil {
			ft := reflect.TypeOf(entry.factory)
			if ft.Kind() == reflect.Func {
				// 输出类型
				if serviceType := serviceEntryOutputType(entry); serviceType != nil {
					info.OutputType = serviceType.String()
				}
				// 输入类型（依赖）
				info.InputTypes = make([]string, ft.NumIn())
				deps := make([]string, 0, ft.NumIn())
				for i := 0; i < ft.NumIn(); i++ {
					paramType := ft.In(i)
					info.InputTypes[i] = di.TypeKey(paramType)
					depKey, candidates, ok := c.findRegisteredServiceKeyLocked(paramType)
					if ok {
						deps = append(deps, depKey)
					} else {
						deps = append(deps, di.TypeKey(paramType))
						result.UnresolvableDependencies = append(result.UnresolvableDependencies, di.UnresolvableDep{
							Service:    name,
							ParamIndex: i,
							ParamType:  di.TypeKey(paramType),
							Reason:     fmt.Sprintf("not registered (tried: %v; raw=%s)", candidates, paramType.String()),
						})
					}
				}
				result.DependencyGraph[name] = deps
			} else {
				// 实例类型
				info.OutputType = ft.String()
				result.DependencyGraph[name] = []string{}
			}
		}

		result.RegisteredServices = append(result.RegisteredServices, info)
	}

	sort.Slice(result.RegisteredServices, func(i, j int) bool {
		return result.RegisteredServices[i].Name < result.RegisteredServices[j].Name
	})
	sort.Slice(result.UnresolvableDependencies, func(i, j int) bool {
		if result.UnresolvableDependencies[i].Service != result.UnresolvableDependencies[j].Service {
			return result.UnresolvableDependencies[i].Service < result.UnresolvableDependencies[j].Service
		}
		return result.UnresolvableDependencies[i].ParamIndex < result.UnresolvableDependencies[j].ParamIndex
	})

	// 检测循环依赖
	result.CircularDependencies = c.detectCircularDependenciesLocked(result.DependencyGraph)

	return result
}

// findRegisteredServiceKeyLocked finds the first registered service key that
// would be used by resolveParameter(). Caller must hold c.mutex RLock.
func (c *Container) findRegisteredServiceKeyLocked(paramType reflect.Type) (key string, candidates []string, ok bool) {
	candidates = make([]string, 0, 4)
	if paramType == nil {
		return "", candidates, false
	}
	seen := make(map[string]struct{}, 4)
	addCandidate := func(candidate string) {
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	if _, exists := c.typedServices[paramType]; exists {
		key := di.TypeKey(paramType)
		return key, []string{key}, true
	}

	for serviceType, entry := range c.typedServices {
		if isCompatibleServiceType(paramType, serviceEntryOutputType(entry)) {
			addCandidate(di.TypeKey(serviceType))
		}
	}
	if len(candidates) == 1 {
		return candidates[0], candidates, true
	}
	return "", candidates, false
}

// detectCircularDependenciesLocked 检测循环依赖（调用时需持有读锁）。
func (c *Container) detectCircularDependenciesLocked(graph map[string][]string) [][]string {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := make([]string, 0)
	cycles := make([][]string, 0)
	seen := make(map[string]struct{})

	var dfs func(node string)
	dfs = func(node string) {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		for _, dep := range graph[node] {
			// 依赖可能是类型名，需要找到对应的服务
			serviceName := c.findServiceByTypeLocked(dep)
			if serviceName == "" {
				continue
			}

			if !visited[serviceName] {
				dfs(serviceName)
			} else if recStack[serviceName] {
				// 找到循环
				cycleStart := -1
				for i, p := range path {
					if p == serviceName {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cycle := make([]string, len(path)-cycleStart)
					copy(cycle, path[cycleStart:])
					key := canonicalCycleKey(cycle)
					if _, ok := seen[key]; !ok {
						seen[key] = struct{}{}
						cycles = append(cycles, cycle)
					}
				}
			}
		}

		path = path[:len(path)-1]
		recStack[node] = false
	}

	for name := range graph {
		if !visited[name] {
			dfs(name)
		}
	}

	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i], "->") < strings.Join(cycles[j], "->")
	})
	return cycles
}

func canonicalCycleKey(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}

	// Rotate cycle to a canonical representation to reduce duplicates.
	best := make([]string, len(cycle))
	copy(best, cycle)
	for start := 1; start < len(cycle); start++ {
		rot := make([]string, 0, len(cycle))
		rot = append(rot, cycle[start:]...)
		rot = append(rot, cycle[:start]...)
		if strings.Join(rot, "\x00") < strings.Join(best, "\x00") {
			best = rot
		}
	}
	return strings.Join(best, "->")
}

func lifecycleMethods(t reflect.Type) []string {
	if t == nil {
		return nil
	}
	methods := make([]string, 0, 2)
	for _, name := range []string{"Close", "Dispose"} {
		method, ok := t.MethodByName(name)
		if ok && isLifecycleMethod(t, method.Type) {
			methods = append(methods, name)
		}
	}
	return methods
}

func isLifecycleMethod(owner reflect.Type, method reflect.Type) bool {
	if owner == nil || method == nil {
		return false
	}
	wantInputs := 1
	if owner.Kind() == reflect.Interface {
		wantInputs = 0
	}
	if method.NumIn() != wantInputs {
		return false
	}
	switch method.NumOut() {
	case 0:
		return true
	case 1:
		return method.Out(0).Implements(reflect.TypeOf((*error)(nil)).Elem())
	default:
		return false
	}
}

// findServiceByTypeLocked 根据类型名查找服务名（调用时需持有读锁）。
func (c *Container) findServiceByTypeLocked(typeName string) string {
	for serviceType, entry := range c.typedServices {
		if di.TypeKey(serviceType) == typeName {
			return typeName
		}
		if outputType := serviceEntryOutputType(entry); outputType != nil && outputType.String() == typeName {
			return di.TypeKey(serviceType)
		}
	}
	return ""
}

type registeredService struct {
	name  string
	entry *serviceEntry
}

func (c *Container) registeredServicesLocked() []registeredService {
	out := make([]registeredService, 0, len(c.typedServices))
	for serviceType, entry := range c.typedServices {
		out = append(out, registeredService{name: di.TypeKey(serviceType), entry: entry})
	}
	return out
}

// DiagnoseString 返回诊断结果的可读字符串。
//
// 用于日志输出或调试。
func (c *Container) DiagnoseString() string {
	diag := c.Diagnose()
	var sb strings.Builder
	sb.WriteString("=== DI Container Diagnosis ===\n\n")

	// 已注册服务
	sb.WriteString(fmt.Sprintf("Registered Services (%d):\n", len(diag.RegisteredServices)))
	for _, svc := range diag.RegisteredServices {
		status := "pending"
		if svc.Created {
			status = "created"
		}
		sb.WriteString(fmt.Sprintf("  - %s [%s, %s]\n", svc.Name, svc.Lifetime, status))
		if len(svc.LifecycleMethods) > 0 {
			sb.WriteString(fmt.Sprintf("      lifecycle: %v\n", svc.LifecycleMethods))
		}
		if svc.OutputType != "" && svc.OutputType != svc.Name {
			sb.WriteString(fmt.Sprintf("      output: %s\n", svc.OutputType))
		}
		if len(svc.InputTypes) > 0 {
			sb.WriteString(fmt.Sprintf("      deps: %v\n", svc.InputTypes))
		}
	}

	// 无法解析的依赖
	if len(diag.UnresolvableDependencies) > 0 {
		sb.WriteString(fmt.Sprintf("\nUnresolvable Dependencies (%d):\n", len(diag.UnresolvableDependencies)))
		for _, dep := range diag.UnresolvableDependencies {
			sb.WriteString(fmt.Sprintf("  - %s: param[%d] %s (%s)\n", dep.Service, dep.ParamIndex, dep.ParamType, dep.Reason))
		}
	}

	// 循环依赖
	if len(diag.CircularDependencies) > 0 {
		sb.WriteString(fmt.Sprintf("\nCircular Dependencies (%d):\n", len(diag.CircularDependencies)))
		for _, cycle := range diag.CircularDependencies {
			sb.WriteString(fmt.Sprintf("  - %v\n", cycle))
		}
	}

	if len(diag.UnresolvableDependencies) == 0 && len(diag.CircularDependencies) == 0 {
		sb.WriteString("\nNo issues detected.\n")
	}

	return sb.String()
}
