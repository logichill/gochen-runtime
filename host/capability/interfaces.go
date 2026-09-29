package capability

import (
	"context"

	"gochen-runtime/host/module"
	"gochen/domain/eventsourced"
	"gochen/eventing/projection"
	"gochen/messaging"
)

// IEventSubscriber 表示模块事件处理器所需的订阅能力。
type IEventSubscriber interface {
	Subscribe(ctx context.Context, messageType string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error)
}

// ITransport 表示 Host 负责启动/停止并注入模块的消息传输能力。
//
// Host 仅停止由本次启动取得的实例；可观测到启动前已运行的实例仍由原所有者管理。
// 无法观测状态时，Start 成功即由 Host 接管；清理失败时保留所有权，供 Shutdown 重试。
type ITransport interface {
	IEventSubscriber
	// Start 失败时，实现必须清理本次启动已创建的资源并保持可重试状态。
	// Host 只会对同时实现完整 messaging.ITransport 且可观测到“本次启动从
	// stopped 变为 running”的实现自动执行回滚；无法提供该状态观测的实现
	// 启动失败后不会被 Host 无条件 Stop，以避免误停启动前已运行的传输层。
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// IProjectionRuntime 表示模块投影注册与启动所需的运行期能力。
//
// 注册必须返回绑定本次注册的释放函数，生命周期结束时调用；
// Host 不按名字清理资源，避免误删其他所有者的同名注册。
type IProjectionRuntime interface {
	projection.IProjectionRegistrar
	StartProjection(name string) error
}

// IModuleCatalogRegistrar 表示 Host 在模块装配完成后回调的**可选**目录登记能力。
//
// 设计意图：Host 基础容器不认识任何具体的安全或治理语义。
// 需要收集模块目录的扩展（例如 runtime/host/authz 的权限目录）
// 实现本接口并经 config.WithCatalogRegistrar 注入；不注入时 Host 完全不做目录登记，
// 也不会把相关包编译进依赖图。
type IModuleCatalogRegistrar interface {
	RegisterModuleCatalog(ctx context.Context, entries []ModuleCatalogEntry) error
}

// ModuleCatalogEntry 描述一个已装配模块的目录登记条目。
type ModuleCatalogEntry struct {
	// ID 是 Host 解析后的模块稳定标识（优先于模块自报 ID）。
	ID string

	// Module 是模块实例，registrar 可按需类型断言其可选扩展接口。
	Module module.IModule
}

// IModuleRegistry 表示 Host 维护模块目录所需的最小能力。
//
// 说明：module.ModuleInfo 是模块目录值对象快照，用于管理界面、调试接口和文档目录。
type IModuleRegistry interface {
	Register(module.ModuleInfo) error
	Modules() []module.ModuleInfo
}

// IMetadataRegistry 表示 Host 模块装配所需的事件溯源聚合 metadata 注册能力。
//
// 说明：eventsourced.MetadataRegistration 是启动期注册描述值对象，承载 sample 与 aggregate type。
type IMetadataRegistry interface {
	RegisterSet(registrations ...eventsourced.MetadataRegistration) error
}

// IProjectionResumer 表示投影支持从持久化 checkpoint 主动追赶 backlog。
type IProjectionResumer interface {
	ResumeFromCheckpoint(ctx context.Context, name string) error
}

// IRuntimeComponent 定义需要由模块生命周期托管启动的运行期组件。
type IRuntimeComponent interface {
	Start(ctx context.Context) error
}

// IRuntimeStopper 表示运行期组件支持优雅停止。Stop 可能面对部分初始化；
// 失败资源应由实现保留并允许框架后续重试。
type IRuntimeStopper interface {
	Stop(ctx context.Context) error
}

// IMessageTypesProvider 允许处理器声明自己关心的多个消息类型。
type IMessageTypesProvider interface {
	EventTypes() []string
}
