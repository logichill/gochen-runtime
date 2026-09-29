package runtime

import (
	"sync/atomic"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/bootstrap"
	"gochen-runtime/host/module"
)

const (
	routeRegistrationPending uint32 = iota
	routeRegistrationInProgress
	routeRegistrationComplete
	routeRegistrationFailed
)

// Host 承载一组模块的轻量服务实现。
type Host struct {
	config    *config.Config
	ctors     []module.ModuleCtor
	modules   []module.IModule
	container di.IContainer

	runtime           *bootstrap.Runtime
	catalogRegistrars []capability.IModuleCatalogRegistrar
	moduleRegistry    capability.IModuleRegistry
	metadataRegistry  capability.IMetadataRegistry

	moduleStops       []module.ModuleStopFunc
	transportOwned    bool // 本 Host 启动取得的 Transport；清理失败时保留所有权。
	backgroundStarted atomic.Bool
	routeRegistration atomic.Uint32

	moduleIDs map[module.IModule]string
}

// NewHost 创建承载模块集合的轻量服务。
func NewHost(ctors []module.ModuleCtor, opts ...config.Option) *Host {
	cfg := config.Build(opts...)
	cfg.Modules = append([]module.ModuleCtor(nil), ctors...)
	return NewHostConfig(cfg)
}

// NewHostConfig 从已构建的共享配置创建模块 Host。
func NewHostConfig(cfg *config.Config) *Host {
	cfg = config.Normalize(cfg)

	return &Host{
		config:    cfg,
		ctors:     append([]module.ModuleCtor(nil), cfg.Modules...),
		container: cfg.Container,
	}
}

func (s *Host) Name() string {
	if s.config != nil && s.config.Name != "" {
		return s.config.Name
	}
	return "gochen-module-server"
}

// LoadConfig 加载并规范化服务配置。
func (s *Host) LoadConfig() error {
	s.config = config.Normalize(s.config)
	s.container = s.config.Container
	s.runtime = nil
	s.routeRegistration.Store(routeRegistrationPending)
	return nil
}
