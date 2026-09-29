package basic

import (
	"reflect"

	"gochen-runtime/di"
	"gochen/errors"
)

// RegisterSingleton 按类型注册单例工厂。
func (c *Container) RegisterSingleton(serviceType reflect.Type, factory di.Factory) error {
	if serviceType == nil {
		return errors.NewCode(errors.InvalidInput, "service type cannot be nil")
	}
	if err := validateTypedFactoryCompatibility(serviceType, factory); err != nil {
		return err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err := c.ensureServiceTypeAvailableLocked(serviceType); err != nil {
		return err
	}
	c.typedServices[serviceType] = newServiceEntry(serviceType, di.FactoryValue(factory), lifetimeSingleton)
	return nil
}

// RegisterTransient 按类型注册瞬态工厂。
func (c *Container) RegisterTransient(serviceType reflect.Type, factory di.Factory) error {
	if serviceType == nil {
		return errors.NewCode(errors.InvalidInput, "service type cannot be nil")
	}
	if err := validateTypedFactoryCompatibility(serviceType, factory); err != nil {
		return err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err := c.ensureServiceTypeAvailableLocked(serviceType); err != nil {
		return err
	}
	c.typedServices[serviceType] = newServiceEntry(serviceType, di.FactoryValue(factory), lifetimeTransient)
	return nil
}

func validateTypedFactoryCompatibility(serviceType reflect.Type, factory di.Factory) error {
	if factory.IsZero() {
		return errors.NewCode(errors.InvalidInput, "factory cannot be nil").
			WithContext("service_type", di.TypeKey(serviceType))
	}

	outputType := serviceOutputType(di.FactoryValue(factory))
	if outputType == nil {
		return errors.NewCode(errors.InvalidInput, "factory output type cannot be resolved").
			WithContext("service_type", di.TypeKey(serviceType))
	}
	if !isCompatibleServiceType(serviceType, outputType) {
		return errors.NewCode(errors.InvalidInput, "factory output is not assignable to service type").
			WithContext("service_type", di.TypeKey(serviceType)).
			WithContext("factory_output_type", di.TypeKey(outputType))
	}

	return nil
}

// RegisterInstance 按类型注册实例。
func (c *Container) RegisterInstance(serviceType reflect.Type, instance di.Instance) error {
	if serviceType == nil {
		return errors.NewCode(errors.InvalidInput, "service type cannot be nil")
	}
	if instance.IsZero() {
		return errors.NewCode(errors.InvalidInput, "instance cannot be nil").
			WithContext("service_type", di.TypeKey(serviceType))
	}
	instanceValue := di.InstanceValue(instance)
	instanceType := reflect.TypeOf(instanceValue)
	if instanceType == nil {
		return errors.NewCode(errors.InvalidInput, "instance cannot be nil").
			WithContext("service_type", di.TypeKey(serviceType))
	}
	instanceReflectValue := reflect.ValueOf(instanceValue)
	switch instanceReflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if instanceReflectValue.IsNil() {
			return errors.NewCode(errors.InvalidInput, "instance cannot be nil").
				WithContext("service_type", di.TypeKey(serviceType))
		}
	}
	if !instanceType.AssignableTo(serviceType) {
		return errors.NewCode(errors.InvalidInput, "instance type is not assignable to service type").
			WithContext("service_type", di.TypeKey(serviceType)).
			WithContext("instance_type", di.TypeKey(instanceType)).
			WithContext("instance_type_raw", instanceType.String())
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err := c.ensureServiceTypeAvailableLocked(serviceType); err != nil {
		return err
	}
	entry := newServiceEntry(serviceType, instanceValue, lifetimeSingleton)
	entry.created = true
	entry.instance = instanceValue
	c.typedServices[serviceType] = entry
	return nil
}

func (c *Container) ensureServiceTypeAvailableLocked(serviceType reflect.Type) error {
	if _, exists := c.typedServices[serviceType]; exists {
		return errors.NewCode(errors.Conflict, "service already registered").WithContext("service_type", di.TypeKey(serviceType))
	}
	return nil
}

func (c *Container) findServiceEntryByKeyLocked(key string) (*serviceEntry, bool) {
	for serviceType, entry := range c.typedServices {
		if di.TypeKey(serviceType) == key {
			return entry, true
		}
	}
	return nil, false
}
