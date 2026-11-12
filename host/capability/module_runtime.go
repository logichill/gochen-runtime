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
	index     int
	resolve   ResolveFunc
	component IRuntimeComponent
	handler   messaging.IMessageHandler
	types     []string
}

type projectionRegistration struct {
	name    string
	release messaging.UnsubscribeFunc
}

// ModuleRuntime 承载模块 role 对应的运行期启停逻辑。
type ModuleRuntime struct {
	moduleID string
	runtime  *Runtime

	eventHandlers     []binding
	projections       []binding
	runtimeComponents []binding

	unsubscribes      []messaging.UnsubscribeFunc
	projectionRecords []projectionRegistration
	runtimeStops      []IRuntimeStopper
	eventsManaged     bool
	eventsSealed      bool
	eventsActivated   bool
	eventsCount       int
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

func (m *ModuleRuntime) runtimeComponent(index int) (IRuntimeComponent, error) {
	item := &m.runtimeComponents[index]
	if item.component != nil {
		return item.component, nil
	}
	inst, err := item.resolve()
	if err != nil {
		return nil, runtimeutil.WrapModuleErr(m.moduleID, err, "resolve runtime component", errors.Internal).WithContext("index", item.index)
	}
	component, ok := inst.(IRuntimeComponent)
	if !ok || runtimeutil.IsTypedNil(component) {
		return nil, errors.NewCode(errors.Internal, "runtime component does not implement IRuntimeComponent").WithContext("module", m.moduleID).WithContext("index", item.index)
	}
	item.component = component
	return component, nil
}

// RuntimeComponents 在启动前解析真实组件，并固定后续 Start 使用的实例。
func (m *ModuleRuntime) RuntimeComponents() ([]IRuntimeComponent, error) {
	var components []IRuntimeComponent
	for i := range m.runtimeComponents {
		component, err := m.runtimeComponent(i)
		if err != nil {
			return nil, err
		}
		components = append(components, component)
	}
	return components, nil
}

// Start 启动模块的事件/投影/runtime component 生命周期。
// 启动失败时保留已取得的资源并直接返回；调用方必须随后调用 Stop 完成回滚。
func (m *ModuleRuntime) Start(ctx context.Context, onStart func(context.Context) error) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if len(m.unsubscribes) > 0 || len(m.projectionRecords) > 0 || len(m.runtimeStops) > 0 {
		return errors.NewCode(errors.Conflict, "module has resources pending cleanup").WithContext("module", m.moduleID)
	}
	if m.eventsManaged && (!m.eventsActivated || len(m.eventHandlers) != m.eventsCount) {
		return errors.NewCode(errors.FailedPrecondition, "event subscriptions require validated and activated entries").WithContext("module", m.moduleID)
	}

	var subscriber IEventSubscriber
	var subscriberSource SubscriberSource
	if m.runtime != nil {
		subscriber, subscriberSource = m.runtime.EffectiveSubscriberWithSource()
	}
	if len(m.eventHandlers) > 0 && runtimeutil.IsTypedNil(subscriber) {
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
		return err
	}
	if err := m.startRuntimeComponents(ctx); err != nil {
		return err
	}
	if onStart != nil {
		if err := onStart(ctx); err != nil {
			return runtimeutil.WrapModuleErr(m.moduleID, err, "OnStart", errors.Internal)
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
			errs = append(errs, runtimeutil.WrapModuleErr(m.moduleID, err, "OnStop", errors.Internal))
			// 模块自有依赖可能是重试 OnStop 所必需的，在钩子成功前保留它们。
			return joinModuleStopErrors(m.moduleID, errs)
		}
	}

	if stopErrs := m.stopRuntimeComponents(ctx); len(stopErrs) > 0 {
		errs = append(errs, stopErrs...)
	}

	if stopErrs := m.stopProjections(ctx); len(stopErrs) > 0 {
		errs = append(errs, stopErrs...)
	}

	if len(errs) > 0 {
		return joinModuleStopErrors(m.moduleID, errs)
	}
	return nil
}

func joinModuleStopErrors(moduleID string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	// 各清理错误只经 Join 链暴露一次，外层 AppError 仅提供统一错误码与模块上下文。
	return errors.Join(
		errors.NewCode(errors.Internal, "module stop errors").WithContext("module", moduleID),
		errors.Join(errs...),
	)
}

func (m *ModuleRuntime) startEventHandlers(ctx context.Context, subscriber IEventSubscriber, subscriberSource SubscriberSource) error {
	for i := range m.eventHandlers {
		item := &m.eventHandlers[i]
		handler, messageTypes, err := m.eventHandler(i)
		if err != nil {
			return err
		}

		for _, messageType := range messageTypes {
			unsub, err := subscriber.Subscribe(ctx, messageType, handler)
			if err != nil {
				return runtimeutil.WrapModuleErr(m.moduleID, err, "subscribe handler", errors.Internal).
					WithContext("index", item.index).
					WithContext("message_type", messageType).
					WithContext("subscriber_source", string(subscriberSource))
			}
			m.unsubscribes = append(m.unsubscribes, unsub)
		}
	}

	return nil
}

