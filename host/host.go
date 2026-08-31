package host

import (
	"context"

	"gochen-runtime/host/config"
	"gochen-runtime/host/engine"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen/domain/eventsourced"
)

// IModule 定义 Host 可管理的基础模块生命周期。
type IModule = module.IModule

// Option 是 Host、模块运行时与 Engine 共用的配置选项。
type Option = config.Option

// New 创建标准模块化 Host 的生命周期引擎。
func New(opts ...Option) (*engine.Engine, error) {
	cfg := config.Build(opts...)
	app := runtime.NewHostConfig(cfg)
	return engine.NewEngineConfig(app, cfg)
}

// Run 创建标准模块化 Host 并执行完整生命周期。
func Run(ctx context.Context, opts ...Option) error {
	runner, err := New(opts...)
	if err != nil {
		return err
	}
	return runner.Start(ctx)
}

// NewEngine 创建自定义应用的生命周期引擎。
func NewEngine(app engine.IApp, opts ...Option) (*engine.Engine, error) {
	return engine.NewEngineConfig(app, config.Build(opts...))
}

// NewModuleRegistry 创建模块目录注册表。
func NewModuleRegistry() *module.ModuleRegistry {
	return module.NewModuleRegistry()
}

// NewMetadataRegistry 创建事件溯源聚合 metadata 注册表。
func NewMetadataRegistry() *eventsourced.MetadataRegistry {
	return eventsourced.NewMetadataRegistry()
}
