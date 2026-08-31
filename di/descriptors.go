package di

// Constructor 描述一个可注册到容器的构造器函数。
type Constructor struct {
	fn any
}

// Factory 描述一个服务工厂函数。
type Factory struct {
	fn any
}

// Instance 描述一个待注册的实例值。
type Instance struct {
	value any
}

// Invocation 描述一个待执行的注入调用。
type Invocation struct {
	fn any
}

// NewConstructor 创建构造器描述。
func NewConstructor(fn any) Constructor { return Constructor{fn: fn} }

// NewFactory 创建工厂描述。
func NewFactory(fn any) Factory { return Factory{fn: fn} }

// NewInstance 创建实例描述。
func NewInstance(value any) Instance { return Instance{value: value} }

// NewInvocation 创建调用描述。
func NewInvocation(fn any) Invocation { return Invocation{fn: fn} }

func (c Constructor) IsZero() bool { return c.fn == nil }

func (f Factory) IsZero() bool { return f.fn == nil }

func (i Instance) IsZero() bool { return i.value == nil }

func (i Invocation) IsZero() bool { return i.fn == nil }

func ConstructorValue(constructor Constructor) any { return constructor.fn }

func FactoryValue(factory Factory) any { return factory.fn }

func InstanceValue(instance Instance) any { return instance.value }

func InvocationValue(invocation Invocation) any { return invocation.fn }

// ApplyConstructor 将构造器描述交给具体容器实现处理。
func ApplyConstructor(constructor Constructor, register func(any) error) error {
	if register == nil {
		return nil
	}
	return register(constructor.fn)
}

// ApplyFactory 将工厂描述交给具体容器实现处理。
func ApplyFactory(factory Factory, register func(any) error) error {
	if register == nil {
		return nil
	}
	return register(factory.fn)
}

// ApplyInstance 将实例描述交给具体容器实现处理。
func ApplyInstance(instance Instance, register func(any) error) error {
	if register == nil {
		return nil
	}
	return register(instance.value)
}

// ApplyInvocation 将调用描述交给具体容器实现处理。
func ApplyInvocation(invocation Invocation, invoke func(any) error) error {
	if invoke == nil {
		return nil
	}
	return invoke(invocation.fn)
}