func (m *ModuleRuntime) cleanupSubscriptions(ctx context.Context) []error {
	var errs []error
	remaining := make([]messaging.UnsubscribeFunc, 0, len(m.unsubscribes))
	for _, unsub := range m.unsubscribes {
		if unsub != nil {
			if err := unsub(ctx); err != nil {
				errs = append(errs, runtimeutil.WrapModuleErr(m.moduleID, err, "unsubscribe handler", errors.Internal))
				remaining = append(remaining, unsub)
			}
		}
	}
	m.unsubscribes = remaining
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

	m.projectionRecords = make([]projectionRegistration, 0, len(m.projections))
	for _, item := range m.projections {
		inst, err := item.resolve()
		if err != nil {
			return runtimeutil.WrapModuleErr(m.moduleID, err, "resolve projection", errors.Internal).
				WithContext("index", item.index)
		}

		p, ok := inst.(interface{ Name() string })
		if !ok || runtimeutil.IsTypedNil(p) {
			return errors.NewCode(errors.Internal, "projection does not expose projection name").
				WithContext("module", m.moduleID).
				WithContext("index", item.index)
		}

		release, registerErr := m.runtime.ProjectionRuntime.RegisterProjectionAny(ctx, inst)
		// 失败注册也可能需要清理；释放函数已绑定本次注册的身份。
		if release != nil {
			m.projectionRecords = append(m.projectionRecords, projectionRegistration{name: p.Name(), release: release})
		}
		if registerErr != nil {
			return runtimeutil.WrapModuleErr(m.moduleID, registerErr, "register projection", errors.Internal).
				WithContext("index", item.index).
				WithContext("projection", p.Name())
		}
		if release == nil {
			return errors.NewCode(errors.Internal, "projection runtime returned no release function").
				WithContext("module", m.moduleID).
				WithContext("index", item.index).
				WithContext("projection", p.Name())
		}

		if err := m.runtime.ProjectionRuntime.StartProjection(p.Name()); err != nil {
			return runtimeutil.WrapModuleErr(m.moduleID, err, "start projection", errors.Internal).
				WithContext("index", item.index).
				WithContext("projection", p.Name())
		}
		if resumer != nil {
			if err := resumer.ResumeFromCheckpoint(ctx, p.Name()); err != nil {
				return runtimeutil.WrapModuleErr(m.moduleID, err, "resume projection from checkpoint", errors.Internal).
					WithContext("index", item.index).
					WithContext("projection", p.Name())
			}
		}
	}
	return nil
}

func (m *ModuleRuntime) stopProjections(ctx context.Context) []error {
	if m == nil || m.runtime == nil || m.runtime.ProjectionRuntime == nil || len(m.projectionRecords) == 0 {
		return nil
	}

	var errs []error
	remaining := make([]projectionRegistration, 0, len(m.projectionRecords))
	for i := len(m.projectionRecords) - 1; i >= 0; i-- {
		registration := m.projectionRecords[i]
		// 已释放的注册由释放函数幂等处理；所有错误均表示仍需重试。
		err := registration.release(ctx)
		if err != nil {
			errs = append(errs, runtimeutil.WrapModuleErr(m.moduleID, err, "unregister projection", errors.Internal).
				WithContext("projection", registration.name))
			remaining = append(remaining, registration)
		}
	}
	// 保留失败项并按原逆序遍历顺序恢复，供下一次 Stop 重试。
	for i, j := 0, len(remaining)-1; i < j; i, j = i+1, j-1 {
		remaining[i], remaining[j] = remaining[j], remaining[i]
	}
	m.projectionRecords = remaining
	return errs
}

func (m *ModuleRuntime) startRuntimeComponents(ctx context.Context) error {
	if m == nil || len(m.runtimeComponents) == 0 {
		return nil
	}

	m.runtimeStops = nil
	started := make([]IRuntimeStopper, 0, len(m.runtimeComponents))
	for i, item := range m.runtimeComponents {
		component, err := m.runtimeComponent(i)
		if err != nil {
			m.runtimeStops = append([]IRuntimeStopper(nil), started...)
			return err
		}

		stopper, hasStopper := component.(IRuntimeStopper)
		if !hasStopper || runtimeutil.IsTypedNil(stopper) {
			stopper = nil
		}
		if err := component.Start(ctx); err != nil {
			// Start 可能已获取资源后才失败：若组件提供 Stop，则保留它，
			// 由同一生命周期所有者完成部分清理，并支持对失败的 Stop 重试。
			m.runtimeStops = append([]IRuntimeStopper(nil), started...)
			if stopper != nil {
				m.runtimeStops = append(m.runtimeStops, stopper)
			}
			return runtimeutil.WrapModuleErr(m.moduleID, err, "start runtime component", errors.Internal).WithContext("index", item.index)
		}

		if stopper != nil {
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
	remaining := make([]IRuntimeStopper, 0, len(m.runtimeStops))
	for i := len(m.runtimeStops) - 1; i >= 0; i-- {
		stopper := m.runtimeStops[i]
		if stopper == nil || runtimeutil.IsTypedNil(stopper) {
			continue
		}
		if err := stopper.Stop(ctx); err != nil {
			errs = append(errs, runtimeutil.WrapModuleErr(m.moduleID, err, "stop runtime component", errors.Internal).
				WithContext("index", i))
			remaining = append(remaining, stopper)
		}
	}
	for i, j := 0, len(remaining)-1; i < j; i, j = i+1, j-1 {
		remaining[i], remaining[j] = remaining[j], remaining[i]
	}
	m.runtimeStops = remaining
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
