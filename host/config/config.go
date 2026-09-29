// Package config 定义 Host、模块运行时与生命周期引擎共享的单一配置模型。
package config

import (
	"context"
	"strings"
	"time"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/initcap"
	"gochen-runtime/http"
	"gochen/domain/eventsourced"
	"gochen/httpx"
	"gochen/observe/logging"
)

// Hook 定义生命周期回调函数。
type Hook func(context.Context) error

// DefaultShutdownTimeout 是停止与启动失败回滚阶段的默认清理预算。
const DefaultShutdownTimeout = 10 * time.Second

// ModuleHTTPConfig 定义模块级 HTTP 挂载配置。
//
// Prefix 为空时按模块 ID 使用默认前缀（例如模块 users 挂载到 /users）；
// 需要显式挂载到 Host 根路径时使用 Prefix: "/"。
type ModuleHTTPConfig struct {
	Disabled                  bool
	Prefix                    string
	Middlewares               []httpx.Middleware
	DisableDefaultMiddlewares bool
}

// Config 聚合模块 Host 与生命周期引擎的完整配置。
type Config struct {
	Modules []module.ModuleCtor

	Name          string
	SecurityLayer httpx.SecurityLayer
	AllowSession  *bool
	Host          string
	Port          int
	BasePath      string

	Container         di.IContainer
	EventBus          capability.IEventSubscriber
	Transport         capability.ITransport
	ProjectionRuntime capability.IProjectionRuntime
	HTTPServer        httpx.IServer
	ModuleRegistry    capability.IModuleRegistry
	MetadataRegistry  capability.IMetadataRegistry

	// CatalogRegistrars 是可选的模块目录登记器（如 host/authz 的权限目录）。
	// 为空时 Host 不做任何目录登记，也不会把相关扩展包编译进依赖图。
	CatalogRegistrars []capability.IModuleCatalogRegistrar

	RouteMiddlewares         []httpx.Middleware
	ModuleCapabilities       []initcap.Setter
	DisableHealthRoute       bool
	EnableMonitoringRoutes   bool
	FailFastOnRouteConflicts bool
	ModuleHTTP               map[string]ModuleHTTPConfig
	Version                  string
	ID                       string
	Metadata                 map[string]string
	StartupTimeout           time.Duration
	ShutdownTimeout          time.Duration
	ModuleHTTPDefaults       ModuleHTTPConfig
	Logger                   logging.ILogger
	OnBeforeInit             []Hook
	OnAfterInit              []Hook
	OnBeforeStart            []Hook
	OnAfterStart             []Hook
	OnBeforeStop             []Hook
	OnAfterStop              []Hook
}

// Option 修改共享 Host 配置。
type Option func(*Config)

// Default 返回共享 Host 配置默认值。
func Default() *Config {
	return &Config{
		SecurityLayer:            httpx.SecurityLayerAPI,
		Host:                     "0.0.0.0",
		Port:                     http.DefaultWebPort,
		BasePath:                 "/api/v1",
		Container:                basic.New(),
		ModuleRegistry:           module.NewModuleRegistry(),
		MetadataRegistry:         eventsourced.NewMetadataRegistry(),
		FailFastOnRouteConflicts: true,
		ModuleHTTP:               make(map[string]ModuleHTTPConfig),
		Version:                  "0.0.0",
		Metadata:                 make(map[string]string),
		StartupTimeout:           30 * time.Second,
		ShutdownTimeout:          DefaultShutdownTimeout,
		Logger:                   logging.ComponentLogger("app.engine"),
	}
}

// Build 应用选项并返回规范化配置。
func Build(opts ...Option) *Config {
	cfg := Default()
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return Normalize(cfg)
}

