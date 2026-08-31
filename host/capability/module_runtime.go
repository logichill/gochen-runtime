package capability

import (
	"context"
	"sort"

	"gochen-runtime/host/internal/runtimeutil"
	"gochen/errors"
	"gochen/messaging"
)

// ResolveFunc 按需解析模块角色实例。
type ResolveFunc func() (any, error)

type binding struct {
	index   int
	resolve ResolveFunc
}

// ModuleRuntime 承载模块 role 对应的运行期启停逻辑。
type ModuleRuntime struct {
	moduleID string
	runtime  *Runtime

	eventHandlers     []binding
	projections       []binding
	runtimeComponents []binding

	unsubscribes    []messaging.UnsubscribeFunc
	projectionNames []string
	runtimeStops    []IRuntimeStopper
}

// NewModuleRuntime 创建模块运行期生命周期对象。
func NewModuleRuntime(moduleID string, runtime *Runtime) *ModuleRuntime {
	return &ModuleRuntime{
		moduleID: moduleID,
		runtime:  runtime,
	}
}

// AddEventHandler 追加事件处理器绑定。
func (m *ModuleRuntime) AddEventHandler(index int, resolve ResolveFunc) {
	if resolve == nil {
		return
	}
	m.eventHandlers = append(m.eventHandlers, binding{index: index, resolve: resolve})
}

// AddProjection 追加投影绑定。
func (m *ModuleRuntime) AddProjection(index int, resolve ResolveFunc) {
	if resolve == nil {
		return
	}
	m.projections = append(m.projections, binding{index: index, resolve: resolve})
}

// AddRuntimeComponent 追加运行期组件绑定。
func (m *ModuleRuntime) AddRuntimeComponent(index int, resolve ResolveFunc) {
	if resolve == nil {
		return
	}
	m.runtimeComponents = append(m.runtimeComponents, binding{index: index, resolve: resolve})
}

// Start 启动模块的事件/投影/runtime component 生命周期。
func (m *ModuleRuntime) Start(ctx context.Context, onStart func(context.Context) error) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	subscriber, subscriberSource := m.runtime.EffectiveSubscriberWithSource()
	if len(m.eventHandlers) > 0 && subscriber == nil {
		return errors.NewCode(errors.InvalidInput, "event handlers configured but no event bus/transport").
			WithContext("module", m.moduleID).
			WithContext("event_handlers_count", len(m.eventHandlers)).
			WithContext("subscriber_source", string(subscriberSource)).
			WithContext("hint", "configure host/config.WithEventBus or host/config.WithTransport for modules that declare event handlers")
	}

	if err := m.startEventHandlers(ctx, subscriber, subscriberSource); err != nil {
		return err
	}
	if err := m.startProjections(ctx); err != nil {
		err = m.withSubscriptionCleanupErrors(ctx, err)
		_ = m.stopProjections(ctx)
		return err
	}
	if err := m.startRuntimeComponents(ctx); err != nil {
		err = m.withSubscriptionCleanupErrors(ctx, err)
		_ = m.stopProjections(ctx)
		_ = m.stopRuntimeComponents(ctx)
		return err
	}
	if onStart != nil {
		if err := onStart(ctx); err != nil {
			err = m.withSubscriptionCleanupErrors(ctx, err)
			_ = m.stopProjections(ctx)
			_ = m.stopRuntimeComponents(ctx)
			return wrapModuleErr(m.moduleID, err, "OnStart")
		}
	}

	return nil
}

// Stop 停止模块的事件/投影/runtime component 生命周期。
func (m *ModuleRuntime) Stop(ctx context.Context, onStop func(context.Context) error) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	var errs []error

	if cleanupErrs := m.cleanupSubscriptions(ctx); len(cleanupErrs) > 0 {
		errs = append(errs, cleanupErrs...)
	}

	if onStop != nil {
		if err := onStop(ctx); err != nil {
			errs = append(errs, wrapModuleErr(m.moduleID, err, "OnStop"))
		}
	}

	if stopErrs := m.stopRuntimeComponents(ctx); len(stopErrs) > 0 {
		errs = append(errs, stopErrs...)
	}

	if stopErrs := m.stopProjections(ctx); len(stopErrs) > 0 {
		errs = append(errs, stopErrs...)
	}

	if len(errs) > 0 {
		return errors.NewCode(errors.Internal, "module stop errors").
			WithContext("module", m.moduleID).
			WithContext("errors", errs)
	}
	return nil
}

