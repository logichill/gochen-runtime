package rest

import (
	"context"
	"strings"

	"gochen/contextx"
	"gochen/errors"
	"gochen/httpx"
)

// serviceContext 从 HTTP 上下文构建服务层上下文。
func (rb *RouteBuilder[T, ID]) serviceContext(c httpx.IContext) (context.Context, error) {
	if rb.config == nil {
		return c.RequestContext(), nil
	}
	return requestContext(c, rb.config.Audit.OperatorExtractor)
}

func requestContext(c httpx.IContext, extract func(httpx.IContext) (string, bool)) (context.Context, error) {
	ctx := c.RequestContext()
	if extract == nil {
		return ctx, nil
	}
	operator, ok := extract(c)
	operator = strings.TrimSpace(operator)
	if !ok || operator == "" {
		return ctx, nil
	}
	derived, err := contextx.WithOperator(ctx, operator)
	if err != nil {
		return nil, err
	}
	return derived, nil
}

// parseID 从路径参数解析 ID。
func (rb *RouteBuilder[T, ID]) parseID(c httpx.IContext) (ID, error) {
	var zero ID
	if rb.config == nil || rb.config.Routing.IDCodec == nil {
		return zero, errors.NewCode(errors.InvalidInput, "RouteConfig.Routing.IDCodec is required")
	}
	id, err := ParsePathWithCodec(c, "id", rb.config.Routing.IDCodec, "invalid ID format")
	if err != nil {
		return zero, errors.Wrap(err, errors.Validation, "invalid ID format")
	}
	return id, nil
}
