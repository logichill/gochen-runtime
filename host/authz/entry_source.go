package authz

import (
	"context"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/internal/runtimeutil"
	"gochen/auth"
	"gochen/errors"
)

// IHandlerRegistry 暴露命令、Worker 或 CLI 的实际处理器注册源。
type IHandlerRegistry interface {
	RegisteredHandlers() []string
	SealHandlers() error
}

// HandlerEntries 将中性处理器注册表投影到启动门禁。
type HandlerEntries struct {
	kind     string
	registry IHandlerRegistry
}

func NewHandlerEntries(kind string, registry IHandlerRegistry) (*HandlerEntries, error) {
	if kind == "" || runtimeutil.IsTypedNil(registry) {
		return nil, errors.NewCode(errors.InvalidInput, "entry source requires a kind and handler registry")
	}
	return &HandlerEntries{kind: kind, registry: registry}, nil
}
func (s *HandlerEntries) Entries() []auth.Entry {
	names := s.registry.RegisteredHandlers()
	out := make([]auth.Entry, 0, len(names))
	for _, name := range names {
		out = append(out, auth.Entry{Kind: s.kind, Key: name})
	}
	return out
}
func (s *HandlerEntries) SealEntries() error { return s.registry.SealHandlers() }

// ActivateEntries 启用支持分阶段执行的实际处理器注册表。
func (s *HandlerEntries) ActivateEntries() error {
	if activator, ok := s.registry.(interface{ ActivateHandlers() error }); ok {
		return activator.ActivateHandlers()
	}
	return nil
}

// EntryRegistration 同时提供独立实际注册源与该来源的执行绑定。
type EntryRegistration struct {
	Source   IEntrySource
	Bindings []auth.EntryBinding
}

// IEntryProvider 在后台启动前提供实际注册源及执行计划，方法本身仅做装配。
type IEntryProvider interface {
	AuthorizationEntries(context.Context) ([]EntryRegistration, error)
}

// PrepareModuleEntries 收集模块及实际运行期组件，与后续启动使用同一实例。
func (r *EntryRegistry) PrepareModuleEntries(ctx context.Context, modules []capability.ModuleCatalogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.modulesPrepared {
		return nil
	}
	if r.sealed {
		return errors.NewCode(errors.FailedPrecondition, "entry registry is sealed")
	}
	var sources []IEntrySource
	var bindings []auth.EntryBinding
	collect := func(moduleID string, provider IEntryProvider) error {
		registrations, err := provider.AuthorizationEntries(ctx)
		if err != nil {
			return err
		}
		for _, registration := range registrations {
			if runtimeutil.IsTypedNil(registration.Source) {
				return errors.NewCode(errors.InvalidInput, "background entry registration requires an actual source")
			}
			sources = append(sources, registration.Source)
			for _, binding := range registration.Bindings {
				binding = binding.Copy()
				binding.Module = moduleID
				bindings = append(bindings, binding)
			}
		}
		return nil
	}
	for _, item := range modules {
		if provider, ok := item.Module.(capability.IEventHandlerRegistrationProvider); ok {
			source, eventBindings, err := collectEventEntries(provider, item.ID)
			if err != nil {
				return err
			}
			sources = append(sources, source)
			bindings = append(bindings, eventBindings...)
		}
		if provider, ok := item.Module.(IEntryProvider); ok {
			if err := collect(item.ID, provider); err != nil {
				return err
			}
		}
		if provider, ok := item.Module.(capability.IRuntimeComponentProvider); ok {
			components, err := provider.RuntimeComponents()
			if err != nil {
				return err
			}
			for index, component := range components {
				provider, ok := component.(IEntryProvider)
				if !ok || runtimeutil.IsTypedNil(provider) {
					return errors.NewCode(errors.InvalidInput, "runtime component requires an authorization entry source").WithContext("module", item.ID).WithContext("component", index)
				}
				if err := collect(item.ID, provider); err != nil {
					return err
				}
			}
		}
	}
	r.sources = append(r.sources, sources...)
	r.bindings = append(r.bindings, bindings...)
	r.modulesPrepared = true
	return nil
}