func (m *ModuleRuntime) startEventHandlers(ctx context.Context, subscriber IEventSubscriber, subscriberSource SubscriberSource) error {
	for _, item := range m.eventHandlers {
		inst, err := item.resolve()
		if err != nil {
			return wrapModuleErr(m.moduleID, err, "resolve event handler").
				WithContext("index", item.index)
		}

		handler, ok := inst.(messaging.IMessageHandler)
		if !ok {
			return errors.NewCode(errors.Internal, "event handler does not implement IMessageHandler").
				WithContext("module", m.moduleID).
				WithContext("index", item.index)
		}

		messageTypes := getHandlerMessageTypes(handler)
		if len(messageTypes) == 0 {
			err := errors.NewCode(errors.InvalidInput, "event handler resolved no valid message types").
				WithContext("module", m.moduleID).
				WithContext("index", item.index)
			return m.withSubscriptionCleanupErrors(ctx, err)
		}

		for _, messageType := range messageTypes {
			unsub, err := subscriber.Subscribe(ctx, messageType, handler)
			if err != nil {
				startErr := wrapModuleErr(m.moduleID, err, "subscribe handler").
					WithContext("index", item.index).
					WithContext("message_type", messageType).
					WithContext("subscriber_source", string(subscriberSource))
				return m.withSubscriptionCleanupErrors(ctx, startErr)
			}
			m.unsubscribes = append(m.unsubscribes, unsub)
		}
	}

	return nil
}

func (m *ModuleRuntime) withSubscriptionCleanupErrors(ctx context.Context, err error) error {
	cleanupErrs := m.cleanupSubscriptions(ctx)
	if len(cleanupErrs) == 0 {
		return err
	}
	if appErr, ok := errors.AsType[*errors.AppError](err); ok && appErr != nil {
		return appErr.WithContext("cleanup_errors", cleanupErrs)
	}
	return errors.Wrap(err, errors.Code(err), "module start error").WithContext("cleanup_errors", cleanupErrs)
}

func (m *ModuleRuntime) cleanupSubscriptions(ctx context.Context) []error {
	var errs []error
	for _, unsub := range m.unsubscribes {
		if unsub != nil {
			if err := unsub(ctx); err != nil {
				errs = append(errs, wrapModuleErr(m.moduleID, err, "unsubscribe handler"))
			}
		}
	}
	m.unsubscribes = nil
	return errs
}

func (m *ModuleRuntime) startProjections(ctx context.Context) error {
	if m == nil || len(m.projections) == 0 {
		return nil
	}
	if m.runtime == nil || m.runtime.ProjectionRuntime == nil || runtimeutil.IsTypedNil(m.runtime.ProjectionRuntime) {
		return errors.NewCode(errors.InvalidInput, "projections configured but no projection runtime").
			WithContext("module", m.moduleID).
			WithContext("projections_count", len(m.projections)).
			WithContext("hint", "configure host/config.WithProjectionRuntime for modules that declare projections")
	}

	resumer, _ := any(m.runtime.ProjectionRuntime).(IProjectionResumer)
	stopper, _ := any(m.runtime.ProjectionRuntime).(IProjectionStopper)
	unregister, _ := any(m.runtime.ProjectionRuntime).(IProjectionUnregister)
	canCleanup := stopper != nil || unregister != nil

	names := make([]string, 0, len(m.projections))
	for _, item := range m.projections {
		inst, err := item.resolve()
		if err != nil {
			if canCleanup && len(names) > 0 {
				m.projectionNames = append([]string(nil), names...)
				_ = m.stopProjections(ctx)
			}
			return wrapModuleErr(m.moduleID, err, "resolve projection").
				WithContext("index", item.index)
		}

		p, ok := inst.(interface{ Name() string })
		if !ok || runtimeutil.IsTypedNil(p) {
			if canCleanup && len(names) > 0 {
				m.projectionNames = append([]string(nil), names...)
				_ = m.stopProjections(ctx)
			}
			return errors.NewCode(errors.Internal, "projection does not expose projection name").
				WithContext("module", m.moduleID).
				WithContext("index", item.index)
		}

		if err := m.runtime.ProjectionRuntime.RegisterProjectionAny(ctx, inst); err != nil {
			if canCleanup && len(names) > 0 {
				m.projectionNames = append([]string(nil), names...)
				_ = m.stopProjections(ctx)
			}
			return wrapModuleErr(m.moduleID, err, "register projection").
				WithContext("index", item.index).
				WithContext("projection", p.Name())
		}

		names = append(names, p.Name())

		if err := m.runtime.ProjectionRuntime.StartProjection(p.Name()); err != nil {
			if canCleanup && len(names) > 0 {
				m.projectionNames = append([]string(nil), names...)
				_ = m.stopProjections(ctx)
			}
			return wrapModuleErr(m.moduleID, err, "start projection").
				WithContext("index", item.index).
				WithContext("projection", p.Name())
		}
		if resumer != nil {
			if err := resumer.ResumeFromCheckpoint(ctx, p.Name()); err != nil {
				if canCleanup && len(names) > 0 {
					m.projectionNames = append([]string(nil), names...)
					_ = m.stopProjections(ctx)
				}
				return wrapModuleErr(m.moduleID, err, "resume projection from checkpoint").
					WithContext("index", item.index).
					WithContext("projection", p.Name())
			}
		}
	}

	m.projectionNames = names
	return nil
}

