// Package di 提供依赖注入公共契约。
//
// 容器只服务组合根、Host 和框架装配阶段；业务运行期应通过构造函数参数接收依赖，
// 不应把容器作为 Service Locator 传入业务对象。
package di

import "reflect"

// IRegistry 表示 DI 显式注册能力。
type IRegistry interface {
	// RegisterSingleton 按类型注册单例工厂。
	RegisterSingleton(serviceType reflect.Type, factory Factory) error

	// RegisterTransient 按类型注册瞬态工厂。
	RegisterTransient(serviceType reflect.Type, factory Factory) error

	// RegisterInstance 按类型注册实例。
	RegisterInstance(serviceType reflect.Type, instance Instance) error
}

// IConstructorRegistry 表示按构造函数返回值自动注册服务的高级能力。
//
// 该接口主要供组合根、Host 与框架装配层使用；普通业务代码优先使用显式
// RegisterSingleton/RegisterTransient/RegisterInstance 注册。
type IConstructorRegistry interface {
	// RegisterConstructor 按构造函数返回值自动注册服务。
	RegisterConstructor(constructor Constructor) error
}

// IResolver 表示 DI 按类型解析能力。
type IResolver interface {
	// Resolve 按类型解析依赖。
	Resolve(serviceType reflect.Type) (any, error)

	// IsRegistered 检查某个类型是否已注册。
	IsRegistered(serviceType reflect.Type) bool
}

// IIntrospector 表示 DI 注册快照内省能力。
//
// 该接口主要供 Host/框架做按能力类型扫描与启动期诊断；普通业务运行期不应依赖
// 注册表快照来实现 Service Locator 行为。
type IIntrospector interface {
	// ListRegisteredTypes 返回当前容器中已注册的服务类型快照（name -> reflect.Type）。
	//
	// 说明：
	// - 该方法不得实例化任何对象；
	// - 返回值必须为非 nil map（即使为空也应返回 make(map[string]reflect.Type)）；
	// - key 使用 TypeKey(reflect.Type)；
	// - 用途是供上层做“按能力类型”的安全扫描，避免 Resolve 全量服务。
	ListRegisteredTypes() map[string]reflect.Type
}

// IInvoker 表示按函数参数自动注入并调用的高级能力。
//
// 该接口主要供组合根、Host 与框架装配/启动阶段使用；业务运行期应通过构造函数
// 或显式依赖传递获得对象，不应把 invoker 当作运行期 Service Locator。
type IInvoker interface {
	// Invoke 调用函数并按参数类型注入。
	Invoke(invocation Invocation) error
}

// IContainer 表示组合根与 Host 可持有的完整 DI 容器能力。
//
// 默认基础容器实现位于 gochen/di/basic。
type IContainer interface {
	IRegistry
	IConstructorRegistry
	IResolver
	IIntrospector
	IInvoker

	// Clear 清空容器。
	Clear()
}
