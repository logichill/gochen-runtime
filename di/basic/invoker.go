package basic

import (
	"fmt"
	"reflect"
	"runtime/debug"

	"gochen-runtime/di"
	"gochen/errors"
)

// Invoke 调用函数并按参数类型从容器解析依赖。
//
// 说明：
// - 任一参数无法解析则立即返回 Dependency 错误，不会调用目标函数；
// - 函数最后一个返回值若为 error 且非 nil，会保留原错误码并补充调用上下文；
// - 不缓存调用结果；transient 服务每次都会重新解析。
func (c *Container) Invoke(invocation di.Invocation) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.NewCode(errors.Internal, "function panicked").
				WithContext("panic", fmt.Sprint(r)).
				WithContext("stack", string(debug.Stack()))
		}
	}()
	if di.InvocationValue(invocation) == nil {
		return errors.NewCode(errors.InvalidInput, "function cannot be nil")
	}
	fv := reflect.ValueOf(di.InvocationValue(invocation))
	if fv.Type().Kind() != reflect.Func {
		return errors.NewCode(errors.InvalidInput, "parameter must be a function")
	}
	state := &resolutionState{}
	args := make([]reflect.Value, fv.Type().NumIn())
	for i := 0; i < fv.Type().NumIn(); i++ {
		paramType := fv.Type().In(i)
		inst, err := c.resolveParameterWithState(paramType, state)
		if err != nil {
			return errors.Wrap(err, errors.Dependency, "failed to resolve parameter").
				WithContext("parameter_type", di.TypeKey(paramType)).
				WithContext("parameter_type_raw", paramType.String())
		}
		if inst == nil {
			args[i] = reflect.Zero(paramType)
		} else {
			args[i] = reflect.ValueOf(inst)
		}
	}
	results, callErr := stripTrailingError(fv.Call(args))
	_ = results
	if callErr != nil {
		return errors.Wrap(callErr, errors.Code(callErr), "function execution failed")
	}
	return nil
}

// buildCallArgs 为反射调用构造自动注入参数列表。
func (c *Container) buildCallArgs(ft reflect.Type, state *resolutionState) ([]reflect.Value, error) {
	if c == nil {
		return nil, errors.NewCode(errors.Internal, "container is nil")
	}

	args := make([]reflect.Value, ft.NumIn())
	for i := 0; i < ft.NumIn(); i++ {
		paramType := ft.In(i)
		inst, err := c.resolveParameterWithState(paramType, state)
		if err != nil {
			return nil, err
		}
		if inst == nil {
			args[i] = reflect.Zero(paramType)
		} else {
			args[i] = reflect.ValueOf(inst)
		}
	}
	return args, nil
}
