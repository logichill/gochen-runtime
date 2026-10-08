package capability

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	"gochen/errors"
	"gochen/eventing"
	eventbus "gochen/eventing/bus"
	"gochen/eventing/projection"
	"gochen/eventing/registry"
	"gochen/eventing/store"
	"gochen/eventing/upcast"
	"gochen/messaging"
	"gochen/messaging/transport/memory"
)

func TestModuleRuntime_Start_EventHandlersRequireSubscribeCapability(t *testing.T) {
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, nil))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })

	err := runtime.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Start to fail when no event bus and no transport are configured")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}

	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected *errors.AppError, got: %T", err)
	}
	if got := appErr.Details()["module"]; got != "test" {
		t.Fatalf("expected module detail 'test', got: %#v", got)
	}
	if got, ok := appErr.Details()["event_handlers_count"].(int); !ok || got != 1 {
		t.Fatalf("expected event_handlers_count detail 1, got: %#v", appErr.Details()["event_handlers_count"])
	}
	if got := appErr.Details()["subscriber_source"]; got != string(SubscriberSourceNone) {
		t.Fatalf("expected subscriber_source none, got: %#v", got)
	}
	if got := appErr.Details()["hint"]; got == "" {
		t.Fatalf("expected diagnostic hint")
	}
}

func TestRuntime_EffectiveSubscriberWithSource_TransportFallback(t *testing.T) {
	transport := newMockTransport()
	runtime := NewRuntime(nil, nil, transport)

	subscriber, source := runtime.EffectiveSubscriberWithSource()
	if subscriber != transport {
		t.Fatalf("expected transport subscriber fallback")
	}
	if source != SubscriberSourceTransport {
		t.Fatalf("expected transport source, got %s", source)
	}
}

// nilReceiverTransport 在 Subscribe 中访问接收者字段：typed-nil 被误判为有效
// 订阅者时，会像真实实现一样在调用时 panic，用于回归 R2a-F03。
type nilReceiverTransport struct {
	subscribeTypes []string
}

func (t *nilReceiverTransport) Subscribe(_ context.Context, messageType string, _ messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	t.subscribeTypes = append(t.subscribeTypes, messageType)
	return func(context.Context) error { return nil }, nil
}

func (t *nilReceiverTransport) Start(context.Context) error { return nil }

func (t *nilReceiverTransport) Stop(context.Context) error { return nil }

// 回归 R2a-F03：typed-nil 传输/订阅者（接口非 nil、底层指针为 nil）不得被
// EffectiveSubscriberWithSource 当作有效能力返回；Subscriber 为 typed-nil 时
// 必须跳过并回退到有效 Transport。
func TestRuntime_EffectiveSubscriberWithSource_SkipsTypedNilCapabilities(t *testing.T) {
	var typedNilTransport *nilReceiverTransport
	runtime := NewRuntime(nil, nil, typedNilTransport)
	if runtime.Transport == nil {
		t.Fatal("test setup: expected a typed-nil (non-nil interface) transport")
	}
	if subscriber, source := runtime.EffectiveSubscriberWithSource(); subscriber != nil || source != SubscriberSourceNone {
		t.Fatalf("typed-nil transport exposed: subscriber=%#v source=%s", subscriber, source)
	}

	var typedNilSubscriber *nilReceiverTransport
	validTransport := newMockTransport()
	fallback := NewRuntime(typedNilSubscriber, nil, validTransport)
	if subscriber, source := fallback.EffectiveSubscriberWithSource(); subscriber != validTransport || source != SubscriberSourceTransport {
		t.Fatalf("typed-nil subscriber must fall back to transport: subscriber=%#v source=%s", subscriber, source)
	}

	allNil := NewRuntime(typedNilSubscriber, nil, (*nilReceiverTransport)(nil))
	if subscriber, source := allNil.EffectiveSubscriberWithSource(); subscriber != nil || source != SubscriberSourceNone {
		t.Fatalf("all-typed-nil runtime exposed: subscriber=%#v source=%s", subscriber, source)
	}
}

