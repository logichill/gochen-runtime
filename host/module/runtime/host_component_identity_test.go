package runtime

import (
	"context"
	"strings"
	"testing"

	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen/errors"
	"gochen/httpx"
)

type valueRuntimeComponent struct{ value any }

func (valueRuntimeComponent) Start(context.Context) error { return nil }

type customComponentModule struct {
	*module.BaseModule
	component capability.IRuntimeComponent
	starts    int
}

func (m *customComponentModule) RuntimeComponents() ([]capability.IRuntimeComponent, error) {
	return []capability.IRuntimeComponent{m.component}, nil
}
func (m *customComponentModule) Start(context.Context) (module.ModuleStopFunc, error) {
	m.starts++
	return nil, nil
}

type identityEntryValidator struct{ activated bool }

func (*identityEntryValidator) ValidateEntries(context.Context, httpx.IServer) error { return nil }
func (v *identityEntryValidator) ActivateEntries() error                             { v.activated = true; return nil }

func TestCustomRuntimeProviderRejectsValueBeforeActivation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component capability.IRuntimeComponent
	}{
		{"comparable value", valueRuntimeComponent{value: 1}},
		{"interface holding slice", valueRuntimeComponent{value: []int{1}}},
		{"typed nil pointer", (*valueRuntimeComponent)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &customComponentModule{BaseModule: module.NewBaseModule("custom-worker", "custom-worker"), component: tc.component}
			validator := &identityEntryValidator{}
			h := NewHost([]module.ModuleCtor{func() (module.IModule, error) { return m, nil }}, config.WithEntryValidator(validator))
			if err := h.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			err := h.StartBackground(context.Background())
			if !errors.Is(err, errors.InvalidInput) || !strings.Contains(err.Error(), "pointer identity") {
				t.Fatalf("expected pointer identity rejection, got %v", err)
			}
			var appErr *errors.AppError
			if !errors.As(err, &appErr) || appErr.Details()["module"] != "custom-worker" {
				t.Fatalf("missing module context: %v", err)
			}
			if validator.activated || m.starts != 0 {
				t.Fatal("invalid component activated before identity validation")
			}
		})
	}
}
