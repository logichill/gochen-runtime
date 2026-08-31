package basic

import (
	"fmt"
	"reflect"
	"runtime/debug"

	"gochen/errors"
)

// createInstance 执行工厂函数并返回实例。
func (c *Container) createInstance(factory any, state *resolutionState) (instance any, err error) {
	defer func() {
		if r := recover(); r != nil {
			instance = nil
			err = errors.NewCode(errors.Internal, "factory panicked").
				WithContext("panic", fmt.Sprint(r)).
				WithContext("stack", string(debug.Stack()))
		}
	}()

	if factory == nil {
		return nil, errors.NewCode(errors.InvalidInput, "factory cannot be nil")
	}
	if groupFactory, ok := factory.(groupOutputFactory); ok {
		v, err := groupFactory.group.get(groupFactory.index, state)
		if err != nil {
			return nil, err
		}
		if !v.IsValid() {
			return nil, nil
		}
		return v.Interface(), nil
	}

	fv := reflect.ValueOf(factory)
	ft := fv.Type()
	if ft.Kind() != reflect.Func {
		return factory, nil
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	switch ft.NumOut() {
	case 0:
		return nil, errors.NewCode(errors.Internal, "factory function has no return value")
	case 1:
		if ft.Out(0).Implements(errorType) {
			return nil, errors.NewCode(errors.InvalidInput, "factory must not return only error")
		}
	case 2:
		if !ft.Out(1).Implements(errorType) {
			return nil, errors.NewCode(errors.InvalidInput, "factory second return value must be error")
		}
	default:
		return nil, errors.NewCode(errors.InvalidInput, "factory must return (T) or (T, error)")
	}

	args, err := c.buildCallArgs(ft, state)
	if err != nil {
		return nil, err
	}

	results := fv.Call(args)
	values, callErr := stripTrailingError(results)
	if callErr != nil {
		return nil, errors.Wrap(callErr, errors.Internal, "factory function execution failed")
	}
	return values[0].Interface(), nil
}

// createMultiInstance 执行多返回值构造函数，并按声明顺序返回全部输出。
func (c *Container) createMultiInstance(constructor any, outTypes []reflect.Type, state *resolutionState) (results []reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			results = zeroValues(outTypes)
			err = errors.NewCode(errors.Internal, "constructor panicked").
				WithContext("panic", fmt.Sprint(r)).
				WithContext("stack", string(debug.Stack()))
		}
	}()

	if constructor == nil {
		return nil, errors.NewCode(errors.InvalidInput, "constructor cannot be nil")
	}
	if len(outTypes) == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "outTypes cannot be empty")
	}

	fv := reflect.ValueOf(constructor)
	ft := fv.Type()
	if ft.Kind() != reflect.Func {
		return nil, errors.NewCode(errors.InvalidInput, "constructor must be a function")
	}

	args, resolveErr := c.buildCallArgs(ft, state)
	if resolveErr != nil {
		return zeroValues(outTypes), resolveErr
	}

	results = fv.Call(args)
	if len(results) == 0 {
		return zeroValues(outTypes), errors.NewCode(errors.Internal, "constructor has no return value")
	}

	values, callErr := stripTrailingError(results)
	if callErr != nil {
		return zeroValues(outTypes), callErr
	}

	if len(values) != len(outTypes) {
		return zeroValues(outTypes), errors.NewCode(errors.Internal, "constructor output count mismatch").
			WithContext("expected", len(outTypes)).
			WithContext("got", len(values))
	}

	out := make([]reflect.Value, len(outTypes))
	for i := range outTypes {
		v := values[i]
		if !v.IsValid() {
			out[i] = reflect.Zero(outTypes[i])
			continue
		}
		if !v.Type().AssignableTo(outTypes[i]) {
			if v.Type().ConvertibleTo(outTypes[i]) {
				v = v.Convert(outTypes[i])
			} else {
				return zeroValues(outTypes), errors.NewCode(errors.Internal, "constructor output type mismatch").
					WithContext("index", i).
					WithContext("expected", outTypes[i].String()).
					WithContext("got", v.Type().String())
			}
		}
		out[i] = v
	}

	return out, nil
}

// stripTrailingError 拆出反射调用结果中的尾部 error。
func stripTrailingError(results []reflect.Value) ([]reflect.Value, error) {
	if len(results) == 0 {
		return results, nil
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	last := results[len(results)-1]
	if !last.IsValid() || !last.Type().Implements(errorType) {
		return results, nil
	}

	// 要求：error 返回值必须是可为 nil 的类型（例如 error 接口或 *MyError），否则无法表达 “no error”。
	switch last.Kind() {
	case reflect.Interface, reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		// ok
	default:
		return results[:len(results)-1], errors.NewCode(errors.Internal, "function returned non-nilable error type")
	}

	if last.IsNil() {
		return results[:len(results)-1], nil
	}
	if err, ok := last.Interface().(error); ok {
		return results[:len(results)-1], err
	}
	return results[:len(results)-1], errors.NewCode(errors.Internal, "function returned non-error as error")
}

// zeroValues 为给定输出类型列表构造零值切片。
func zeroValues(outTypes []reflect.Type) []reflect.Value {
	out := make([]reflect.Value, len(outTypes))
	for i, t := range outTypes {
		out[i] = reflect.Zero(t)
	}
	return out
}

func wrapResolveEntryError(err error, serviceLabel string) error {
	if err == nil {
		return nil
	}
	code := errors.Internal
	if errors.Is(err, errors.Dependency) {
		code = errors.Dependency
	}
	return errors.Wrap(err, code, "failed to create service").WithContext("service", serviceLabel)
}
