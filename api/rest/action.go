package rest

import (
	"strings"

	"gochen/domain"
	"gochen/errors"
	"gochen/httpx"
)

// Action 注册一条业务动作路由，并校验动作已在 Descriptor 中声明。
//
// handler 负责解码与命令编排（调用 app.Execute / app.ExecuteNew / app.Read）；
// rest 只做动作声明校验、操作人注入与统一错误映射。
// 直接注册到路由组的路由不受此声明检查。
func Action[T domain.IEntity[ID], ID comparable](
	group httpx.IRouteGroup,
	app Application[T, ID],
	method, path, action string,
	handler ActionHandler[T, ID],
	options ...Option[T, ID],
) error {
	if isNilService(app) {
		return errors.NewCode(errors.InvalidInput, "application cannot be nil")
	}
	if isNilService(group) {
		return errors.NewCode(errors.InvalidInput, "route group cannot be nil")
	}
	if handler == nil {
		return errors.NewCode(errors.InvalidInput, "action handler cannot be nil")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	action = strings.TrimSpace(action)
	if method == "" || strings.TrimSpace(path) == "" || action == "" {
		return errors.NewCode(errors.InvalidInput, "action route requires method, path and action")
	}
	descriptor := app.Descriptor()
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if _, ok := descriptor.Action(action); !ok {
		return errors.NewCode(errors.InvalidInput, "action is not declared by the application descriptor").
			WithContext("resource", descriptor.Resource).
			WithContext("action", action)
	}

	rb, err := NewRouteBuilder[T, ID](app, options...)
	if err != nil {
		return err
	}
	rb.descriptor = descriptor

	wrapped := func(c httpx.IContext) error {
		ctx, err := rb.serviceContext(c)
		if err != nil {
			return err
		}
		return handler(ctx, c, app)
	}
	permission, _ := descriptor.PermissionFor(action)
	return rb.mount(group, routeDef{
		spec: RouteSpec{
			Op:         OpAction,
			Method:     method,
			Path:       strings.TrimSpace(path),
			Action:     action,
			Permission: permission,
		},
		handler: wrapped,
	})
}
