package authz_test

import (
	"context"
	"reflect"
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/host/module/assembly"
	"gochen-runtime/host/module/runtime"
	"gochen/auth"
	"gochen/errors"
	"gochen/httpx"
	"gochen/process/task"
)

type entryWorker struct {
	tasks *task.Registry
	bind  bool
	calls int
}

func (w *entryWorker) AuthorizationEntries(context.Context) ([]authz.EntryRegistration, error) {
	source, err := authz.NewHandlerEntries("worker", w.tasks)
	if err != nil {
		return nil, err
	}
	registration := authz.EntryRegistration{Source: source}
	if w.bind {
		registration.Bindings = []auth.EntryBinding{{Entry: auth.Entry{Kind: "worker", Key: "worker:probe"}, ResourceAccess: auth.ResourceAccessNone, Source: "in-memory worker liveness counter", AllowReason: "supervisor liveness"}}
	}
	return []authz.EntryRegistration{registration}, nil
}

func (w *entryWorker) Start(ctx context.Context) error { return w.tasks.Run(ctx, "worker:probe") }
func (w *entryWorker) Stop(ctx context.Context) error  { return w.tasks.Stop(ctx) }

type rejectingEntryValidator struct{}

func (rejectingEntryValidator) ValidateEntries(context.Context, httpx.IServer) error {
	return errors.NewCode(errors.InvalidInput, "second gate failed")
}

func TestHostCollectsActualWorkerBeforeStartingAndActivatesAfterAllGates(t *testing.T) {
	for _, test := range []struct {
		name       string
		bind       bool
		secondGate bool
		wantCalls  int
	}{{"missing_binding", false, false, 0}, {"second_gate_rejection", true, true, 0}, {"validated", true, false, 1}} {
		t.Run(test.name, func(t *testing.T) {
			worker := &entryWorker{bind: test.bind}
			var err error
			worker.tasks, err = task.NewRegistry(task.NewTaskSupervisor("worker"))
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.tasks.Register("worker:probe", func(context.Context) error { worker.calls++; return nil }); err != nil {
				t.Fatal(err)
			}
			registry := authz.NewEntryRegistry()
			resolves := 0
			desc := assembly.ModuleDescriptor{ID: "worker", Name: "Worker", Registrations: []assembly.Registration{{
				ServiceType: reflect.TypeFor[*entryWorker](),
				Factory:     di.NewFactory(func() (*entryWorker, error) { resolves++; return worker, nil }),
				Roles:       []assembly.Role{assembly.RoleRuntimeComponent},
			}}}
			opts := []config.Option{config.WithDisableHealthRoute(true), config.WithEntryValidator(registry)}
			if test.secondGate {
				opts = append(opts, config.WithEntryValidator(rejectingEntryValidator{}))
			}
			host := runtime.NewHost([]module.ModuleCtor{func() (module.IModule, error) { return assembly.NewModule(desc), nil }}, opts...)
			if err := host.Prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = host.StartBackground(t.Context())
			if (test.wantCalls == 1) != (err == nil) || worker.calls != test.wantCalls || resolves != 1 {
				t.Fatalf("start=%v calls=%d resolves=%d", err, worker.calls, resolves)
			}
			if test.wantCalls == 0 {
				if err := worker.tasks.Run(t.Context(), "worker:probe"); !errors.Is(err, errors.FailedPrecondition) {
					t.Fatalf("rejected task was activated: %v", err)
				}
			}
			report := registry.Report()
			if len(report) != 1 || report[0].Entry.Key != "worker:probe" || (test.bind && report[0].Module != "worker") {
				t.Fatalf("actual report=%+v", report)
			}
			if test.wantCalls == 1 {
				if err := host.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type missingSourceWorker struct{ started bool }

func (w *missingSourceWorker) Start(context.Context) error { w.started = true; return nil }

func TestHostRejectsRuntimeComponentWithoutActualEntrySource(t *testing.T) {
	worker := &missingSourceWorker{}
	desc := assembly.ModuleDescriptor{ID: "worker", Name: "Worker", Registrations: []assembly.Registration{{ServiceType: reflect.TypeFor[*missingSourceWorker](), Instance: worker, Roles: []assembly.Role{assembly.RoleRuntimeComponent}}}}
	host := runtime.NewHost([]module.ModuleCtor{func() (module.IModule, error) { return assembly.NewModule(desc), nil }}, config.WithDisableHealthRoute(true), config.WithEntryValidator(authz.NewEntryRegistry()))
	if err := host.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := host.StartBackground(t.Context()); !errors.Is(err, errors.InvalidInput) || worker.started {
		t.Fatalf("missing source start=%v, started=%v", err, worker.started)
	}
}

var _ capability.IRuntimeComponent = (*entryWorker)(nil)
