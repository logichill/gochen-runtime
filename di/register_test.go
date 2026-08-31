package di_test

import (
	"gochen-runtime/di"
	"gochen-runtime/di/basic"
	"testing"

	"gochen/errors"
)

type IRegisterTestIface interface {
	Foo() string
}

type registerTestImpl struct{}

func (*registerTestImpl) Foo() string { return "ok" }

func TestRegisterInstance_Generic(t *testing.T) {
	c := basic.New()

	if err := di.RegisterInstance[IRegisterTestIface](c, &registerTestImpl{}); err != nil {
		t.Fatalf("RegisterInstance failed: %v", err)
	}

	var got IRegisterTestIface
	if err := c.Invoke(di.NewInvocation(func(x IRegisterTestIface) { got = x })); err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}
	if got == nil || got.Foo() != "ok" {
		t.Fatalf("unexpected resolved instance: %#v", got)
	}
}

func TestRegisterInstance_Generic_TypedNilRejected(t *testing.T) {
	c := basic.New()

	var impl *registerTestImpl = nil
	if err := di.RegisterInstance[IRegisterTestIface](c, impl); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got: %v", err)
	}
}

func TestRegisterSingleton_Generic(t *testing.T) {
	c := basic.New()

	if err := di.RegisterSingleton[IRegisterTestIface](c, func() IRegisterTestIface {
		return &registerTestImpl{}
	}); err != nil {
		t.Fatalf("RegisterSingleton failed: %v", err)
	}

	got, err := di.Resolve[IRegisterTestIface](c)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if got == nil || got.Foo() != "ok" {
		t.Fatalf("unexpected resolved instance: %#v", got)
	}
}

func TestRegisterSingleton_Generic_InputValidation(t *testing.T) {
	c := basic.New()

	if err := di.RegisterSingleton[IRegisterTestIface](nil, func() IRegisterTestIface {
		return &registerTestImpl{}
	}); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for nil registry, got: %v", err)
	}
	if err := di.RegisterSingleton[IRegisterTestIface](c, func() string { return "bad" }); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput for non-assignable factory, got: %v", err)
	}
}
