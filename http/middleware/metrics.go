package middleware

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gochen/httpx"
	"gochen/observe"
)

const normalizedPathCacheMaxEntries = 4096

var (
	defaultNormalizedPathCache = newNormalizedPathCache()
)

type normalizedPathCacheStore struct {
	mu    sync.RWMutex
	cache map[string]string // path -> normalized path
	order []string
}

func newNormalizedPathCache() *normalizedPathCacheStore {
	return &normalizedPathCacheStore{cache: make(map[string]string)}
}

// MetricsConfig 指标中间件配置。
type MetricsConfig struct {
	// Metrics 指标收集器；默认使用无操作实现。
	Metrics observe.IMetrics

	// Namespace 指标命名空间前缀（可选）。
	Namespace string

	// SkipPaths 不记录指标的路径（精确匹配）。
	SkipPaths []string

	// SkipPathPrefixes 不记录指标的路径前缀。
	SkipPathPrefixes []string

	// Buckets 延迟直方图分桶（毫秒），默认使用标准分桶。
	Buckets []float64
}

type histogramWithBuckets interface {
	HistogramWithBuckets(name string, value float64, buckets []float64, labels map[string]string)
}

// MetricsMiddleware 创建 HTTP 请求指标中间件。
//
// 说明：
// - 记录请求计数（按 method/path/status 维度）；
// - 记录请求延迟直方图；
// - 记录当前进行中请求数。
//
// 指标名称：
// - {namespace}_http_requests_total：请求总数计数器
// - {namespace}_http_request_duration_ms：请求延迟直方图（毫秒）
// - {namespace}_http_requests_in_flight：进行中请求仪表盘
func MetricsMiddleware(cfg MetricsConfig) httpx.Middleware {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = &observe.DefaultMetrics{}
	}

	namespace := cfg.Namespace
	if namespace != "" && !strings.HasSuffix(namespace, "_") {
		namespace = namespace + "_"
	}

	filter := newPathFilter(cfg.SkipPaths, cfg.SkipPathPrefixes)

	requestsTotal := namespace + "http_requests_total"
	requestDuration := namespace + "http_request_duration_ms"
	requestsInFlight := namespace + "http_requests_in_flight"

	type inFlightEntry struct {
		counter  atomic.Int64
		lastZero atomic.Int64 // unix nano; 0 means not in zero state / unknown
	}

	var inFlight sync.Map // key -> *inFlightEntry
	var cleanupTick atomic.Uint64
	pathCache := newNormalizedPathCache()
	const cleanupEveryN = 1024
	const inFlightTTL = 10 * time.Minute

	return func(ctx httpx.IContext, next func() error) error {
		if ctx == nil {
			return next()
		}

		path := ctx.Path()

		// 检查是否跳过
		if filter.matches(path) {
			return next()
		}

		method := ctx.Method()
		normalizedPath := pathCache.normalize(path)

		// 记录进行中请求
		key := method + "\n" + normalizedPath
		entryAny, _ := inFlight.LoadOrStore(key, &inFlightEntry{})
		entry := entryAny.(*inFlightEntry)
		inFlightNow := entry.counter.Add(1)
		if inFlightNow == 1 {
			entry.lastZero.Store(0)
		}

		inFlightLabels := map[string]string{
			"method": method,
			"path":   normalizedPath,
		}
		metrics.Gauge(requestsInFlight, float64(inFlightNow), inFlightLabels)
		defer func() {
			inFlightNow := entry.counter.Add(-1)
			if inFlightNow < 0 {
				entry.counter.Store(0)
				inFlightNow = 0
			}
			metrics.Gauge(requestsInFlight, float64(inFlightNow), inFlightLabels)
			if inFlightNow == 0 {
				entry.lastZero.Store(time.Now().UnixNano())
				if cleanupTick.Add(1)%cleanupEveryN == 0 {
					now := time.Now().UnixNano()
					inFlight.Range(func(k any, v any) bool {
						e, ok := v.(*inFlightEntry)
						if !ok || e == nil {
							inFlight.Delete(k)
							return true
						}
						if e.counter.Load() != 0 {
							return true
						}
						z := e.lastZero.Load()
						if z <= 0 || time.Duration(now-z) < inFlightTTL {
							return true
						}
						// Best-effort cleanup: re-check zero state before deleting.
						if e.counter.Load() == 0 && e.lastZero.Load() == z {
							inFlight.Delete(k)
						}
						return true
					})
				}
			}
		}()

		start := time.Now()
		err := next()
		elapsed := time.Since(start)

		// 确定状态码
		status := responseStatus(ctx, err)

		// 请求计数
		labels := map[string]string{
			"method": method,
			"path":   normalizedPath,
			"status": strconv.Itoa(status),
		}
		metrics.Counter(requestsTotal, 1, labels)

		// 请求延迟
		if len(cfg.Buckets) > 0 {
			if hb, ok := metrics.(histogramWithBuckets); ok {
				hb.HistogramWithBuckets(requestDuration, float64(elapsed.Milliseconds()), cfg.Buckets, labels)
			} else {
				metrics.Histogram(requestDuration, float64(elapsed.Milliseconds()), labels)
			}
		} else {
			metrics.Histogram(requestDuration, float64(elapsed.Milliseconds()), labels)
		}

		return err
	}
}

// normalizePath 规范化路径，移除路径参数以减少指标基数。
//
// 例如：/users/123 -> /users/:id
func normalizePath(path string) string {
	return defaultNormalizedPathCache.normalize(path)
}

func (c *normalizedPathCacheStore) normalize(path string) string {
	if c == nil {
		return normalizePathUncached(path)
	}
	c.mu.RLock()
	cached, ok := c.cache[path]
	c.mu.RUnlock()
	if ok {
		return cached
	}

	normalized := normalizePathUncached(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.cache[path]; ok {
		return cached
	}
	if len(c.cache) >= normalizedPathCacheMaxEntries {
		c.evictOldest()
	}
	c.cache[path] = normalized
	c.order = append(c.order, path)
	return normalized
}

func (c *normalizedPathCacheStore) evictOldest() {
	for len(c.order) > 0 {
		oldest := c.order[0]
		copy(c.order, c.order[1:])
		c.order[len(c.order)-1] = ""
		c.order = c.order[:len(c.order)-1]
		if _, ok := c.cache[oldest]; ok {
			delete(c.cache, oldest)
			return
		}
	}
}

func clearNormalizedPathCache() {
	defaultNormalizedPathCache = newNormalizedPathCache()
}

func normalizedPathCacheLen() int {
	defaultNormalizedPathCache.mu.RLock()
	defer defaultNormalizedPathCache.mu.RUnlock()
	return len(defaultNormalizedPathCache.cache)
}

func normalizePathUncached(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		// 检测数字 ID
		if isNumeric(part) {
			parts[i] = ":id"
			continue
		}
		// 检测 UUID
		if isUUID(part) {
			parts[i] = ":uuid"
			continue
		}
	}
	return strings.Join(parts, "/")
}

// isNumeric 判断Numeric。
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isUUID 判断UUID。
func isUUID(s string) bool {
	// UUID 格式：8-4-4-4-12，共 36 个字符
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}