// 回归 R2a-F03：typed-nil 传输/订阅者不得让事件处理器启动路径在 nil 接收者上
// panic；Start 必须返回明确的 InvalidInput 错误（与投影/运行时组件路径一致）。
func TestModuleRuntime_Start_TypedNilCapabilitiesReturnErrorInsteadOfPanic(t *testing.T) {
	var typedNilTransport *nilReceiverTransport
	var typedNilSubscriber *nilReceiverTransport
	for _, tc := range []struct {
		name    string
		runtime *Runtime
	}{
		{name: "typed-nil transport", runtime: NewRuntime(nil, nil, typedNilTransport)},
		{name: "typed-nil subscriber", runtime: NewRuntime(typedNilSubscriber, nil, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			moduleRuntime := NewModuleRuntime("test", tc.runtime)
			moduleRuntime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })

			err := moduleRuntime.Start(context.Background(), nil)
			if !errors.Is(err, errors.InvalidInput) {
				t.Fatalf("Start error = %v, want InvalidInput", err)
			}
		})
	}
}

func TestModuleRuntime_Start_FailFast_WhenHandlerHasNoValidMessageTypes(t *testing.T) {
	transport := newMockTransport()
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, transport))
	runtime.AddEventHandler(0, func() (any, error) { return &emptyTypeEventHandler{}, nil })

	err := runtime.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("expected start error, got nil")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input error, got %v", err)
	}
	if !strings.Contains(err.Error(), "no valid message types") {
		t.Fatalf("expected error to mention message types, got %v", err)
	}
}

func TestModuleRuntime_Start_IncludesSubscriptionCleanupErrors(t *testing.T) {
	cleanupErr := stderrors.New("unsubscribe failed")
	transport := &unsubscribeFailTransport{
		mockTransport:  newMockTransport(),
		unsubscribeErr: cleanupErr,
	}
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddEventHandler(1, func() (any, error) { return &emptyTypeEventHandler{}, nil })

	startErr := runtime.Start(context.Background(), nil)
	if startErr == nil {
		t.Fatal("expected start error, got nil")
	}
	if !errors.Is(startErr, errors.InvalidInput) {
		t.Fatalf("expected invalid input start error, got %v", startErr)
	}
	stopErr := runtime.Stop(context.Background(), nil)
	if !stderrors.Is(stopErr, cleanupErr) {
		t.Fatalf("expected explicit cleanup to expose unsubscribe error, got %v", stopErr)
	}
}

func TestModuleRuntime_StartHandlerFailuresRetainEarlierSubscriptions(t *testing.T) {
	resolveErr := stderrors.New("handler resolve failed")
	subscribeErr := stderrors.New("handler subscribe failed")
	cases := []struct {
		name  string
		setup func(*ModuleRuntime, *mockTransport)
	}{
		{
			name: "resolve",
			setup: func(runtime *ModuleRuntime, _ *mockTransport) {
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return nil, resolveErr })
			},
		},
		{
			name: "type",
			setup: func(runtime *ModuleRuntime, _ *mockTransport) {
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return struct{}{}, nil })
			},
		},
		{
			name: "subscribe",
			setup: func(runtime *ModuleRuntime, transport *mockTransport) {
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return &testEventHandler{msgType: "fail.event"}, nil })
				// 该场景的 transport 由调用方替换。
				_ = transport
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := newMockTransport()
			var runtime *ModuleRuntime
			if tc.name == "subscribe" {
				runtime = NewModuleRuntime("test", NewRuntime(nil, nil, &subscribeErrorTransport{
					mockTransport: transport,
					failType:      "fail.event",
					err:           subscribeErr,
				}))
			} else {
				runtime = NewModuleRuntime("test", NewRuntime(nil, nil, transport))
			}
			tc.setup(runtime, transport)
			if err := runtime.Start(context.Background(), nil); err == nil {
				t.Fatal("expected handler startup failure")
			}
			if err := runtime.Stop(context.Background(), nil); err != nil {
				t.Fatalf("cleanup failed: %v", err)
			}
			if len(transport.Subscriptions["test.event"]) != 1 {
				t.Fatalf("earlier subscription was not retained, got %d", len(transport.Subscriptions["test.event"]))
			}
		})
	}
}

type subscribeErrorTransport struct {
	*mockTransport
	failType string
	err      error
}

type subscribeAndUnsubscribeErrorTransport struct {
	*mockTransport
	failType       string
	subscribeErr   error
	unsubscribeErr error
}

func (t *subscribeAndUnsubscribeErrorTransport) Subscribe(ctx context.Context, messageType string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	if messageType == t.failType {
		return nil, t.subscribeErr
	}
	t.mockTransport.Subscriptions[messageType] = append(t.mockTransport.Subscriptions[messageType], handler)
	return func(context.Context) error { return t.unsubscribeErr }, nil
}

func (t *subscribeErrorTransport) Subscribe(ctx context.Context, messageType string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	if messageType == t.failType {
		return nil, t.err
	}
	return t.mockTransport.Subscribe(ctx, messageType, handler)
}

