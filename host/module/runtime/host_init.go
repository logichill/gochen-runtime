package runtime

import (
	"context"
	"strings"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/initcap"
	"gochen-runtime/host/module/runtimecap"
	"gochen/errors"
	"gochen/httpx"
)

func (s *Host) normalizeModuleHTTPConfigKeys() error {
	if s == nil || s.config == nil || len(s.config.ModuleHTTP) == 0 {
		return nil
	}
	normalized := make(map[string]config.ModuleHTTPConfig, len(s.config.ModuleHTTP))
	for rawID, cfg := range s.config.ModuleHTTP {
		id, err := module.NormalizeModuleID(rawID)
		if err != nil {
			return errors.Wrap(err, errors.InvalidInput, "invalid module http config key").WithContext("raw_id", rawID)
		}
		if _, exists := normalized[id]; exists {
			return errors.NewCode(errors.Conflict, "duplicate module http config key").WithContext("id", id)
		}
		normalized[id] = cfg
	}
	s.config.ModuleHTTP = normalized
	return nil
}

// makeModuleInitOptions 构造模块初始化选项。
func (s *Host) makeModuleInitOptions(m module.IModule) module.ModuleInitOptions {
	var baseGroup httpx.IRouteGroup
	var eventBus capability.IEventSubscriber
	var projectionRuntime capability.IProjectionRuntime
	var transport capability.ITransport
	if s.runtime != nil {
		baseGroup = s.runtime.BaseGroup
		eventBus = s.runtime.EventBus
		projectionRuntime = s.runtime.ProjectionRuntime
		transport = s.runtime.Transport
	}
	httpOpt := s.makeHTTPOptions(m, baseGroup)
	var registry di.IRegistry
	var resolver di.IResolver
	var moduleContainer module.IModuleContainer
	if s.container != nil {
		registry = s.container
		resolver = s.container
		moduleContainer = s.container
	}
	capabilities := []initcap.Setter{
		runtimecap.HTTP(httpOpt),
		runtimecap.ModuleRegistry(s.moduleRegistry),
		runtimecap.MetadataRegistry(s.metadataRegistry),
		runtimecap.EventBus(eventBus),
		runtimecap.ProjectionRuntime(projectionRuntime),
		runtimecap.Transport(transport),
	}
	capabilities = append(capabilities, s.config.ModuleCapabilities...)
	opts := module.NewModuleInitOptions(
		registry,
		resolver,
		moduleContainer,
		capabilities...,
	)
	return opts
}

func (s *Host) registerModules() error {
	if s == nil || s.moduleRegistry == nil {
		return nil
	}
	for _, m := range s.modules {
		if m == nil {
			continue
		}
		var features []module.PublicFeature
		if provider, ok := m.(module.IPublicFeatureProvider); ok {
			features = provider.PublicFeatures()
		}
		if err := s.moduleRegistry.Register(module.ModuleInfo{
			ID:       m.ID(),
			Name:     m.Name(),
			Features: features,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Host) registerModuleCatalogs(ctx context.Context) error {
	if s == nil || len(s.catalogRegistrars) == 0 {
		return nil
	}
	entries := make([]capability.ModuleCatalogEntry, 0, len(s.modules))
	for _, m := range s.modules {
		if m == nil {
			continue
		}
		entries = append(entries, capability.ModuleCatalogEntry{ID: s.moduleIDs[m], Module: m})
	}
	for _, registrar := range s.catalogRegistrars {
		if registrar == nil {
			continue
		}
		if err := registrar.RegisterModuleCatalog(ctx, entries); err != nil {
			return err
		}
	}
	return nil
}

// makeHTTPOptions 构造HTTP选项。
func (s *Host) makeHTTPOptions(m module.IModule, baseGroup httpx.IRouteGroup) *runtimecap.ModuleHTTPOptions {
	if baseGroup == nil {
		return nil
	}

	id := ""
	if m != nil {
		if s != nil && s.moduleIDs != nil {
			id = s.moduleIDs[m]
		}
		if id == "" {
			id = strings.TrimSpace(m.ID())
		}
	}
	defaults := s.config.ModuleHTTPDefaults
	cfg, ok := s.config.ModuleHTTP[id]
	if defaults.Disabled && !ok {
		return nil
	}
	if ok && cfg.Disabled {
		return nil
	}

	prefix := defaults.Prefix
	if ok && cfg.Prefix != "" {
		prefix = cfg.Prefix
	} else if id != "" {
		if prefix == "" {
			prefix = "/" + id
		}
	}
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	var mws []httpx.Middleware
	if !(ok && cfg.DisableDefaultMiddlewares) && len(defaults.Middlewares) > 0 {
		mws = append(mws, defaults.Middlewares...)
	}
	if ok && len(cfg.Middlewares) > 0 {
		mws = append(mws, cfg.Middlewares...)
	}

	return runtimecap.NewModuleHTTPOptions(baseGroup, prefix, mws...)
}
