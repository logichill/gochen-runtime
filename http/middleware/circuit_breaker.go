package middleware

import (
	"gochen/httpx"
	"gochen/policy/circuit"
)

// CircuitBreaker 熔断器中间件。
func CircuitBreaker(cfg circuit.Config) httpx.Middleware {
	b := circuit.New(cfg)
	return func(ctx httpx.IContext, next func() error) error {
		return b.Call(next)
	}
}
