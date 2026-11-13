// Package action 提供 HTTP 路由级 Action 快速拦截中间件。
//
// 定位：**纯 Fast-Deny 性能优化**，不是安全边界。
//
//	真正的授权发生在 Application 层（gochen/app/crud），
//	因此绕过 HTTP 直接调用 Application 同样会被拦截。
//	本中间件的价值只是把"明显无权"的请求在进入业务编排前挡掉，
//	省掉后续的资源解析与 PDP 开销。
//
// 铁律：**不得**把这里当成唯一的授权点。删掉本中间件后，
// 系统的安全语义必须完全不变——只是多做了一些无用功。
// 这也是取代原 gochen/auth/http 的原因：后者曾是唯一授权点。
package action

import auth "gochen/auth"

import (
	authaction "gochen/auth/action"

	"gochen/errors"
	"gochen/httpx"
)

// FastDeny 构造路由级 Action 快速拦截中间件。
//
// checker 为 nil 或 action 非法时立即返回构造错误，避免把配置问题推迟到首个请求。
func FastDeny(checker auth.IActionChecker, action string) (httpx.Middleware, error) {
	if checker == nil {
		return nil, errors.NewCode(errors.InvalidInput, "action checker is nil")
	}
	if !authaction.IsValidCode(action) {
		return nil, errors.NewCode(errors.InvalidInput, "invalid action code").
			WithContext("action", action)
	}
	return func(ctx httpx.IContext, next func() error) error {
		if err := checker.RequireAction(ctx.RequestContext(), action); err != nil {
			return err
		}
		return next()
	}, nil
}
