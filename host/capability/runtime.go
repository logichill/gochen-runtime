package capability

import "gochen-runtime/host/internal/runtimeutil"

// SubscriberSource 表示模块事件订阅能力来源。
type SubscriberSource string

const (
	// SubscriberSourceNone 表示未配置事件订阅能力。
	SubscriberSourceNone SubscriberSource = "none"
	// SubscriberSourceEventBus 表示使用显式 EventBus 订阅事件。
	SubscriberSourceEventBus SubscriberSource = "event_bus"
	// SubscriberSourceTransport 表示复用 Transport 订阅事件。
	SubscriberSourceTransport SubscriberSource = "transport"
)

// Runtime 汇总模块运行期可选能力。
type Runtime struct {
	Subscriber        IEventSubscriber
	ProjectionRuntime IProjectionRuntime
	Transport         ITransport
}

// NewRuntime 创建模块运行期能力集合。
func NewRuntime(subscriber IEventSubscriber, projectionRuntime IProjectionRuntime, transport ITransport) *Runtime {
	return &Runtime{
		Subscriber:        subscriber,
		ProjectionRuntime: projectionRuntime,
		Transport:         transport,
	}
}

func (r *Runtime) EffectiveSubscriber() IEventSubscriber {
	subscriber, _ := r.EffectiveSubscriberWithSource()
	return subscriber
}

// EffectiveSubscriberWithSource 返回事件订阅能力及其来源。
//
// 说明：判定须同时排除 typed-nil（接口非 nil、底层值为 nil），
// 否则 nil 接收者会被当作有效订阅者，在 Subscribe 时 panic。
func (r *Runtime) EffectiveSubscriberWithSource() (IEventSubscriber, SubscriberSource) {
	if r == nil {
		return nil, SubscriberSourceNone
	}
	if !runtimeutil.IsTypedNil(r.Subscriber) {
		return r.Subscriber, SubscriberSourceEventBus
	}
	if !runtimeutil.IsTypedNil(r.Transport) {
		return r.Transport, SubscriberSourceTransport
	}
	return nil, SubscriberSourceNone
}
