package basic

import (
	"reflect"
	"sync"

	"gochen-runtime/di"
	"gochen/errors"
)

type multiConstructorGroup struct {
	container   *Container
	constructor any
	outTypes    []reflect.Type
	outLabels   []string

	mu        sync.Mutex
	cond      *sync.Cond
	created   bool
	creating  bool
	creator   *resolutionState
	results   []reflect.Value
	createErr error
}

type groupOutputFactory struct {
	group *multiConstructorGroup
	index int
}

// newMultiConstructorGroup 创建一个共享多返回值构造结果的分组。
func newMultiConstructorGroup(container *Container, constructor any, outTypes []reflect.Type) *multiConstructorGroup {
	outLabels := make([]string, 0, len(outTypes))
	for _, outType := range outTypes {
		outLabels = append(outLabels, di.TypeKey(outType))
	}
	g := &multiConstructorGroup{
		container:   container,
		constructor: constructor,
		outTypes:    outTypes,
		outLabels:   outLabels,
	}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// get 返回指定下标的构造结果，并确保多返回值构造函数只执行一次。
func (g *multiConstructorGroup) get(index int, state *resolutionState) (reflect.Value, error) {
	if g == nil {
		return reflect.Value{}, errors.NewCode(errors.Internal, "multi constructor group is nil")
	}
	if index < 0 || index >= len(g.outTypes) {
		return reflect.Value{}, errors.NewCode(errors.InvalidInput, "multi constructor output index out of range").WithContext("index", index)
	}

	g.mu.Lock()
	if g.created {
		v := g.results[index]
		err := g.createErr
		g.mu.Unlock()
		return v, err
	}
	if g.creating {
		if state != nil && state.intersects(g.outLabels) {
			g.mu.Unlock()
			return reflect.Value{}, newCircularDependencyError(state.cyclePathFromAny(g.outLabels))
		}
		endWait, err := g.container.beginResolutionWait(state, g.creator, g.outLabels[index])
		if err != nil {
			g.mu.Unlock()
			return reflect.Value{}, err
		}
		for !g.created {
			g.cond.Wait()
		}
		endWait()
		v := g.results[index]
		err = g.createErr
		g.mu.Unlock()
		return v, err
	}
	g.creating = true
	g.creator = state
	g.mu.Unlock()

	results, err := g.container.createMultiInstance(g.constructor, g.outTypes, state)

	g.mu.Lock()
	g.results = results
	g.createErr = err
	g.created = true
	g.creating = false
	g.creator = nil
	g.cond.Broadcast()
	v := g.results[index]
	g.mu.Unlock()
	return v, err
}

// splitConstructorOutputs 提取构造函数中所有非 error 返回值类型。
func splitConstructorOutputs(t reflect.Type) ([]reflect.Type, error) {
	if t == nil {
		return nil, errors.NewCode(errors.InvalidInput, "constructor type cannot be nil")
	}
	if t.Kind() != reflect.Func {
		return nil, errors.NewCode(errors.InvalidInput, "constructor must be a function")
	}
	if t.NumOut() == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "constructor must have a return value")
	}

	errorType := reflect.TypeOf((*error)(nil)).Elem()
	var outs []reflect.Type
	for i := 0; i < t.NumOut(); i++ {
		out := t.Out(i)
		if out.Implements(errorType) {
			if i != t.NumOut()-1 {
				return nil, errors.NewCode(errors.InvalidInput, "error must be the last return value")
			}
			continue
		}
		outs = append(outs, out)
	}
	if len(outs) == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "constructor must return at least one non-error value")
	}
	return outs, nil
}

// makeGroupOutputFactory 为多返回值构造函数的单个输出生成适配工厂。
func makeGroupOutputFactory(group *multiConstructorGroup, index int, outType reflect.Type) any {
	_ = outType
	return groupOutputFactory{group: group, index: index}
}

// RegisterConstructor 注册一个构造函数，并自动把其返回值暴露为可解析服务。
func (c *Container) RegisterConstructor(constructor di.Constructor) error {
	if di.ConstructorValue(constructor) == nil {
		return errors.NewCode(errors.InvalidInput, "constructor cannot be nil")
	}
	t := reflect.TypeOf(di.ConstructorValue(constructor))
	outs, err := splitConstructorOutputs(t)
	if err != nil {
		return err
	}
	if len(outs) == 1 {
		return c.RegisterSingleton(outs[0], di.NewFactory(di.ConstructorValue(constructor)))
	}

	// multi-output: register each output by its service type, but ensure constructor is invoked once and results are shared.
	group := newMultiConstructorGroup(c, di.ConstructorValue(constructor), outs)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	seen := make(map[reflect.Type]struct{}, len(outs))
	for _, out := range outs {
		if _, ok := seen[out]; ok {
			return errors.NewCode(errors.InvalidInput, "constructor has duplicate output type").WithContext("service_type", di.TypeKey(out))
		}
		seen[out] = struct{}{}
		if err := c.ensureServiceTypeAvailableLocked(out); err != nil {
			return err
		}
	}

	for i, out := range outs {
		c.typedServices[out] = newServiceEntry(out, makeGroupOutputFactory(group, i, out), lifetimeSingleton)
	}
	return nil
}