// Normalize 补齐配置的运行时必需值。
func Normalize(cfg *Config) *Config {
	if cfg == nil {
		cfg = Default()
	}
	if cfg.BasePath == "" {
		cfg.BasePath = "/api/v1"
	}
	if !strings.HasPrefix(cfg.BasePath, "/") {
		cfg.BasePath = "/" + cfg.BasePath
	}
	if cfg.SecurityLayer == "" {
		cfg.SecurityLayer = httpx.SecurityLayerAPI
	}
	if cfg.AllowSession == nil {
		allow := cfg.SecurityLayer == httpx.SecurityLayerWeb
		cfg.AllowSession = &allow
	}
	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.Port <= 0 {
		cfg.Port = http.DefaultWebPort
	}
	if cfg.Container == nil {
		cfg.Container = basic.New()
	}
	if cfg.ModuleRegistry == nil {
		cfg.ModuleRegistry = module.NewModuleRegistry()
	}
	if cfg.MetadataRegistry == nil {
		cfg.MetadataRegistry = eventsourced.NewMetadataRegistry()
	}
	if cfg.ModuleHTTP == nil {
		cfg.ModuleHTTP = make(map[string]ModuleHTTPConfig)
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}
	if cfg.Metadata == nil {
		cfg.Metadata = make(map[string]string)
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 30 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.ComponentLogger("app.engine")
	}
	return cfg
}

// WithModules 追加需要装配的模块构造函数。
func WithModules(ctors ...module.ModuleCtor) Option {
	return func(cfg *Config) {
		cfg.Modules = append(cfg.Modules, ctors...)
	}
}

// WithName 设置服务名称。
func WithName(name string) Option {
	return func(cfg *Config) {
		if name != "" {
			cfg.Name = name
		}
	}
}

// WithVersion 设置服务版本。
func WithVersion(version string) Option {
	return func(cfg *Config) {
		if version != "" {
			cfg.Version = version
		}
	}
}

// WithStartupTimeout 设置启动阶段超时。
func WithStartupTimeout(timeout time.Duration) Option {
	return func(cfg *Config) {
		if timeout > 0 {
			cfg.StartupTimeout = timeout
		}
	}
}

// WithShutdownTimeout 设置停止阶段超时。
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(cfg *Config) {
		if timeout > 0 {
			cfg.ShutdownTimeout = timeout
		}
	}
}

// WithLogger 注入生命周期日志器。
func WithLogger(logger logging.ILogger) Option {
	return func(cfg *Config) {
		if logger != nil {
			cfg.Logger = logger
		}
	}
}

// WithBeforeInit 注册初始化前钩子。
func WithBeforeInit(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnBeforeInit = append(cfg.OnBeforeInit, fn)
		}
	}
}

// WithAfterInit 注册初始化后钩子。
func WithAfterInit(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnAfterInit = append(cfg.OnAfterInit, fn)
		}
	}
}

// WithBeforeStart 注册启动前钩子。
func WithBeforeStart(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnBeforeStart = append(cfg.OnBeforeStart, fn)
		}
	}
}

// WithAfterStart 注册启动后钩子。
func WithAfterStart(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnAfterStart = append(cfg.OnAfterStart, fn)
		}
	}
}

// WithBeforeStop 注册停止前钩子。
func WithBeforeStop(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnBeforeStop = append(cfg.OnBeforeStop, fn)
		}
	}
}

// WithAfterStop 注册停止后钩子。
func WithAfterStop(fn Hook) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.OnAfterStop = append(cfg.OnAfterStop, fn)
		}
	}
}

// WithAddress 设置 HTTP 监听地址。
func WithAddress(host string) Option {
	return func(cfg *Config) {
		if host != "" {
			cfg.Host = host
		}
	}
}

// WithPort 设置 HTTP 监听端口。
func WithPort(port int) Option {
	return func(cfg *Config) {
		if port > 0 {
			cfg.Port = port
		}
	}
}

// WithBasePath 设置模块路由基础路径。
func WithBasePath(basePath string) Option {
	return func(cfg *Config) {
		if basePath == "" {
			return
		}
		if !strings.HasPrefix(basePath, "/") {
			basePath = "/" + basePath
		}
		cfg.BasePath = basePath
	}
}

// WithContainer 注入依赖容器。
func WithContainer(container di.IContainer) Option {
	return func(cfg *Config) {
		if container != nil {
			cfg.Container = container
		}
	}
}

// WithEventBus 注入事件订阅器。
func WithEventBus(eventBus capability.IEventSubscriber) Option {
	return func(cfg *Config) {
		if eventBus != nil {
			cfg.EventBus = eventBus
		}
	}
}

// WithTransport 注入消息传输运行时。
func WithTransport(transport capability.ITransport) Option {
	return func(cfg *Config) {
		if transport != nil {
			cfg.Transport = transport
		}
	}
}

