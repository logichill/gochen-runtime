package authz_test

import (
	"context"
	"reflect"
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/assembly"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/runtime"
	"gochen-runtime/internal/testutil"
	"gochen/auth"
	"gochen/messaging"
)

type eventProbe struct {
	types []string
	calls int
}

func (h *eventProbe) Type() string                                     { return "probe" }
func (h *eventProbe) EventTypes() []string                             { return h.types }
func (h *eventProbe) Handle(context.Context, messaging.IMessage) error { h.calls++; return nil }

type boundEventProbe struct{ *eventProbe }

func (h *boundEventProbe) AuthorizationBinding(messageType string) (auth.EntryBinding, error) {
	// 修改公开类型资料后，实际订阅仍须使用门禁取得的冻结集合。
	h.types = []string{"injected.event"}
	return auth.EntryBinding{ResourceAccess: auth.ResourceAccessNone, Source: "in-memory event probe " + messageType, AllowReason: "subscription liveness counter"}, nil
}

func TestHostCollectsEventSubscriptionsBeforeAnySubscription(t *testing.T) {
	for _, test := range []struct {
		name              string
		bind, secondGate  bool
		wantSubscriptions int
	}{{"missing_binding", false, false, 0}, {"second_gate_rejection", true, true, 0}, {"validated", true, false, 2}} {
		t.Run(test.name, func(t *testing.T) {
			probe := &eventProbe{types: []string{"first.event", "second.event", "first.event"}}
			var handler messaging.IMessageHandler = probe
			if test.bind {
				handler = &boundEventProbe{probe}
			}
			transport := testutil.NewMockTransport()
			registry := authz.NewEntryRegistry()
			resolves := 0
			desc := assembly.ModuleDescriptor{ID: "events", Name: "Events", Registrations: []assembly.Registration{{ServiceType: reflect.TypeFor[messaging.IMessageHandler](), Factory: di.NewFactory(func() messaging.IMessageHandler { resolves++; return handler }), Roles: []assembly.Role{assembly.RoleEventHandler}}}}
			opts := []config.Option{config.WithDisableHealthRoute(true), config.WithTransport(transport), config.WithEntryValidator(registry)}
			if test.secondGate {
				opts = append(opts, config.WithEntryValidator(rejectingEntryValidator{}))
			}
			host := runtime.NewHost([]module.ModuleCtor{func() (module.IModule, error) { return assembly.NewModule(desc), nil }}, opts...)
			if err := host.Prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			err := host.StartBackground(t.Context())
			if (test.wantSubscriptions > 0) != (err == nil) || len(transport.Subscriptions) != test.wantSubscriptions || resolves != 1 || probe.calls != 0 {
				t.Fatalf("start=%v subscriptions=%+v resolves=%d calls=%d", err, transport.Subscriptions, resolves, probe.calls)
			}
			report := registry.Report()
			if len(report) != 2 {
				t.Fatalf("actual subscription report=%+v", report)
			}
			for _, entry := range report {
				if entry.Entry.Kind != "event" || test.bind && (entry.Module != "events" || entry.Class != auth.EntryAllowed) || !test.bind && entry.Class != auth.EntryInvalid {
					t.Fatalf("wrong event classification: %+v", entry)
				}
			}
			if test.wantSubscriptions > 0 {
				for _, messageType := range []string{"first.event", "second.event"} {
					if len(transport.Subscriptions[messageType]) != 1 || transport.Subscriptions[messageType][0] != handler {
						t.Fatalf("subscription differs from actual source: %+v", transport.Subscriptions)
					}
				}
				if err := host.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
