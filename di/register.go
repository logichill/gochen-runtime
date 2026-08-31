package di

import (
	"reflect"

	"gochen/errors"
)

// RegisterSingleton 按泛型类型注册单例工厂。
func RegisterSingleton[T any](registry IRegistry, factory any) error {
	if registry == nil {
		return errors.NewCode(errors.InvalidInput, "registry is nil")
	}
	t := reflect.TypeFor[T]()
	return registry.RegisterSingleton(t, NewFactory(factory))
}

// RegisterTransient 按泛型类型注册瞬态工厂。
func RegisterTransient[T any](registry IRegistry, factory any) error {
	if registry == nil {
		return errors.NewCode(errors.InvalidInput, "registry is nil")
	}
	t := reflect.TypeFor[T]()
	return registry.RegisterTransient(t, NewFactory(factory))
}

// RegisterInstance 按泛型类型注册实例。
func RegisterInstance[T any](registry IRegistry, instance T) error {
	if registry == nil {
		return errors.NewCode(errors.InvalidInput, "registry is nil")
	}
	t := reflect.TypeFor[T]()

	v := reflect.ValueOf(instance)
	if !v.IsValid() {
		return errors.NewCode(errors.InvalidInput, "instance is nil")
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return errors.NewCode(errors.InvalidInput, "instance is nil")
		}
	}

	if !v.Type().AssignableTo(t) {
		return errors.NewCode(errors.InvalidInput, "instance type is not assignable to target type").
			WithContext("target_type", TypeKey(t)).
			WithContext("instance_type", TypeKey(v.Type())).
			WithContext("instance_type_raw", v.Type().String())
	}

	return registry.RegisterInstance(t, NewInstance(instance))
}