func TestModuleRuntime_StartFailureCleanupRetryMatrix(t *testing.T) {
	startErr := stderrors.New("startup stage failed")
	cleanupErr := stderrors.New("cleanup stage failed")

	type scenario struct {
		name  string
		build func() (*ModuleRuntime, func() bool, func(), error)
	}
	scenarios := []scenario{
		{
			name: "handler resolve",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				transport := &unsubscribeFailTransport{mockTransport: newMockTransport(), unsubscribeErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, transport))
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return nil, startErr })
				return runtime, func() bool { return transport.unsubscribeErr != nil }, func() { transport.unsubscribeErr = nil }, startErr
			},
		},
		{
			name: "handler type",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				transport := &unsubscribeFailTransport{mockTransport: newMockTransport(), unsubscribeErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, transport))
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return struct{}{}, nil })
				return runtime, func() bool { return transport.unsubscribeErr != nil }, func() { transport.unsubscribeErr = nil }, nil
			},
		},
		{
			name: "handler subscribe",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				transport := &subscribeAndUnsubscribeErrorTransport{
					mockTransport: newMockTransport(), failType: "fail.event", subscribeErr: startErr, unsubscribeErr: cleanupErr,
				}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, transport))
				runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
				runtime.AddEventHandler(1, func() (any, error) { return &testEventHandler{msgType: "fail.event"}, nil })
				return runtime, func() bool { return transport.unsubscribeErr != nil }, func() { transport.unsubscribeErr = nil }, startErr
			},
		},
		{
			name: "projection register",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				pm := &failingProjectionRuntime{registerErr: startErr, stopErr: cleanupErr, registerKeepsOwnership: true}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, pm, nil))
				runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
				return runtime, func() bool { return pm.stopErr != nil }, func() { pm.stopErr = nil }, startErr
			},
		},
		{
			name: "projection start",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				pm := &failingProjectionRuntime{startErr: startErr, stopErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, pm, nil))
				runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
				return runtime, func() bool { return pm.stopErr != nil }, func() { pm.stopErr = nil }, startErr
			},
		},
		{
			name: "projection resume",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				pm := &resumingFailingProjectionRuntime{
					failingProjectionRuntime: failingProjectionRuntime{stopErr: cleanupErr},
					resumeErr:                startErr,
				}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, pm, nil))
				runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
				return runtime, func() bool { return pm.stopErr != nil }, func() { pm.stopErr = nil }, startErr
			},
		},
		{
			name: "component resolve",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				component := &partialStartComponent{stopErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, nil))
				runtime.AddRuntimeComponent(0, func() (any, error) { return component, nil })
				runtime.AddRuntimeComponent(1, func() (any, error) { return nil, startErr })
				return runtime, func() bool { return component.stopCount > 0 }, func() { component.stopErr = nil }, startErr
			},
		},
		{
			name: "component start",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				component := &partialStartComponent{startErr: startErr, stopErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, nil))
				runtime.AddRuntimeComponent(0, func() (any, error) { return component, nil })
				return runtime, func() bool { return component.stopCount > 0 }, func() { component.stopErr = nil }, startErr
			},
		},
		{
			name: "on start",
			build: func() (*ModuleRuntime, func() bool, func(), error) {
				component := &partialStartComponent{stopErr: cleanupErr}
				runtime := NewModuleRuntime("matrix", NewRuntime(nil, nil, nil))
				runtime.AddRuntimeComponent(0, func() (any, error) { return component, nil })
				return runtime, func() bool { return component.stopCount > 0 }, func() { component.stopErr = nil }, startErr
			},
		},
	}

	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			runtime, cleanupObserved, allowRetry, wantStartErr := tc.build()
			var startError error
			if tc.name == "on start" {
				startError = runtime.Start(context.Background(), func(context.Context) error { return startErr })
			} else {
				startError = runtime.Start(context.Background(), nil)
			}
			if startError == nil || (wantStartErr != nil && !stderrors.Is(startError, wantStartErr)) {
				t.Fatalf("start error = %v, want %v", startError, wantStartErr)
			}
			firstStop := runtime.Stop(context.Background(), nil)
			if !stderrors.Is(firstStop, cleanupErr) {
				t.Fatalf("first stop error = %v, want cleanup error %v", firstStop, cleanupErr)
			}
			observed := cleanupObserved()
			allowRetry()
			if err := runtime.Stop(context.Background(), nil); err != nil {
				t.Fatalf("cleanup retry failed: %v", err)
			}
			if !observed && tc.name != "handler subscribe" {
				t.Fatal("cleanup stage was not exercised")
			}
		})
	}
}

