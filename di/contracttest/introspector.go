// Package contracttest 提供 DI 实现共享契约测试。
package contracttest

import (
	"reflect"
	"sync/atomic"
	"testing"

	"gochen-runtime/di"
)

type introspectorService struct{}

// RunIntrospectorContractTests 验证 IIntrospector 的公共契约。
func RunIntrospectorContractTests(t *testing.T, newContainer func() interface {
	di.IRegistry
	di.IIntrospector
}) {
	t.Helper()

	t.Run("empty snapshot is non-nil", func(t *testing.T) {
		t.Helper()
		container := newContainer()
		if container == nil {
			t.Fatal("newContainer returned nil")
		}
		if got := container.ListRegisteredTypes(); got == nil {
			t.Fatal("RegisteredTypes returned nil map")
		}
	})

	t.Run("snapshot uses TypeKey without instantiating", func(t *testing.T) {
		t.Helper()
		container := newContainer()
		serviceType := reflect.TypeOf((*introspectorService)(nil))
		var created atomic.Int64
		if err := container.RegisterSingleton(serviceType, di.NewFactory(func() *introspectorService {
			created.Add(1)
			return &introspectorService{}
		})); err != nil {
			t.Fatalf("RegisterSingleton failed: %v", err)
		}

		types := container.ListRegisteredTypes()
		if types == nil {
			t.Fatal("RegisteredTypes returned nil map")
		}
		key := di.TypeKey(serviceType)
		if got := types[key]; got != serviceType {
			t.Fatalf("RegisteredTypes[%q] = %v, want %v", key, got, serviceType)
		}
		if got := created.Load(); got != 0 {
			t.Fatalf("RegisteredTypes instantiated service %d times", got)
		}
	})
}
