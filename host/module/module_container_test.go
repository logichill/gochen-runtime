package module_test

import (
	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	. "gochen-runtime/host/module"
	"testing"
)

func TestResolveModuleContainer_WithExplicitCapabilities(t *testing.T) {
	c := basic.New()

	mc, err := ResolveModuleContainer(NewModuleInitOptions(c, c, c))
	if err != nil {
		t.Fatalf("ResolveModuleContainer returned error: %v", err)
	}
	if mc == nil {
		t.Fatalf("expected module container, got nil")
	}
	opts := NewModuleInitOptions(c, c, c)
	if _, ok := opts.Registry.(di.IContainer); ok {
		t.Fatalf("registry field must not expose full container")
	}
	if _, ok := opts.Resolver.(di.IContainer); ok {
		t.Fatalf("resolver field must not expose full container")
	}
}

func TestResolveModuleContainer_MissingCapabilities(t *testing.T) {
	_, err := ResolveModuleContainer(ModuleInitOptions{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
