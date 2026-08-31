package rest

import (
	"gochen/errors"
	"gochen/httpx"
)

// ParseQuery 绑定命令型/专用 query DTO。
func ParseQuery[T any](c httpx.IContext) (T, error) {
	if c == nil {
		var zero T
		return zero, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	var query T
	if err := c.BindQuery(&query); err != nil {
		var zero T
		return zero, err
	}
	return query, nil
}

// ParseQueryWithDefaults 在绑定前注入默认值；未传入的字段保留 defaults 中的值。
func ParseQueryWithDefaults[T any](c httpx.IContext, defaults T) (T, error) {
	if c == nil {
		var zero T
		return zero, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	query := defaults
	if err := c.BindQuery(&query); err != nil {
		var zero T
		return zero, err
	}
	return query, nil
}
