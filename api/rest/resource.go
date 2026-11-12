package rest

import (
	"gochen-runtime/host/module"
	"gochen/app"
	"gochen/domain"
	"gochen/errors"
	"gochen/httpx"
)

// ResourceBinding 将同一资源应用连接到模块清单与标准 REST 注册流程。
type ResourceBinding[T domain.IEntity[ID], ID comparable] struct {
	application ReadApplication[T, ID]
	options     []Option[T, ID]
}

// Resource 声明资源入口；Build 只校验，实际路由由 Host 注册。
func Resource[T domain.IEntity[ID], ID comparable](application ReadApplication[T, ID], options ...Option[T, ID]) *ResourceBinding[T, ID] {
	return &ResourceBinding[T, ID]{application: application, options: append([]Option[T, ID](nil), options...)}
}

// ModuleBinding 返回同一应用实例及其路由注册器。
func (r *ResourceBinding[T, ID]) ModuleBinding() (module.Binding, error) {
	if r == nil || isNilService(r.application) {
		return module.Binding{}, errors.NewCode(errors.InvalidInput, "resource binding requires an application")
	}
	if _, _, err := Describe(r.application, r.options...); err != nil {
		return module.Binding{}, err
	}
	return module.Binding{Applications: []app.IDescriptorProvider{r.application}, RouteRegistrars: []module.IRouteRegistrar{r}}, nil
}

// RegisterRoutes 复用标准 Mount 的能力校验、路由及入口登记。
func (r *ResourceBinding[T, ID]) RegisterRoutes(group httpx.IRouteGroup) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "resource binding cannot be nil")
	}
	return Mount(group, r.application, r.options...)
}
