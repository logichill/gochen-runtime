package di

import "reflect"

type registryView struct {
	registry IRegistry
}

// RegistryOnly 返回只暴露注册能力的 DI view。
func RegistryOnly(registry IRegistry) IRegistry {
	if registry == nil {
		return nil
	}
	return registryView{registry: registry}
}

func (v registryView) RegisterSingleton(serviceType reflect.Type, factory Factory) error {
	return v.registry.RegisterSingleton(serviceType, factory)
}

func (v registryView) RegisterTransient(serviceType reflect.Type, factory Factory) error {
	return v.registry.RegisterTransient(serviceType, factory)
}

func (v registryView) RegisterInstance(serviceType reflect.Type, instance Instance) error {
	return v.registry.RegisterInstance(serviceType, instance)
}

type resolverView struct {
	resolver IResolver
}

// ResolverOnly 返回只暴露解析能力的 DI view。
func ResolverOnly(resolver IResolver) IResolver {
	if resolver == nil {
		return nil
	}
	return resolverView{resolver: resolver}
}

func (v resolverView) Resolve(serviceType reflect.Type) (any, error) {
	return v.resolver.Resolve(serviceType)
}

func (v resolverView) IsRegistered(serviceType reflect.Type) bool {
	return v.resolver.IsRegistered(serviceType)
}
