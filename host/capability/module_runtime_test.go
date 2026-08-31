package capability

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	"gochen/errors"
	"gochen/messaging"
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

	err := runtime.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("expected start error, got nil")
	}
	var appErr *errors.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("expected AppError, got %T", err)
	}
	cleanupErrs, ok := appErr.Details()["cleanup_errors"].([]error)
	if !ok || len(cleanupErrs) != 1 {
		t.Fatalf("expected one cleanup error, got %#v", appErr.Details()["cleanup_errors"])
	}
	if !stderrors.Is(cleanupErrs[0], cleanupErr) {
		t.Fatalf("expected cleanup error to wrap unsubscribe failure, got %v", cleanupErrs[0])
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
	if len(pm.Stopped) != 2 {
		t.Fatalf("expected 2 stopped projections, got %d", len(pm.Stopped))
	}
	if pm.Stopped[0] != "p2" || pm.Stopped[1] != "p1" {
		t.Fatalf("expected stop order p2,p1; got %v", pm.Stopped)
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

func TestModuleRuntime_Start_CleansUpProjectionWhenCheckpointResumeFails(t *testing.T) {
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
	if got, want := strings.Join(pm.Stopped, ","), "p1"; got != want {
		t.Fatalf("unexpected stopped projections: %s", got)
	}
}

func TestModuleRuntime_ProjectionCleanupPrefersUnregisterOverStop(t *testing.T) {
	pm := &unregisteringProjectionManager{}
	runtime := NewModuleRuntime("test", NewRuntime(nil, pm, nil))
	runtime.AddProjection(0, func() (any, error) { return newTestProjection1(), nil })
	runtime.AddProjection(1, func() (any, error) { return newTestProjection2(), nil })

	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := runtime.Stop(context.Background(), nil); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if len(pm.Stopped) != 0 {
		t.Fatalf("expected StopProjection not to be called when unregister is available, got %v", pm.Stopped)
	}
	if got, want := strings.Join(pm.unregistered, ","), "p2,p1"; got != want {
		t.Fatalf("unexpected unregister order: %s", got)
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
	if got, want := strings.Join(log, ","), "subscribe:test.event,projection:register:p1,projection:start:p1,runtime:start,hook:start,unsubscribe:test.event,hook:stop,runtime:stop,projection:stop:p1"; got != want {
		t.Fatalf("unexpected full lifecycle order: %s", got)
	}
}
