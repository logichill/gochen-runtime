package module

import (
	"context"
	"net/http"
	"strings"

	"gochen-runtime/host/module/initcap"
	"gochen/errors"
	"gochen/httpx"
)

const runtimeManifestModuleID = "gochen_runtime_manifest"

// IModuleRegistry 暴露模块目录管理能力。
type IModuleRegistry interface {
	Register(ModuleInfo) error
	Modules() []ModuleInfo
}

var (
	// ModuleRegistryKey 是模块目录能力的类型化键。
	ModuleRegistryKey = initcap.NewKey[IModuleRegistry]("host.module.runtimecap.module_registry")
)

// RuntimeManifest 表示登录前可读取的宿主模块能力快照。
type RuntimeManifest struct {
	Service string                  `json:"service"`
	Modules []RuntimeManifestModule `json:"modules"`
}

// RuntimeManifestModule 表示一个模块的公开目录项。
type RuntimeManifestModule struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Features map[string]string `json:"features"`
}

// RuntimeManifestModuleAdapter 将 Host 的模块目录投影为公共 HTTP 端点。
type RuntimeManifestModuleAdapter struct {
	*BaseModule

	service  string
	http     IHTTPOptions
	registry IModuleRegistry
}

// NewRuntimeManifestModule 创建公共模块 manifest 适配器。
func NewRuntimeManifestModule(service string) *RuntimeManifestModuleAdapter {
	return &RuntimeManifestModuleAdapter{
		BaseModule: NewBaseModule(runtimeManifestModuleID, "Runtime Manifest"),
		service:    strings.TrimSpace(service),
	}
}

// Init 保存 Host 注入的 HTTP 与模块目录能力。
func (m *RuntimeManifestModuleAdapter) Init(opts ModuleInitOptions) error {
	if m == nil {
		return errors.NewCode(errors.InvalidInput, "runtime manifest module is nil")
	}
	m.http, _ = CapabilityFrom(opts, HTTPKey)
	m.registry, _ = CapabilityFrom(opts, ModuleRegistryKey)
	if m.registry == nil {
		return errors.NewCode(errors.Dependency, "module registry is unavailable")
	}
	return nil
}

// RegisterRoutes 注册登录前可访问的模块 manifest 端点。
func (m *RuntimeManifestModuleAdapter) RegisterRoutes(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if m == nil || m.registry == nil {
		return errors.NewCode(errors.Dependency, "runtime manifest module is not initialized")
	}
	if m.http == nil {
		return nil
	}
	group := m.http.MountGroup()
	if group == nil {
		return nil
	}
	group.GET("/modules", func(requestCtx httpx.IContext) error {
		return httpx.WriteSuccessStatus(requestCtx, http.StatusOK, m.snapshot())
	})
	return nil
}

func (m *RuntimeManifestModuleAdapter) snapshot() RuntimeManifest {
	manifest := RuntimeManifest{
		Service: m.service,
		Modules: make([]RuntimeManifestModule, 0),
	}
	if m == nil || m.registry == nil {
		return manifest
	}
	for _, info := range m.registry.Modules() {
		features := make(map[string]string, len(info.Features))
		for _, feature := range info.Features {
			features[feature.Key] = feature.Value
		}
		manifest.Modules = append(manifest.Modules, RuntimeManifestModule{
			ID:       info.ID,
			Name:     info.Name,
			Features: features,
		})
	}
	return manifest
}
