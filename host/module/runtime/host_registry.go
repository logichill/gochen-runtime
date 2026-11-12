package runtime

import (
	"reflect"

	"gochen-runtime/di"
	"gochen-runtime/host/capability"
	"gochen-runtime/host/config"
	"gochen-runtime/host/internal/appregistry"
	"gochen-runtime/host/module"
	"gochen/errors"
)

func (s *Host) ensureFrameworkRegistries() error {
	if s == nil {
		return nil
	}
	s.config = config.Normalize(s.config)
	if s.container == nil {
		return errors.NewCode(errors.Internal, "container is nil")
	}

	if s.applicationRegistry == nil {
		s.applicationRegistry = appregistry.NewRegistry()
	}
	if err := registerFrameworkRegistry(s.container, reflect.TypeOf((*module.IApplicationRegistry)(nil)).Elem(), s.applicationRegistry); err != nil {
		return err
	}

	if s.catalogRegistrars == nil {
		s.catalogRegistrars = s.config.CatalogRegistrars
	}
	if s.moduleRegistry == nil {
		s.moduleRegistry = s.config.ModuleRegistry
	}
	if s.metadataRegistry == nil {
		s.metadataRegistry = s.config.MetadataRegistry
	}

	if err := registerFrameworkRegistry(s.container, reflect.TypeOf((*capability.IModuleRegistry)(nil)).Elem(), s.moduleRegistry); err != nil {
		return err
	}
	if err := registerFrameworkRegistry(s.container, reflect.TypeOf((*capability.IMetadataRegistry)(nil)).Elem(), s.metadataRegistry); err != nil {
		return err
	}

	return nil
}

func registerFrameworkRegistry(container di.IContainer, serviceType reflect.Type, instance any) error {
	if container == nil || instance == nil {
		return nil
	}
	if serviceType == nil || container.IsRegistered(serviceType) {
		return nil
	}
	if err := container.RegisterInstance(serviceType, di.NewInstance(instance)); err != nil {
		return err
	}

	concreteType := reflect.TypeOf(instance)
	if concreteType == nil || concreteType == serviceType || container.IsRegistered(concreteType) {
		return nil
	}
	return container.RegisterInstance(concreteType, di.NewInstance(instance))
}