func TestModuleRuntime_StopRetriesEveryCleanupCategory(t *testing.T) {
	startErr := stderrors.New("on start failed")
	unsubscribeErr := stderrors.New("unsubscribe failed")
	componentStopErr := stderrors.New("component stop failed")
	projectionStopErr := stderrors.New("projection stop failed")
	onStopErr := stderrors.New("on stop failed")

	transport := &subscribeAndUnsubscribeErrorTransport{
		mockTransport:  newMockTransport(),
		unsubscribeErr: unsubscribeErr,
	}
	projectionRuntime := &failingProjectionRuntime{stopErr: projectionStopErr}
	component := &partialStartComponent{stopErr: componentStopErr}
	runtime := NewModuleRuntime("all-cleanup", NewRuntime(nil, projectionRuntime, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddRuntimeComponent(2, func() (any, error) { return component, nil })
	if err := runtime.Start(context.Background(), func(context.Context) error { return startErr }); !stderrors.Is(err, startErr) {
		t.Fatalf("Start error = %v, want %v", err, startErr)
	}

	onStopCalls := 0
	onStop := func(context.Context) error {
		onStopCalls++
		if onStopCalls == 1 {
			return onStopErr
		}
		return nil
	}
	first := runtime.Stop(context.Background(), onStop)
	if !stderrors.Is(first, unsubscribeErr) || !stderrors.Is(first, onStopErr) {
		t.Fatalf("first cleanup = %v, want unsubscribe and OnStop errors", first)
	}
	if component.stopCount != 0 || projectionRuntime.releaseAttempts != 0 {
		t.Fatal("dependencies must remain untouched while OnStop is failing")
	}

	second := runtime.Stop(context.Background(), onStop)
	if !stderrors.Is(second, unsubscribeErr) || !stderrors.Is(second, componentStopErr) || !stderrors.Is(second, projectionStopErr) {
		t.Fatalf("second cleanup = %v, want all remaining cleanup errors", second)
	}
	transport.unsubscribeErr = nil
	component.stopErr = nil
	projectionRuntime.stopErr = nil
	if err := runtime.Stop(context.Background(), onStop); err != nil {
		t.Fatalf("final cleanup retry failed: %v", err)
	}
	if onStopCalls != 3 || component.stopCount != 2 || projectionRuntime.releaseAttempts != 2 || len(projectionRuntime.released) != 1 {
		t.Fatalf("cleanup retry counts = onStop:%d component:%d releaseAttempts:%d released:%d",
			onStopCalls, component.stopCount, projectionRuntime.releaseAttempts, len(projectionRuntime.released))
	}
}

type unsubscribeFailTransport struct {
	*mockTransport
	unsubscribeErr error
}

func (t *unsubscribeFailTransport) Subscribe(ctx context.Context, messageType string, handler messaging.IMessageHandler) (messaging.UnsubscribeFunc, error) {
	t.Subscriptions[messageType] = append(t.Subscriptions[messageType], handler)
	return func(context.Context) error {
		return t.unsubscribeErr
	}, nil
}

func TestModuleRuntime_Projections(t *testing.T) {
	pm := &mockProjectionManager{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection2(), nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if len(pm.Registered) != 2 {
		t.Fatalf("expected 2 registered projections, got %d", len(pm.Registered))
	}
	if len(pm.Started) != 2 {
		t.Fatalf("expected 2 started projections, got %d", len(pm.Started))
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if len(pm.Released) != 2 {
		t.Fatalf("expected 2 released projections, got %d", len(pm.Released))
	}
	if pm.Released[0] != "p2" || pm.Released[1] != "p1" {
		t.Fatalf("expected release order p2,p1; got %v", pm.Released)
	}
}

func TestModuleRuntime_Start_ProjectionsRequireRuntime(t *testing.T) {
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	err := runtime.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Start to fail when projection runtime is missing")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

func TestModuleRuntime_Start_ResumesProjectionCheckpointBacklog(t *testing.T) {
	pm := &resumingProjectionManager{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if got, want := strings.Join(pm.Started, ","), "p1"; got != want {
		t.Fatalf("unexpected started projections: %s", got)
	}
	if got, want := strings.Join(pm.resumed, ","), "p1"; got != want {
		t.Fatalf("unexpected resumed projections: %s", got)
	}
}

func TestModuleRuntime_Start_RetainsProjectionWhenCheckpointResumeFailsUntilStop(t *testing.T) {
	pm := &resumingProjectionManager{resumeErr: errors.NewCode(errors.Database, "resume failed")}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	err := runtime.Start(context.Background(), nil)
	if !errors.Is(err, errors.Database) {
		t.Fatalf("Start error = %v, want Database", err)
	}
	if got, want := strings.Join(pm.resumed, ","), "p1"; got != want {
		t.Fatalf("unexpected resumed projections: %s", got)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("explicit cleanup after failed resume: %v", err)
	}
	if got, want := strings.Join(pm.Released, ","), "p1"; got != want {
		t.Fatalf("unexpected released projections after cleanup: %s", got)
	}
}

func TestModuleRuntime_ProjectionCleanupReleasesOwnedRegistrationsByReleaseFunction(t *testing.T) {
	pm := &mockProjectionManager{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection2(), nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if got, want := strings.Join(pm.Released, ","), "p2,p1"; got != want {
		t.Fatalf("unexpected release order: %s", got)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("second Stop failed: %v", err)
	}
	if len(pm.Released) != 2 {
		t.Fatalf("released records must be dropped after success, got %v", pm.Released)
	}
}

func TestModuleRuntime_RuntimeComponents(t *testing.T) {
	component := &testRuntimeComponent{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, nil))
	runtime.AddRuntimeComponent(0, func() (any, error) { return component, nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if component.startCount != 1 {
		t.Fatalf("expected runtime component start count 1, got %d", component.startCount)
	}
	if component.startedWith != "test" {
		t.Fatalf("expected runtime component to receive startedWith test, got %q", component.startedWith)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if component.stopCount != 1 {
		t.Fatalf("expected runtime component stop count 1, got %d", component.stopCount)
	}
}

func TestModuleRuntime_StartFailureStopsPartiallyInitializedComponent(t *testing.T) {
	startErr := stderrors.New("component start failed")
	stopErr := stderrors.New("partial component stop failed")
	component := &partialStartComponent{startErr: startErr, stopErr: stopErr}
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, nil))
	runtime.AddRuntimeComponent(0, func() (any, error) { return component, nil })

	err := runtime.Start(context.Background(), nil)
	if !stderrors.Is(err, startErr) {
		t.Fatalf("expected only start error before explicit cleanup, got %v", err)
	}
	if component.startCount != 1 || component.stopCount != 0 {
		t.Fatalf("component cleanup must wait for Stop, got start=%d stop=%d", component.startCount, component.stopCount)
	}

	if err := runtime.Stop(context.Background(), nil); !stderrors.Is(err, stopErr) {
		t.Fatalf("expected first explicit cleanup to return stop error, got %v", err)
	}
	component.stopErr = nil
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("expected retained partial component cleanup retry to succeed, got %v", err)
	}
	if component.stopCount != 2 {
		t.Fatalf("expected exactly one cleanup retry, got %d stops", component.stopCount)
	}
}

func TestModuleRuntime_ProjectionFailureCleansEarlierSubscriptions(t *testing.T) {
	startErr := stderrors.New("projection start failed")
	log := []string{}
	transport := newMockTransport()
	transport.Log = &log
	projectionRuntime := &failingProjectionRuntime{startErr: startErr}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection1(), nil })

	err := runtime.Start(context.Background(), nil)
	if !stderrors.Is(err, startErr) {
		t.Fatalf("Start error = %v, want projection start error", err)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop cleanup failed: %v", err)
	}
	if got, want := strings.Join(log, ","), "subscribe:test.event,unsubscribe:test.event"; got != want {
		t.Fatalf("cleanup log = %q, want %q", got, want)
	}
	if len(projectionRuntime.released) != 1 || projectionRuntime.released[0] != "p1" {
		t.Fatalf("projection cleanup = %v, want [p1]", projectionRuntime.released)
	}
}

func TestModuleRuntime_ProjectionRegistrationFailureWithoutReleaseFunctionSkipsCleanup(t *testing.T) {
	registerErr := stderrors.New("projection registration failed before allocation")
	projectionRuntime := &failingProjectionRuntime{registerErr: registerErr}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	err := runtime.Start(context.Background(), nil)
	if !stderrors.Is(err, registerErr) {
		t.Fatalf("Start error = %v, want registration error", err)
	}
	if len(projectionRuntime.registered) != 1 || projectionRuntime.releaseAttempts != 0 {
		t.Fatalf("registration/cleanup counts = %d/%d, want 1/0 before Stop",
			len(projectionRuntime.registered), projectionRuntime.releaseAttempts)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop cleanup failed: %v", err)
	}
	if projectionRuntime.releaseAttempts != 0 {
		t.Fatalf("failed registration without release function was cleaned: %d attempts", projectionRuntime.releaseAttempts)
	}
}

func TestModuleRuntime_RejectsProjectionRuntimeWithoutReleaseFunction(t *testing.T) {
	projectionRuntime := &missingReleaseProjectionRuntime{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	err := runtime.Start(context.Background(), nil)
	if !errors.Is(err, errors.Internal) || !strings.Contains(err.Error(), "release function") {
		t.Fatalf("Start error = %v, want Internal release-function error", err)
	}
	if len(projectionRuntime.registered) != 1 || projectionRuntime.startProjection {
		t.Fatalf("contract violation must fail fast before starting: registered=%d started=%v",
			len(projectionRuntime.registered), projectionRuntime.startProjection)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop cleanup failed: %v", err)
	}
}

func TestModuleRuntime_ProjectionReleaseRetainsFailedRegistrationForRetry(t *testing.T) {
	registerErr := stderrors.New("projection registration failed after allocation")
	cleanupErr := stderrors.New("projection cleanup failed")
	projectionRuntime := &failingProjectionRuntime{registerErr: registerErr, stopErr: cleanupErr, registerKeepsOwnership: true}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })

	if err := runtime.Start(context.Background(), nil); !stderrors.Is(err, registerErr) {
		t.Fatalf("Start error = %v, want registration error", err)
	}
	if err := runtime.Stop(context.Background(), nil); !stderrors.Is(err, cleanupErr) {
		t.Fatalf("first Stop error = %v, want cleanup error", err)
	}
	projectionRuntime.stopErr = nil
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if projectionRuntime.releaseAttempts != 2 {
		t.Fatalf("cleanup attempts = %d, want two attempts", projectionRuntime.releaseAttempts)
	}
}

func TestModuleRuntime_StaleProjectionReleaseStopIsNoop(t *testing.T) {
	projectionRuntime := &failingProjectionRuntime{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 模拟该次注册在其之前已被释放、名字被其他所有者接管：本模块的 token 已过期。
	projectionRuntime.tokens["p1"] = &failingProjectionToken{name: "p1"}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("stale token Stop error = %v, want nil", err)
	}
	if len(projectionRuntime.released) != 0 {
		t.Fatalf("stale token must not release other owners' resources: %v", projectionRuntime.released)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("second Stop error = %v, want nil", err)
	}
	if projectionRuntime.releaseAttempts != 1 {
		t.Fatalf("stale record must be dropped after one release attempt, got %d", projectionRuntime.releaseAttempts)
	}
}

// TestModuleRuntime_ConflictingRegistrationDoesNotStealOwnership 回归 R1：
// 两个模块从 DI 单例解析到同一投影实例时，后注册模块的注册冲突不得让它在
// Stop 时误删先注册模块的投影，也不得破坏先注册模块自己的清理。
func TestModuleRuntime_ConflictingRegistrationDoesNotStealOwnership(t *testing.T) {
	ctx := context.Background()
	transport := memory.NewMemoryTransport(64, 1)
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	defer func() {
		if err := transport.Stop(ctx); err != nil {
			t.Errorf("stop transport: %v", err)
		}
	}()

	manager, err := projection.NewProjectionManager[int64](
		store.NewMemoryEventStore[int64](),
		eventbus.NewEventBus(messaging.NewMessageBus(transport)),
		registry.NewRegistry(),
		upcast.NewUpgraderRegistry(),
	)
	if err != nil {
		t.Fatalf("new projection manager: %v", err)
	}

	shared := &sharedTestProjection{name: "shared"}
	owner := NewModuleRuntime("owner", NewRuntime(nil, manager, nil))
	owner.AddProjection(0, func() (any, error) { return shared, nil })
	if err := owner.Start(ctx, nil); err != nil {
		t.Fatalf("owner start failed: %v", err)
	}

	thief := NewModuleRuntime("thief", NewRuntime(nil, manager, nil))
	thief.AddProjection(0, func() (any, error) { return shared, nil })
	if err := thief.Start(ctx, nil); !errors.Is(err, errors.Conflict) {
		t.Fatalf("thief start error = %v, want Conflict", err)
	}
	if err := thief.Stop(ctx, nil); err != nil {
		t.Fatalf("thief stop error = %v, want nil", err)
	}
	if _, err := manager.ProjectionStatus("shared"); err != nil {
		t.Fatalf("thief stop removed the owner's projection: %v", err)
	}

	if err := owner.Stop(ctx, nil); err != nil {
		t.Fatalf("owner stop error = %v, want nil", err)
	}
	if _, err := manager.ProjectionStatus("shared"); !errors.Is(err, errors.NotFound) {
		t.Fatalf("owner registration survived its own stop: %v", err)
	}
}

type sharedTestProjection struct {
	name string
}

func (p *sharedTestProjection) Name() string { return p.name }

func (p *sharedTestProjection) Handle(context.Context, eventing.IEvent) error { return nil }

func (p *sharedTestProjection) SupportedEventTypes() []string { return []string{"*"} }

func (p *sharedTestProjection) Rebuild(context.Context, []eventing.Event[int64]) error { return nil }

func (p *sharedTestProjection) Status() projection.ProjectionStatus {
	return projection.ProjectionStatus{Name: p.name, Status: "stopped"}
}

func TestModuleRuntime_OnStartFailureCleansAllStartedResources(t *testing.T) {
	startErr := stderrors.New("on start failed")
	log := []string{}
	transport := newMockTransport()
	transport.Log = &log
	projectionRuntime := newMockProjectionManager()
	projectionRuntime.Log = &log
	component := &testRuntimeComponent{log: &log}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddRuntimeComponent(2, func() (any, error) { return component, nil })

	err := runtime.Start(context.Background(), func(context.Context) error { return startErr })
	if !stderrors.Is(err, startErr) {
		t.Fatalf("Start error = %v, want OnStart error", err)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop cleanup failed: %v", err)
	}
	if got, want := strings.Join(log, ","), "subscribe:test.event,projection:register:p1,projection:start:p1,runtime:start,unsubscribe:test.event,runtime:stop,projection:release:p1"; got != want {
		t.Fatalf("cleanup log = %q, want %q", got, want)
	}
}

func TestModuleRuntime_OnStopFailureRetainsDependenciesForRetry(t *testing.T) {
	hookErr := stderrors.New("on stop failed")
	log := []string{}
	transport := newMockTransport()
	transport.Log = &log
	projectionRuntime := newMockProjectionManager()
	projectionRuntime.Log = &log
	component := &testRuntimeComponent{log: &log}
	runtime := NewModuleRuntime("test", NewRuntime(nil, projectionRuntime, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddRuntimeComponent(2, func() (any, error) { return component, nil })
	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	hookCalls := 0
	stopHook := func(context.Context) error {
		hookCalls++
		if hookCalls == 1 {
			return hookErr
		}
		return nil
	}
	if err := runtime.Stop(context.Background(), stopHook); !stderrors.Is(err, hookErr) {
		t.Fatalf("first Stop error = %v, want OnStop error", err)
	}
	if component.stopCount != 0 || len(projectionRuntime.Released) != 0 {
		t.Fatalf("dependencies were stopped despite OnStop failure: component=%d projections=%v", component.stopCount, projectionRuntime.Released)
	}
	if err := runtime.Stop(context.Background(), stopHook); err != nil {
		t.Fatalf("retry Stop failed: %v", err)
	}
	if component.stopCount != 1 || strings.Join(projectionRuntime.Released, ",") != "p1" {
		t.Fatalf("retry cleanup = component:%d projections:%v, want 1/p1", component.stopCount, projectionRuntime.Released)
	}
}

func TestModuleRuntime_Start_SubscribeMultiTypes(t *testing.T) {
	transport := newMockTransport()
	runtime := NewModuleRuntime("test", NewRuntime(nil, nil, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newMultiTypeEventHandler(), nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if len(transport.Subscriptions["a.event"]) != 1 {
		t.Errorf("expected 1 subscription for 'a.event', got %d", len(transport.Subscriptions["a.event"]))
	}
	if len(transport.Subscriptions["b.event"]) != 1 {
		t.Errorf("expected 1 subscription for 'b.event', got %d", len(transport.Subscriptions["b.event"]))
	}
}

type failingProjectionRuntime struct {
	registered  []string
	started     []string
	released    []string
	registerErr error
	startErr    error
	stopErr     error

	// registerKeepsOwnership 为 true 时模拟“注册失败但仍持有资源”：返回函数供重试释放。
	registerKeepsOwnership bool
	releaseAttempts        int
	tokens                 map[string]any
}

func (m *failingProjectionRuntime) RegisterProjectionAny(_ context.Context, p any) (messaging.UnsubscribeFunc, error) {
	named, ok := p.(interface{ Name() string })
	if !ok {
		return nil, stderrors.New("projection has no name")
	}
	name := named.Name()
	m.registered = append(m.registered, name)
	if m.registerErr != nil && !m.registerKeepsOwnership {
		return nil, m.registerErr
	}
	if m.tokens == nil {
		m.tokens = make(map[string]any)
	}
	token := &failingProjectionToken{name: name}
	m.tokens[name] = token
	return func(context.Context) error {
		m.releaseAttempts++
		if m.tokens[name] != token {
			return nil
		}
		if m.stopErr != nil {
			return m.stopErr
		}
		delete(m.tokens, name)
		m.released = append(m.released, name)
		return nil
	}, m.registerErr
}

func (m *failingProjectionRuntime) StartProjection(name string) error {
	m.started = append(m.started, name)
	return m.startErr
}

type failingProjectionToken struct {
	name string
}

type missingReleaseProjectionRuntime struct {
	registered      []string
	startProjection bool
}

func (m *missingReleaseProjectionRuntime) RegisterProjectionAny(_ context.Context, p any) (messaging.UnsubscribeFunc, error) {
	named, ok := p.(interface{ Name() string })
	if !ok {
		return nil, stderrors.New("projection has no name")
	}
	m.registered = append(m.registered, named.Name())
	return nil, nil
}

func (m *missingReleaseProjectionRuntime) StartProjection(string) error {
	m.startProjection = true
	return nil
}

type resumingFailingProjectionRuntime struct {
	failingProjectionRuntime
	resumeErr error
}

func (m *resumingFailingProjectionRuntime) ResumeFromCheckpoint(context.Context, string) error {
	return m.resumeErr
}

func TestModuleRuntime_StartStop_HooksPreserveLifecycleOrder(t *testing.T) {
	log := []string{}
	transport := newMockTransport()
	transport.Log = &log
	pm := &mockProjectionManager{Log: &log}
	component := &testRuntimeComponent{log: &log}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, transport))
	runtime.AddEventHandler(0, func() (any, error) { return newTestEventHandler(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddRuntimeComponent(2, func() (any, error) { return component, nil })

	if err := runtime.Start(context.Background(), func(context.Context) error {
		log = append(log, "hook:start")
		return nil
	}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if got, want := strings.Join(log, ","), "subscribe:test.event,projection:register:p1,projection:start:p1,runtime:start,hook:start"; got != want {
		t.Fatalf("unexpected start order: %s", got)
	}

	if err := runtime.Stop(context.Background(), func(context.Context) error {
		log = append(log, "hook:stop")
		return nil
	}); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if got, want := strings.Join(log, ","), "subscribe:test.event,projection:register:p1,projection:start:p1,runtime:start,hook:start,unsubscribe:test.event,hook:stop,runtime:stop,projection:release:p1"; got != want {
		t.Fatalf("unexpected full lifecycle order: %s", got)
	}
}

func TestJoinModuleStopErrorsNilWithoutFailures(t *testing.T) {
	if err := joinModuleStopErrors("test", nil); err != nil {
		t.Fatalf("expected nil error when no cleanup failed, got %v", err)
	}
}

// 清理错误组必须只经 Join 链展示一次：details 里再存一份会让日志重复打印每条失败原因。
func TestJoinModuleStopErrorsKeepsSingleDisplayCopy(t *testing.T) {
	first := stderrors.New("unsubscribe failed")
	second := stderrors.New("release failed")

	joined := joinModuleStopErrors("test", []error{first, second})
	if joined == nil {
		t.Fatal("expected joined error")
	}

	var appErr *errors.AppError
	if !stderrors.As(joined, &appErr) || appErr == nil {
		t.Fatalf("joined error does not expose AppError: %v", joined)
	}
	if appErr.Code() != errors.Internal {
		t.Fatalf("joined code = %q, want %q", appErr.Code(), errors.Internal)
	}
	if got := appErr.Details()["module"]; got != "test" {
		t.Fatalf("joined module context = %#v, want test", got)
	}
	if _, ok := appErr.Details()["errors"]; ok {
		t.Fatalf("cleanup errors must not be duplicated into details: %#v", appErr.Details())
	}
	if !stderrors.Is(joined, first) || !stderrors.Is(joined, second) {
		t.Fatalf("joined error lost a cleanup failure: %v", joined)
	}

	message := joined.Error()
	for _, want := range []string{"module stop errors", "unsubscribe failed", "release failed"} {
		if got := strings.Count(message, want); got != 1 {
			t.Fatalf("%q appears %d times in %q, want exactly once", want, got, message)
		}
	}
}
