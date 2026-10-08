package runtime

import (
	"sync/atomic"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/bootstrap"
	"gochen-runtime/host/module"
	"gochen/errors"
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

	// moduleIDs 记录已由 ensureModuleIDs 校验的规范化模块 ID 集合。
	// 不得以模块实例作键：值类型模块（含 slice/map 字段）动态类型不可比较，
	// 以实例为 map 键会触发运行时 "hash of unhashable type" panic。
	moduleIDs map[string]struct{}
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
// 仅在 Prepare 前调用；已装配的 Host 必须先 Shutdown，再创建新实例加载配置。
// 拒绝重载时保留原 runtime 和全部清理责任，包括启动/停止失败后的待清理资源。
func (s *Host) LoadConfig() error {
	if s.runtime != nil || s.backgroundStarted.Load() || s.transportOwned || len(s.moduleStops) > 0 {
		return errors.NewCode(errors.Conflict, "host already initialized; shut down and create a new host to reload configuration")
	}
	s.config = config.Normalize(s.config)
	s.container = s.config.Container
	s.routeRegistration.Store(routeRegistrationPending)
	return nil
}
