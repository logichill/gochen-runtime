package middleware

import (
	"strconv"
	"sync"
	"testing"

	"gochen/testkit/assert"

	"gochen/observe"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/users", "/users"},
		{"/users/123", "/users/:id"},
		{"/users/123/posts", "/users/:id/posts"},
		{"/users/123/posts/456", "/users/:id/posts/:id"},
		{"/items/550e8400-e29b-41d4-a716-446655440000", "/items/:uuid"},
		{"/api/v1/users", "/api/v1/users"},
		{"/", "/"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizePath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizePathCacheEvictsOldestAtMaxEntries(t *testing.T) {
	clearNormalizedPathCache()
	t.Cleanup(clearNormalizedPathCache)

	for i := 0; i < normalizedPathCacheMaxEntries; i++ {
		_ = normalizePath("/items/" + strconv.Itoa(i))
	}
	_ = normalizePath("/users/1")

	assert.Equal(t, normalizedPathCacheMaxEntries, normalizedPathCacheLen())
	assert.Equal(t, "/users/:id", normalizePath("/users/1"))
}

func TestIsNumeric(t *testing.T) {
	assert.True(t, isNumeric("123"))
	assert.True(t, isNumeric("0"))
	assert.True(t, isNumeric("999999"))
	assert.False(t, isNumeric(""))
	assert.False(t, isNumeric("abc"))
	assert.False(t, isNumeric("12a"))
	assert.False(t, isNumeric("a12"))
}

func TestIsUUID(t *testing.T) {
	assert.True(t, isUUID("550e8400-e29b-41d4-a716-446655440000"))
	assert.True(t, isUUID("550E8400-E29B-41D4-A716-446655440000"))
	assert.False(t, isUUID(""))
	assert.False(t, isUUID("123"))
	assert.False(t, isUUID("not-a-uuid-string-here"))
	assert.False(t, isUUID("550e8400e29b41d4a716446655440000")) // no dashes
}

func TestMetricsMiddleware_NilContext(t *testing.T) {
	mw := MetricsMiddleware(MetricsConfig{})

	nextCalled := false
	err := mw(nil, func() error {
		nextCalled = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, nextCalled)
}

func TestMetricsConfig_DefaultValues(t *testing.T) {
	cfg := MetricsConfig{}

	// 验证默认配置不会 panic
	mw := MetricsMiddleware(cfg)
	assert.NotNil(t, mw)
}

func TestMetricsConfig_WithNamespace(t *testing.T) {
	cfg := MetricsConfig{
		Namespace: "myapp",
	}

	mw := MetricsMiddleware(cfg)
	assert.NotNil(t, mw)
}

func TestMetricsConfig_WithSkipPaths(t *testing.T) {
	cfg := MetricsConfig{
		SkipPaths:        []string{"/health", "/ready"},
		SkipPathPrefixes: []string{"/internal/"},
	}

	mw := MetricsMiddleware(cfg)
	assert.NotNil(t, mw)
}

type metricCall struct {
	name   string
	value  float64
	labels map[string]string
}

type recordingMetrics struct {
	mu       sync.Mutex
	gauges   []metricCall
	counters []metricCall
	hists    []metricCall
	buckets  [][]float64
}

func (m *recordingMetrics) Counter(name string, value int64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = append(m.counters, metricCall{name: name, value: float64(value), labels: cloneLabels(labels)})
}

func (m *recordingMetrics) Gauge(name string, value float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges = append(m.gauges, metricCall{name: name, value: value, labels: cloneLabels(labels)})
}

func (m *recordingMetrics) Histogram(name string, value float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hists = append(m.hists, metricCall{name: name, value: value, labels: cloneLabels(labels)})
}

func (m *recordingMetrics) HistogramWithBuckets(name string, value float64, buckets []float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hists = append(m.hists, metricCall{name: name, value: value, labels: cloneLabels(labels)})
	m.buckets = append(m.buckets, append([]float64(nil), buckets...))
}

func (m *recordingMetrics) Timer(string, map[string]string) observe.ITimer {
	return &noopTimer{}
}

type noopTimer struct{}

func (t *noopTimer) Stop() {}

func cloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestMetricsMiddleware_InFlightGaugeIsSet(t *testing.T) {
	metrics := &recordingMetrics{}
	mw := MetricsMiddleware(MetricsConfig{Metrics: metrics})

	ctx := &stubContext{path: "/users/123"}
	err := mw(ctx, func() error { return nil })
	assert.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	if assert.Len(t, metrics.gauges, 2) {
		assert.Equal(t, "http_requests_in_flight", metrics.gauges[0].name)
		assert.Equal(t, float64(1), metrics.gauges[0].value)
		assert.Equal(t, map[string]string{"method": "GET", "path": "/users/:id"}, metrics.gauges[0].labels)

		assert.Equal(t, "http_requests_in_flight", metrics.gauges[1].name)
		assert.Equal(t, float64(0), metrics.gauges[1].value)
		assert.Equal(t, map[string]string{"method": "GET", "path": "/users/:id"}, metrics.gauges[1].labels)
	}
}

func TestMetricsMiddleware_Buckets_UsesBucketedHistogramWhenAvailable(t *testing.T) {
	metrics := &recordingMetrics{}
	mw := MetricsMiddleware(MetricsConfig{
		Metrics: metrics,
		Buckets: []float64{1, 2, 3},
	})

	ctx := &stubContext{path: "/users/123"}
	err := mw(ctx, func() error { return nil })
	assert.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	assert.NotEmpty(t, metrics.hists)
	assert.NotEmpty(t, metrics.buckets)
	assert.Equal(t, []float64{1, 2, 3}, metrics.buckets[0])
}
