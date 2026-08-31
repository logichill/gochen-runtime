package module

import (
	"reflect"

	"gochen-runtime/di"
	"gochen/errors"
)

// IModuleContainer 表示模块运行期需要的容器能力集合。
//
// 说明：
// - Registry：用于注册显式 providers；
// - ConstructorRegistry/Invoker/Introspector：用于框架装配层高级能力；
// - Resolver：用于模块运行期按类型解析依赖。
type IModuleContainer interface {
	di.IRegistry
	di.IConstructorRegistry
	di.IResolver
	di.IInvoker
	di.IIntrospector
}

// ResolveModuleContainer 解析模块Container。
func ResolveModuleContainer(opts ModuleInitOptions) (IModuleContainer, error) {
	if opts.container == nil || isTypedNil(opts.container) {
		return nil, errors.NewCode(errors.Internal, "module requires framework container capabilities")
	}
	return opts.container, nil
}

func isTypedNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
