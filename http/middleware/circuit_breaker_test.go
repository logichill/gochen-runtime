package middleware

import (
	"testing"
	"time"

	"gochen/clock"
	"gochen/errors"
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
