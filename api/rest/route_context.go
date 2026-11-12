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

// extractOperator 从请求中提取操作人。
func (rb *RouteBuilder[T, ID]) extractOperator(c httpx.IContext) (string, bool) {
	if rb.config == nil || rb.config.Audit.OperatorExtractor == nil {
		return "", false
	}
	operator, ok := rb.config.Audit.OperatorExtractor(c)
	operator = strings.TrimSpace(operator)
	return operator, ok && operator != ""
}

// mustAuditedContext 构建审计上下文，operator 为必需。
func (rb *RouteBuilder[T, ID]) mustAuditedContext(c httpx.IContext) (context.Context, string, error) {
	operator, ok := rb.extractOperator(c)
	if !ok || operator == "" {
		return nil, "", errors.NewCode(errors.Validation, "missing operator")
	}
	ctx, err := contextx.WithOperator(c.RequestContext(), operator)
	if err != nil {
		return nil, "", err
	}
	return ctx, operator, nil
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
