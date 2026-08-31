package di

import "reflect"

// DiagnoseResult 表示容器诊断结果。
type DiagnoseResult struct {
	// RegisteredServices 已注册的服务列表。
	RegisteredServices []ServiceInfo
	// DependencyGraph 依赖图，key 为服务名，value 为依赖列表。
	DependencyGraph map[string][]string
	// UnresolvableDependencies 表示无法解析的依赖。
	UnresolvableDependencies []UnresolvableDep
	// CircularDependencies 表示检测到的循环依赖路径。
	CircularDependencies [][]string
}

// ServiceInfo 表示已注册服务的诊断信息。
type ServiceInfo struct {
	Name       string
	Lifetime   string
	Created    bool
	OutputType string
	InputTypes []string
	// LifecycleMethods 标记 singleton 输出类型暴露的可选资源释放方法。
	LifecycleMethods []string
}

// UnresolvableDep 表示无法解析的单个依赖参数。
type UnresolvableDep struct {
	Service    string
	ParamIndex int
	ParamType  string
	Reason     string
}

// IDiagnoser 表示容器诊断能力。
type IDiagnoser interface {
	Diagnose() *DiagnoseResult
	DiagnoseString() string
}

// Diagnose 从目标对象读取诊断快照。
//
// 该 helper 让 Host/组合根在启动期显式使用诊断能力，而不是把诊断方法放入常规
// IContainer 公共面；目标未实现 IDiagnoser 时返回 (nil, false)。
func Diagnose(target any) (*DiagnoseResult, bool) {
	if target == nil {
		return nil, false
	}
	rv := reflect.ValueOf(target)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if rv.IsNil() {
			return nil, false
		}
	}
	diagnoser, ok := target.(IDiagnoser)
	if !ok {
		return nil, false
	}
	return diagnoser.Diagnose(), true
}