// WithProjectionRuntime 注入投影运行时。
func WithProjectionRuntime(projectionRuntime capability.IProjectionRuntime) Option {
	return func(cfg *Config) {
		if projectionRuntime != nil {
			cfg.ProjectionRuntime = projectionRuntime
		}
	}
}

// WithCatalogRegistrar 追加模块目录登记器（可选扩展）。
//
// Host 基础容器不认识任何具体目录语义；需要收集模块目录的扩展
// （如 runtime/host/authz 的权限目录）经此注入。
func WithCatalogRegistrar(registrars ...capability.IModuleCatalogRegistrar) Option {
	return func(cfg *Config) {
		for _, registrar := range registrars {
			if registrar != nil {
				cfg.CatalogRegistrars = append(cfg.CatalogRegistrars, registrar)
			}
		}
	}
}

// WithModuleRegistry 注入模块注册表。
func WithModuleRegistry(registry capability.IModuleRegistry) Option {
	return func(cfg *Config) {
		if registry != nil {
			cfg.ModuleRegistry = registry
		}
	}
}

// WithMetadataRegistry 注入事件元数据注册表。
func WithMetadataRegistry(registry capability.IMetadataRegistry) Option {
	return func(cfg *Config) {
		if registry != nil {
			cfg.MetadataRegistry = registry
		}
	}
}

// WithHTTPServer 注入 HTTP 服务实现。
func WithHTTPServer(server httpx.IServer) Option {
	return func(cfg *Config) {
		if server != nil {
			cfg.HTTPServer = server
		}
	}
}

// WithRouteMiddlewares 追加基础路由中间件。
func WithRouteMiddlewares(middlewares ...httpx.Middleware) Option {
	return func(cfg *Config) {
		cfg.RouteMiddlewares = append(cfg.RouteMiddlewares, middlewares...)
	}
}

// WithModuleCapabilities 向所有模块初始化阶段注入通用 capability。
//
// 具体能力的 key 和类型由提供方定义，Host 只负责沿模块初始化链路统一传递。
func WithModuleCapabilities(capabilities ...initcap.Setter) Option {
	return func(cfg *Config) {
		for _, capability := range capabilities {
			if capability != nil {
				cfg.ModuleCapabilities = append(cfg.ModuleCapabilities, capability)
			}
		}
	}
}

// WithDisableHealthRoute 控制是否关闭默认健康检查路由。
func WithDisableHealthRoute(disable bool) Option {
	return func(cfg *Config) { cfg.DisableHealthRoute = disable }
}

// WithEnableMonitoringRoutes 控制是否启用监控路由。
func WithEnableMonitoringRoutes(enable bool) Option {
	return func(cfg *Config) { cfg.EnableMonitoringRoutes = enable }
}

// WithFailFastOnRouteConflicts 控制是否在启动时拒绝重复路由。
func WithFailFastOnRouteConflicts(enabled bool) Option {
	return func(cfg *Config) { cfg.FailFastOnRouteConflicts = enabled }
}

// WithModuleHTTP 设置指定模块的 HTTP 挂载配置。
func WithModuleHTTP(moduleID string, moduleConfig ModuleHTTPConfig) Option {
	return func(cfg *Config) {
		moduleID = strings.TrimSpace(moduleID)
		if moduleID == "" {
			return
		}
		if cfg.ModuleHTTP == nil {
			cfg.ModuleHTTP = make(map[string]ModuleHTTPConfig)
		}
		cfg.ModuleHTTP[moduleID] = moduleConfig
	}
}

// WithModuleHTTPDefaults 设置所有模块的默认 HTTP 挂载配置。
//
// 单个模块可通过 WithModuleHTTP 覆盖 Prefix、追加中间件，或关闭默认中间件。
func WithModuleHTTPDefaults(moduleConfig ModuleHTTPConfig) Option {
	return func(cfg *Config) {
		cfg.ModuleHTTPDefaults = moduleConfig
	}
}

// WithSecurityLayer 设置默认 HTTP 安全分层。
func WithSecurityLayer(layer httpx.SecurityLayer) Option {
	return func(cfg *Config) {
		if layer != "" {
			cfg.SecurityLayer = layer
		}
	}
}

// WithAllowSession 控制当前安全分层是否允许会话语义。
func WithAllowSession(allow bool) Option {
	return func(cfg *Config) { cfg.AllowSession = &allow }
}
