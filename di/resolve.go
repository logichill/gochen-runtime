package di

import (
	"reflect"

	"gochen/errors"
)

// Resolve 按泛型类型解析依赖。
func Resolve[T any](resolver IResolver) (T, error) {
	var zero T
	if resolver == nil {
		return zero, errors.NewCode(errors.InvalidInput, "resolver is nil")
	}
	serviceType := reflect.TypeFor[T]()
	inst, err := resolver.Resolve(serviceType)
	if err != nil {
		return zero, err
	}
	typed, ok := inst.(T)
	if !ok {
		return zero, errors.NewCode(errors.Internal, "resolved dependency has unexpected type").
			WithContext("service_type", TypeKey(serviceType))
	}
	return typed, nil
}
