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
type ITransport interface {
	IEventSubscriber
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// IProjectionRuntime 表示模块投影注册与启动所需的运行期能力。
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

// IProjectionStopper 表示投影支持显式停止。
type IProjectionStopper interface {
	StopProjection(name string) error
}

// IProjectionUnregister 表示投影支持从 manager 中解除注册。
type IProjectionUnregister interface {
	UnregisterProjectionWithContext(ctx context.Context, name string) error
}

// IRuntimeComponent 定义需要由模块生命周期托管启动的运行期组件。
type IRuntimeComponent interface {
	Start(ctx context.Context) error
}

// IRuntimeStopper 表示运行期组件支持优雅停止。
type IRuntimeStopper interface {
	Stop(ctx context.Context) error
}

// IMessageTypesProvider 允许处理器声明自己关心的多个消息类型。
type IMessageTypesProvider interface {
	EventTypes() []string
}
