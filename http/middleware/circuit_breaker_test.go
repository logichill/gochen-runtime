package middleware

import (
	"context"
	"testing"
	"time"

	"gochen/clock"
	"gochen/errors"
	"gochen/httpx"
	"gochen/policy/circuit"
)

func TestCircuitBreaker_UsesPatternConfig(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(0, 0).UTC())
	mw := CircuitBreaker(circuit.Config{
		MaxFailures:  1,
		ResetTimeout: 5 * time.Second,
		Clock:        clk,
	})
	ctx := &stubContext{}

	upstreamErr := errors.New("boom")
	if err := mw(ctx, func() error { return upstreamErr }); !errors.Is(err, upstreamErr) {
		t.Fatalf("expected upstream error passthrough, got %v", err)
	}

	if err := mw(ctx, func() error { return nil }); err == nil || !errors.Is(err, errors.ServiceUnavailable) {
		t.Fatalf("expected open circuit rejection, got %v", err)
	}

	clk.Advance(6 * time.Second)
	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatalf("expected half-open probe success and close transition, got %v", err)
	}
}

// newStubContextWithRequest 构造带可取消请求 ctx 的 stub 上下文（供
// CallContext 派生使用，模拟真实 HTTP 接线中 ctx.RequestContext() 的来源）。
func newStubContextWithRequest(t *testing.T) (*stubContext, context.Context, context.CancelFunc) {
	t.Helper()
	reqCtx, cancel := context.WithCancel(context.Background())
	requestContext, err := httpx.NewRequestContext(reqCtx)
	if err != nil {
		t.Fatalf("new request context: %v", err)
	}
	return &stubContext{reqCtx: requestContext}, reqCtx, cancel
}

// TestCircuitBreaker_CallerCancelNotCountedAsFailure 回归：迁移到 CallContext
// 后，调用方（HTTP 请求）主动取消不计失败、不跳闸。
func TestCircuitBreaker_CallerCancelNotCountedAsFailure(t *testing.T) {
	mw := CircuitBreaker(circuit.Config{
		MaxFailures:  1,
		ResetTimeout: 5 * time.Second,
	})
	ctx, reqCtx, cancel := newStubContextWithRequest(t)
	defer cancel()

	cancel() // 调用方主动取消请求。
	handlerErr := mw(ctx, func() error { return reqCtx.Err() })
	if !errors.Is(handlerErr, context.Canceled) {
		t.Fatalf("expected caller cancel passthrough, got %v", handlerErr)
	}

	// 取消被豁免：熔断器仍闭合，后续请求正常放行。
	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatalf("caller cancel must not trip the breaker, got %v", err)
	}
}

// TestCircuitBreaker_DownstreamTimeoutTrips 回归：调用方 ctx 存活时下游返回
// 超时/取消形态错误仍计失败并跳闸。
func TestCircuitBreaker_DownstreamTimeoutTrips(t *testing.T) {
	mw := CircuitBreaker(circuit.Config{
		MaxFailures:  1,
		ResetTimeout: 5 * time.Second,
	})
	ctx, _, cancel := newStubContextWithRequest(t)
	defer cancel()

	// 下游自身超时（调用方 ctx 仍存活）：计失败。
	if err := mw(ctx, func() error { return context.DeadlineExceeded }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected downstream timeout passthrough, got %v", err)
	}

	if err := mw(ctx, func() error { return nil }); err == nil || !errors.Is(err, errors.ServiceUnavailable) {
		t.Fatalf("downstream timeout must trip the breaker, got %v", err)
	}
}

// TestCircuitBreaker_CallTimeoutBudgetTrips 验证 Config.CallTimeout 经中间件
// 真实生效：中间件把带预算的派生 ctx 注入 RequestContext，next 阻塞等待其
// Done()（尊重 ctx 的真实 handler 形态），预算到期取消传播到 handler，
// 计失败并跳闸。
//
// 注意不自行 sleep 后返回哨兵错误——那种写法有无 CallTimeout 都计失败，
// 构不成预算生效的守护（上一版测试即此假守护）。
func TestCircuitBreaker_CallTimeoutBudgetTrips(t *testing.T) {
	mw := CircuitBreaker(circuit.Config{
		MaxFailures:  1,
		ResetTimeout: 5 * time.Second,
		CallTimeout:  20 * time.Millisecond,
	})
	ctx, _, cancel := newStubContextWithRequest(t)
	defer cancel()

	// 慢下游：阻塞在注入后的请求 ctx 上，预算到期由派生 ctx 取消唤醒。
	slow := mw(ctx, func() error {
		<-ctx.RequestContext().Done()
		return ctx.RequestContext().Err()
	})
	// 预算到期以专属 cause 取消派生 ctx，其 Err() 为 context.Canceled。
	if !errors.Is(slow, context.Canceled) {
		t.Fatalf("expected call timeout budget to cancel injected request ctx, got %v", slow)
	}

	// 预算内未完成计失败：熔断器跳闸。
	if err := mw(ctx, func() error { return nil }); err == nil || !errors.Is(err, errors.ServiceUnavailable) {
		t.Fatalf("call timeout must trip the breaker, got %v", err)
	}
}

// TestCircuitBreaker_CallTimeout_HandlerIgnoresContext 如实断言 ctx 语义的
// 固有限制：handler 完全无视注入的请求 ctx（不监听 Done()）且返回 nil 时，
// 熔断器无法判定其内部是否超时，只能按成功记账、无法跳闸——预算到期只能
// 取消派生 ctx，无法强制中断无视 ctx 的 handler。
func TestCircuitBreaker_CallTimeout_HandlerIgnoresContext(t *testing.T) {
	mw := CircuitBreaker(circuit.Config{
		MaxFailures:  1,
		ResetTimeout: 5 * time.Second,
		CallTimeout:  20 * time.Millisecond,
	})
	ctx, _, cancel := newStubContextWithRequest(t)
	defer cancel()

	// 无视 ctx 的慢 handler：返回 nil → 计成功，错误透传为 nil。
	if err := mw(ctx, func() error {
		time.Sleep(50 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatalf("ctx-ignorant handler returning nil must be recorded as success, got %v", err)
	}

	// 该形态不产生失败计数：熔断器仍闭合。
	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatalf("ctx-ignorant slow success must not trip the breaker, got %v", err)
	}
}
