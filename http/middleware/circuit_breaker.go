package middleware

import (
	"context"

	"gochen/httpx"
	"gochen/policy/circuit"
)

// CircuitBreaker 熔断器中间件。
//
// 使用 CallContext 而非 Call：调用方（HTTP 请求）取消时 fn 返回的取消类
// 错误可按派生 ctx 的 cause 豁免失败计数（调用方主动放弃不能证明下游故障）；
// 下游自身超时/慢调用仍计失败并可触发跳闸。next 为 func() error 签名，
// 不接收派生 ctx。
//
// 派生 ctx（含调用方取消信号与 Config.CallTimeout 预算）通过 SetContext
// 注入 IRequestContext：next 内 handler 经 ctx.RequestContext() 看到的即是
// 带预算的派生 ctx——只要 handler 尊重请求 ctx（监听 Done()），CallTimeout
// 预算即对其生效，预算到期时 handler 的 ctx.Err() 为 Canceled、Cause 为
// 熔断器专属超时 cause。fn 返回前还原原 RequestContext，不污染外层中间件
// 的 ctx 视图；ctx.RequestContext() 为 nil 时降级为直接放行。
//
// 已知局限（ctx 语义固有）：若 handler 完全无视请求 ctx（不监听 Done()）
// 且返回 nil，熔断器无法判定其内部是否超时，只能按成功记账——预算到期
// 只能取消派生 ctx，无法强制中断无视 ctx 的 handler。
func CircuitBreaker(cfg circuit.Config) httpx.Middleware {
	b := circuit.New(cfg)
	return func(ctx httpx.IContext, next func() error) error {
		return b.CallContext(ctx.RequestContext(), func(callCtx context.Context) error {
			reqCtx := ctx.RequestContext()
			if reqCtx == nil {
				// 无请求 ctx 的降级路径：没有可注入的载体，直接放行。
				return next()
			}
			// 参照 httpx/tenant.go 的注入先例：把带预算的派生 ctx 挂回
			// IRequestContext，handler 经 ctx.RequestContext() 即可感知。
			ctx.SetContext(reqCtx.WithContext(callCtx))
			defer ctx.SetContext(reqCtx)
			return next()
		})
	}
}
