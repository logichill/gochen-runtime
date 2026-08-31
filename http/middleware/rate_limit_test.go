package middleware

import (
	"testing"
	"time"

	"gochen/clock"
	"gochen/errors"
	"gochen/httpx"
	"gochen/policy/ratelimit"
)

func TestRateLimit_SkipPathsBypassLimiter(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(0, 0).UTC())
	mw := RateLimit(RateLimitConfig{
		Config: ratelimit.Config{
			RequestsPerSecond: 1,
			BurstSize:         1,
			WindowSize:        time.Minute,
			Clock:             clk,
		},
		SkipPaths: []string{"/health"},
	})

	ctx := &stubContext{path: "/health", clientIP: "127.0.0.1"}
	calls := 0
	next := func() error {
		calls++
		return nil
	}

	for i := 0; i < 3; i++ {
		if err := mw(ctx, next); err != nil {
			t.Fatalf("expected skip path to bypass limiter, got err=%v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected next called 3 times, got %d", calls)
	}
}

func TestRateLimit_UsesConfigAndAnonymousKeyFallback(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(0, 0).UTC())
	mw := RateLimit(RateLimitConfig{
		Config: ratelimit.Config{
			RequestsPerSecond: 1,
			BurstSize:         1,
			WindowSize:        time.Minute,
			Clock:             clk,
		},
		KeyFn: func(httpx.IContext) string {
			return "   "
		},
	})

	ctx := &stubContext{path: "/api", clientIP: "10.0.0.8"}

	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatalf("first request should pass, err=%v", err)
	}
	if err := mw(ctx, func() error { return nil }); err == nil || !errors.Is(err, errors.TooManyRequests) {
		t.Fatalf("second request should be throttled, err=%v", err)
	}

	clk.Advance(time.Second)
	if err := mw(ctx, func() error { return nil }); err != nil {
		t.Fatalf("request after refill should pass, err=%v", err)
	}
}