func (m *ModuleRuntime) stopProjections(ctx context.Context) []error {
	if m == nil || m.runtime == nil || m.runtime.ProjectionRuntime == nil || len(m.projectionNames) == 0 {
		return nil
	}
	stopper, _ := any(m.runtime.ProjectionRuntime).(IProjectionStopper)
	unregister, _ := any(m.runtime.ProjectionRuntime).(IProjectionUnregister)
	if stopper == nil && unregister == nil {
		m.projectionNames = nil
		return nil
	}

	var errs []error
	for i := len(m.projectionNames) - 1; i >= 0; i-- {
		name := m.projectionNames[i]
		if name == "" {
			continue
		}
		if unregister != nil {
			if err := unregister.UnregisterProjectionWithContext(ctx, name); err != nil && !errors.Is(err, errors.NotFound) {
				errs = append(errs, wrapModuleErr(m.moduleID, err, "unregister projection").WithContext("projection", name))
			}
			continue
		}
		if stopper != nil {
			if err := stopper.StopProjection(name); err != nil {
				errs = append(errs, wrapModuleErr(m.moduleID, err, "stop projection").WithContext("projection", name))
			}
		}
	}
	m.projectionNames = nil
	return errs
}

func (m *ModuleRuntime) startRuntimeComponents(ctx context.Context) error {
	if m == nil || len(m.runtimeComponents) == 0 {
		return nil
	}

	m.runtimeStops = nil
	started := make([]IRuntimeStopper, 0, len(m.runtimeComponents))
	for _, item := range m.runtimeComponents {
		inst, err := item.resolve()
		if err != nil {
			m.runtimeStops = append([]IRuntimeStopper(nil), started...)
			_ = m.stopRuntimeComponents(ctx)
			return wrapModuleErr(m.moduleID, err, "resolve runtime component").
				WithContext("index", item.index)
		}

		component, ok := inst.(IRuntimeComponent)
		if !ok || runtimeutil.IsTypedNil(component) {
			m.runtimeStops = append([]IRuntimeStopper(nil), started...)
			_ = m.stopRuntimeComponents(ctx)
			return errors.NewCode(errors.Internal, "runtime component does not implement IRuntimeComponent").
				WithContext("module", m.moduleID).
				WithContext("index", item.index)
		}

		if err := component.Start(ctx); err != nil {
			m.runtimeStops = append([]IRuntimeStopper(nil), started...)
			_ = m.stopRuntimeComponents(ctx)
			return wrapModuleErr(m.moduleID, err, "start runtime component").
				WithContext("index", item.index)
		}

		if stopper, ok := inst.(IRuntimeStopper); ok && !runtimeutil.IsTypedNil(stopper) {
			started = append(started, stopper)
		}
	}

	m.runtimeStops = append([]IRuntimeStopper(nil), started...)
	return nil
}

func (m *ModuleRuntime) stopRuntimeComponents(ctx context.Context) []error {
	if m == nil || len(m.runtimeStops) == 0 {
		return nil
	}

	var errs []error
	for i := len(m.runtimeStops) - 1; i >= 0; i-- {
		stopper := m.runtimeStops[i]
		if stopper == nil || runtimeutil.IsTypedNil(stopper) {
			continue
		}
		if err := stopper.Stop(ctx); err != nil {
			errs = append(errs, wrapModuleErr(m.moduleID, err, "stop runtime component").
				WithContext("index", i))
		}
	}
	m.runtimeStops = nil
	return errs
}

func getHandlerMessageTypes(handler messaging.IMessageHandler) []string {
	if handler == nil {
		return nil
	}

	if p, ok := handler.(IMessageTypesProvider); ok {
		types := p.EventTypes()
		if len(types) > 0 {
			return uniqStrings(types)
		}
	}

	if t := handler.Type(); t != "" {
		return []string{t}
	}
	return nil
}

func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func wrapModuleErr(moduleID string, err error, message string) errors.IAppError {
	if err == nil {
		return nil
	}

	var appErr *errors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		return appErr.Wrap(message).WithContext("module", moduleID)
	}
	return errors.Wrap(err, errors.Internal, message).WithContext("module", moduleID)
}
