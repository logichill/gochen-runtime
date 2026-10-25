package middleware

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gochen/errors"
	"gochen/httpx"
	"gochen/observe"
)

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

	// Buckets 延迟直方图分桶（毫秒）。
	//
	// 分桶（配置值或默认的 DefaultDurationBucketsMS）在装配阶段注册生效：
	// 收集器必须支持带分桶注册（histogramRegistrar），注册失败（含同名指标
	// 已被不同配置注册）立即返回错误，不允许运行期静默退回默认分桶。
	// 观察值与分桶同为毫秒。
	Buckets []float64
}

// DefaultDurationBucketsMS 未配置 Buckets 时使用的默认耗时分桶（毫秒）。
//
// 与 Prometheus 默认分桶（0.005…10，秒尺度）不同：本中间件的观察值是毫秒，
// 默认分桶必须与观察值同单位，否则典型 Web 请求（数十毫秒）会全部落入
// 最后一个有限桶，延迟分布不可用。
var DefaultDurationBucketsMS = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// histogramRegistrar 声明“可按分桶注册直方图”的收集器能力。
type histogramRegistrar interface {
	RegisterHistogram(name, help string, labelNames []string, buckets []float64) error
}

// routePatternProvider 可选能力：请求上下文暴露命中的路由模板。
//
// 由 HTTP 适配层实现（nethttp 基于 Go 1.22+ ServeMux 的 r.Pattern，
// gin 基于 FullPath），未命中任何路由时返回空字符串。
type routePatternProvider interface {
	RoutePattern() string
}

// UnmatchedRouteLabel 未命中路由的请求统一使用的 path 标签值。
//
// 未命中路由的原始路径完全由客户端控制，进入指标标签会造成无基数上限的
// 时间序列增长，因此归入固定类别；高基数信息应进入日志或 trace。
const UnmatchedRouteLabel = "unmatched"

// UnpatternedRouteLabel 上下文不暴露路由模板时使用的 path 标签值。
//
// 没有模板就无法从任意路径中可靠区分静态段与动态段（历史上按数字/UUID
// 猜测的归一化对 slug 类路径无效，仍会产生无界标签）。固定类别牺牲了
// 静态路径的区分度，换取标签集合有界；需要精确路径分布时给适配层实现
// RoutePattern，而不是退回猜测。
const UnpatternedRouteLabel = "unpatterned"

// MetricsMiddleware 创建 HTTP 请求指标中间件。
//
// 说明：
// - 记录请求计数（按 method/path/status 维度）；
// - 记录请求延迟直方图（毫秒）；
// - 记录当前进行中请求数。
//
// path 标签使用路由模板（如 /items/:slug 或 /items/{slug}）而非实际路径：
// 同一路由的任意动态路径值只产生一个标签值；未命中路由归入
// UnmatchedRouteLabel；上下文不暴露路由模板归入 UnpatternedRouteLabel。
//
// 指标名称：
// - {namespace}_http_requests_total：请求总数计数器
// - {namespace}_http_request_duration_ms：请求延迟直方图（毫秒）
// - {namespace}_http_requests_in_flight：进行中请求仪表盘
func MetricsMiddleware(cfg MetricsConfig) (httpx.Middleware, error) {
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

	// 分桶在装配期统一声明并注册（配置值或毫秒默认值）：收集器不支持注册、
	// 分桶非法、或同名指标已按别的配置存在，都在这里明确失败，
	// 不进入“配置了但静默用默认分桶”的运行态。
	registrar, ok := metrics.(histogramRegistrar)
	if !ok || registrar == nil {
		return nil, errors.NewCode(errors.Unsupported,
			"metrics collector does not support histogram registration; use a registration-capable collector (e.g. observe.DefaultMetrics or contrib prometheus)")
	}
	buckets := cfg.Buckets
	if len(buckets) == 0 {
		buckets = DefaultDurationBucketsMS
	}
	if err := validateDurationBuckets(buckets); err != nil {
		return nil, err
	}
	if err := registrar.RegisterHistogram(requestDuration,
		"HTTP request duration in milliseconds",
		[]string{"method", "path", "status"}, buckets); err != nil {
		return nil, errors.Wrap(err, errors.FailedPrecondition,
			"failed to register http duration histogram with configured buckets")
	}

	type inFlightEntry struct {
		counter  atomic.Int64
		lastZero atomic.Int64 // unix nano; 0 means not in zero state / unknown
	}

	var inFlight sync.Map // key -> *inFlightEntry
	var cleanupTick atomic.Uint64
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
		normalizedPath := routePathLabel(ctx)

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

		// 请求延迟（毫秒，与注册阶段的毫秒分桶同一单位）
		metrics.Histogram(requestDuration, float64(elapsed.Milliseconds()), labels)

		return err
	}, nil
}

// validateDurationBuckets 在装配期校验耗时分桶（毫秒）。
//
// 非有限值、非正数或非严格递增的分桶对直方图没有意义；这类配置如果
// 放到运行期才暴露（某些后端在首个观察值上才 panic），故障会迟到且难定位。
func validateDurationBuckets(buckets []float64) error {
	if len(buckets) == 0 {
		return errors.NewCode(errors.InvalidInput, "duration buckets cannot be empty")
	}
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			return errors.NewCode(errors.InvalidInput, "duration buckets must be finite").
				WithContext("index", i)
		}
		if b <= 0 {
			return errors.NewCode(errors.InvalidInput, "duration buckets must be positive").
				WithContext("index", i)
		}
		if i > 0 && b <= buckets[i-1] {
			return errors.NewCode(errors.InvalidInput, "duration buckets must be strictly increasing").
				WithContext("index", i).
				WithContext("previous", buckets[i-1]).
				WithContext("current", b)
		}
	}
	return nil
}

// routePathLabel 决定本次请求的 path 指标标签。
//
//	模板可用            → 路由模板（"/items/{slug}" / "/items/:slug"）
//	模板能力存在但未命中 → UnmatchedRouteLabel
//	上下文无模板能力     → UnpatternedRouteLabel
func routePathLabel(ctx httpx.IContext) string {
	provider, ok := ctx.(routePatternProvider)
	if !ok || provider == nil {
		return UnpatternedRouteLabel
	}
	pattern := strings.TrimSpace(provider.RoutePattern())
	if pattern == "" {
		return UnmatchedRouteLabel
	}
	return pattern
}
