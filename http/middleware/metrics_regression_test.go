package middleware

import (
	"fmt"
	"math"
	nethttptesting "net/http/httptest"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/httpx"
	"gochen/observe"
	"gochen/testkit/assert"
)

// patternedStub 在 stubContext 基础上暴露路由模板（模拟 nethttp/gin 适配层）。
type patternedStub struct {
	stubContext
	pattern string
}

func (c *patternedStub) RoutePattern() string { return c.pattern }

// TestRegressionF05_RouteTemplateBoundsCardinality 验证：指标 path 标签使用
// 路由模板，同一路由的任意数量动态路径值只产生一个标签值。
func TestRegressionF05_RouteTemplateBoundsCardinality(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)

	uniquePaths := map[string]struct{}{}
	for i := 0; i < 500; i++ {
		ctx := &patternedStub{
			stubContext: stubContext{path: fmt.Sprintf("/items/customer-%d", i)},
			pattern:     "/items/:slug",
		}
		assert.NoError(t, mw(ctx, func() error { return nil }))
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.NotEmpty(t, metrics.counters)
	for _, call := range metrics.counters {
		uniquePaths[call.labels["path"]] = struct{}{}
	}
	assert.Equal(t, 1, len(uniquePaths))
	assert.Contains(t, uniquePaths, "/items/:slug")
}

// TestRegressionF05_UnmatchedRoutesUseFixedLabel 验证：上下文支持路由信息但
// 未命中路由时，统一归入固定类别，不产生用户可控的无界序列。
func TestRegressionF05_UnmatchedRoutesUseFixedLabel(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)

	uniquePaths := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		ctx := &patternedStub{
			stubContext: stubContext{path: fmt.Sprintf("/probe-%d/exploit", i)},
			pattern:     "",
		}
		assert.NoError(t, mw(ctx, func() error { return nil }))
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, call := range metrics.counters {
		uniquePaths[call.labels["path"]] = struct{}{}
	}
	assert.Equal(t, 1, len(uniquePaths))
	assert.Contains(t, uniquePaths, UnmatchedRouteLabel)
}

// TestRegressionF05_NethttpServeMuxPatternFlowsThrough 验证 nethttp 适配层把
// Go 1.22+ ServeMux 命中的模板传给指标中间件（端到端）。
func TestRegressionF05_NethttpServeMuxPatternFlowsThrough(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)

	server := nethttp.NewServer(nil)
	server.Use(mw)
	server.GET("/items/:slug", func(c httpx.IContext) error {
		return c.String(200, "ok")
	})
	handler := server.Handler()

	uniquePaths := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		rec := nethttptesting.NewRecorder()
		handler.ServeHTTP(rec, nethttptesting.NewRequest("GET", fmt.Sprintf("/items/slug-%d", i), nil))
		assert.Equal(t, 200, rec.Code)
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, call := range metrics.counters {
		uniquePaths[call.labels["path"]] = struct{}{}
	}
	assert.Equal(t, 1, len(uniquePaths))
	assert.Contains(t, uniquePaths, "/items/{slug}")
}

// TestRegressionF06_BucketsRequireRegistrar 验证：收集器不支持直方图注册时，
// 装配阶段明确失败（无论是否配置分桶——注册现在是装配的必需步骤）。
func TestRegressionF06_BucketsRequireRegistrar(t *testing.T) {
	_, err := MetricsMiddleware(MetricsConfig{Metrics: &minimalMetrics{}})
	assert.Error(t, err)

	_, err = MetricsMiddleware(MetricsConfig{
		Metrics: &minimalMetrics{},
		Buckets: []float64{50, 100, 500},
	})
	assert.Error(t, err)
}

// TestRegressionF06_InvalidBucketsFailAtAssembly 验证：乱序/非正/非有限的
// 分桶在装配期返回错误，而不是推迟到首个请求以 panic 暴露。
func TestRegressionF06_InvalidBucketsFailAtAssembly(t *testing.T) {
	invalid := [][]float64{
		{100, 50},           // 乱序
		{10, 10},            // 非严格递增
		{-1, 1, 5},          // 非正数
		{1, 2, math.NaN()},  // 非有限
		{1, 2, math.Inf(1)}, // 非有限
	}
	for _, buckets := range invalid {
		_, err := MetricsMiddleware(MetricsConfig{
			Metrics: &recordingMetrics{},
			Buckets: buckets,
		})
		assert.Error(t, err)
	}
}

// TestRegressionF06_DefaultBucketsAreMillisecondScale 验证：未配置分桶时
// 装配期按毫秒尺度默认分桶注册，典型 Web 请求（数十毫秒）落在桶范围内，
// 不再出现"观察值毫秒 vs 默认桶 0.005…10 秒尺度"的错配。
func TestRegressionF06_DefaultBucketsAreMillisecondScale(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	metrics.mu.Lock()
	if assert.Len(t, metrics.registered, 1) {
		registered := metrics.registered[0]
		assert.Equal(t, "http_request_duration_ms", registered.name)
		assert.Equal(t, DefaultDurationBucketsMS, registered.buckets)
		assert.True(t, registered.buckets[len(registered.buckets)-1] >= 10000,
			"default buckets must cover typical slow requests in milliseconds")
	}
	metrics.mu.Unlock()
}

// hiddenRouteContext 包装 IContext 但不暴露 RoutePattern（模拟只转发接口
// 方法、丢失模板能力的自定义包装层）。
type hiddenRouteContext struct{ httpx.IContext }

// TestRegressionF05_UnpatternedContextUsesFixedLabel 验证：上下文不暴露
// 路由模板时归入固定类别，slug 类动态路径不再形成无界序列（旧实现按
// 数字/UUID 猜测归一化，对字符串 ID 无效）。
func TestRegressionF05_UnpatternedContextUsesFixedLabel(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)

	uniquePaths := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		ctx := hiddenRouteContext{&stubContext{path: fmt.Sprintf("/items/slug-%d", i)}}
		assert.NoError(t, mw(ctx, func() error { return nil }))
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, call := range metrics.counters {
		uniquePaths[call.labels["path"]] = struct{}{}
	}
	assert.Equal(t, 1, len(uniquePaths))
	assert.Contains(t, uniquePaths, UnpatternedRouteLabel)
}

// minimalMetrics 只实现 observe.IMetrics，不支持直方图注册。
type minimalMetrics struct{}

func (m *minimalMetrics) Counter(string, int64, map[string]string)       {}
func (m *minimalMetrics) Gauge(string, float64, map[string]string)       {}
func (m *minimalMetrics) Histogram(string, float64, map[string]string)   {}
func (m *minimalMetrics) Timer(string, map[string]string) observe.ITimer { return nil }
