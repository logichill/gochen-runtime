package middleware

import (
	"sync"
	"testing"

	"gochen/testkit/assert"

	"gochen/observe"
)

func TestMetricsMiddleware_NilContext(t *testing.T) {
	mw, err := MetricsMiddleware(MetricsConfig{})
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	nextCalled := false
	err = mw(nil, func() error {
		nextCalled = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, nextCalled)
}

func TestMetricsConfig_DefaultValues(t *testing.T) {
	cfg := MetricsConfig{}

	// 验证默认配置不会 panic
	mw, err := MetricsMiddleware(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, mw)
}

func TestMetricsConfig_WithNamespace(t *testing.T) {
	cfg := MetricsConfig{
		Namespace: "myapp",
	}

	mw, err := MetricsMiddleware(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, mw)
}

func TestMetricsConfig_WithSkipPaths(t *testing.T) {
	cfg := MetricsConfig{
		SkipPaths:        []string{"/health", "/ready"},
		SkipPathPrefixes: []string{"/internal/"},
	}

	mw, err := MetricsMiddleware(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, mw)
}

type metricCall struct {
	name   string
	value  float64
	labels map[string]string
}

type recordingMetrics struct {
	mu             sync.Mutex
	gauges         []metricCall
	counters       []metricCall
	hists          []metricCall
	registered     []registeredHistogram
	registerErr    error
}

type registeredHistogram struct {
	name    string
	help    string
	labels  []string
	buckets []float64
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

func (m *recordingMetrics) RegisterHistogram(name, help string, labelNames []string, buckets []float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.registerErr != nil {
		return m.registerErr
	}
	m.registered = append(m.registered, registeredHistogram{
		name:    name,
		help:    help,
		labels:  append([]string(nil), labelNames...),
		buckets: append([]float64(nil), buckets...),
	})
	return nil
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
	mw, err := MetricsMiddleware(MetricsConfig{Metrics: metrics})
	assert.NoError(t, err)

	ctx := &stubContext{path: "/users/123"}
	err = mw(ctx, func() error { return nil })
	assert.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	if assert.Len(t, metrics.gauges, 2) {
		assert.Equal(t, "http_requests_in_flight", metrics.gauges[0].name)
		assert.Equal(t, float64(1), metrics.gauges[0].value)
		// stubContext 不暴露路由模板：归入固定类别，不再猜测归一化路径。
		assert.Equal(t, map[string]string{"method": "GET", "path": "unpatterned"}, metrics.gauges[0].labels)

		assert.Equal(t, "http_requests_in_flight", metrics.gauges[1].name)
		assert.Equal(t, float64(0), metrics.gauges[1].value)
		assert.Equal(t, map[string]string{"method": "GET", "path": "unpatterned"}, metrics.gauges[1].labels)
	}
}

func TestMetricsMiddleware_Buckets_RegisterAtAssembly(t *testing.T) {
	metrics := &recordingMetrics{}
	mw, err := MetricsMiddleware(MetricsConfig{
		Metrics: metrics,
		Buckets: []float64{1, 2, 3},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	metrics.mu.Lock()
	if assert.Len(t, metrics.registered, 1) {
		assert.Equal(t, "http_request_duration_ms", metrics.registered[0].name)
		assert.Equal(t, []float64{1, 2, 3}, metrics.registered[0].buckets)
		assert.Equal(t, []string{"method", "path", "status"}, metrics.registered[0].labels)
	}
	metrics.mu.Unlock()

	ctx := &stubContext{path: "/users/123"}
	err = mw(ctx, func() error { return nil })
	assert.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.NotEmpty(t, metrics.hists)
}
