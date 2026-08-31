package di_test

import (
	"testing"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
)

func TestRegistryOnlyDoesNotExposeFullContainer(t *testing.T) {
	c := basic.New()
	registry := di.RegistryOnly(c)

	if _, ok := registry.(di.IContainer); ok {
		t.Fatalf("registry view must not expose full container")
	}
	if err := di.RegisterInstance[string](registry, "value"); err != nil {
		t.Fatalf("RegisterInstance failed: %v", err)
	}
}

func TestResolverOnlyDoesNotExposeFullContainer(t *testing.T) {
	c := basic.New()
	if err := di.RegisterInstance[string](c, "value"); err != nil {
		t.Fatalf("RegisterInstance failed: %v", err)
	}

	resolver := di.ResolverOnly(c)
	if _, ok := resolver.(di.IContainer); ok {
		t.Fatalf("resolver view must not expose full container")
	}
	got, err := di.Resolve[string](resolver)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if got != "value" {
		t.Fatalf("expected value, got %q", got)
	}
}
