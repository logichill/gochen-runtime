package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	coredialect "gochen/db/dialect"
	"strconv"
	"time"

	"gochen-runtime/db/dialect"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
)

const (
	defaultMaxPendingCount         = 10000
	defaultMaxFailedCount          = 1000
	defaultMaxDLQCount             = 100
	defaultMaxPendingAge           = 1 * time.Hour
	defaultHighRetryCountThreshold = 3
	defaultAvgPublishDelaySample   = 1000
)

// MetricsCollector 通过 SQL 查询采集 Outbox 指标，并据此计算健康状态。
type MetricsCollector struct {
	db             db.IDatabase
	outboxTable    string
	dlqTable       string
	outboxTableSQL string
	dlqTableSQL    string

	// 健康阈值配置
	maxPendingCount int64         // 待处理记录数阈值
	maxFailedCount  int64         // 失败记录数阈值
	maxDLQCount     int64         // DLQ 记录数阈值
	maxPendingAge   time.Duration // 最老待处理记录年龄阈值
}

// MetricsCollectorConfig 汇总指标采集器的表名和健康阈值配置。
type MetricsCollectorConfig struct {
	OutboxTable     string
	DLQTable        string
	MaxPendingCount int64
	MaxFailedCount  int64
	MaxDLQCount     int64
	MaxPendingAge   time.Duration
}

// DefaultMetricsCollectorConfig 返回指标采集器默认配置。
func DefaultMetricsCollectorConfig() MetricsCollectorConfig {
	return MetricsCollectorConfig{
		OutboxTable:     DefaultOutboxTable,
		DLQTable:        DefaultOutboxDLQTable,
		MaxPendingCount: defaultMaxPendingCount,
		MaxFailedCount:  defaultMaxFailedCount,
		MaxDLQCount:     defaultMaxDLQCount,
		MaxPendingAge:   defaultMaxPendingAge,
	}
}

// NewMetricsCollector 使用默认阈值创建一个 Outbox 指标采集器。
func NewMetricsCollector(db db.IDatabase) *MetricsCollector {
	collector, _ := newMetricsCollector(db, DefaultMetricsCollectorConfig(), false)
	return collector
}

// NewMetricsCollectorWithThresholds 使用自定义阈值创建指标采集器。
func NewMetricsCollectorWithThresholds(
	db db.IDatabase,
	maxPendingCount, maxFailedCount, maxDLQCount int64,
	maxPendingAge time.Duration,
) *MetricsCollector {
	cfg := DefaultMetricsCollectorConfig()
	cfg.MaxPendingCount = maxPendingCount
	cfg.MaxFailedCount = maxFailedCount
	cfg.MaxDLQCount = maxDLQCount
	cfg.MaxPendingAge = maxPendingAge
	collector, _ := newMetricsCollector(db, cfg, false)
	return collector
}

// NewMetricsCollectorWithConfig 创建使用显式配置的指标采集器。
func NewMetricsCollectorWithConfig(db db.IDatabase, cfg MetricsCollectorConfig) (*MetricsCollector, error) {
	return newMetricsCollector(db, cfg, true)
}

// NewMetricsCollectorForRepository 创建跟随 SQL Outbox 仓储表名配置的指标采集器。
func NewMetricsCollectorForRepository[ID comparable](
	db db.IDatabase,
	outboxRepo outbox.IOutboxRepository[ID],
) (*MetricsCollector, error) {
	if outboxRepo == nil {
		return nil, errors.NewCode(errors.InvalidInput, "outboxRepo cannot be nil")
	}
	outboxTableProvider, ok := outboxRepo.(IOutboxTableProvider)
	if !ok || outboxTableProvider.OutboxTableName() == "" {
		return nil, errors.NewCode(errors.InvalidInput, "outbox repository table name is required for metrics collector")
	}
	dlqTableProvider, ok := any(outboxRepo).(IDLQTableProvider)
	dlqTable := DefaultOutboxDLQTable
	if ok && dlqTableProvider.DLQTableName() != "" {
		dlqTable = dlqTableProvider.DLQTableName()
	}
	cfg := DefaultMetricsCollectorConfig()
	cfg.OutboxTable = outboxTableProvider.OutboxTableName()
	cfg.DLQTable = dlqTable
	return NewMetricsCollectorWithConfig(db, cfg)
}

// NewMetricsCollectorWithTables 创建使用指定 Outbox 与 DLQ 表名的指标采集器。
func NewMetricsCollectorWithTables(db db.IDatabase, outboxTable, dlqTable string) (*MetricsCollector, error) {
	return NewMetricsCollectorWithTablesAndThresholds(
		db,
		outboxTable,
		dlqTable,
		defaultMaxPendingCount,
		defaultMaxFailedCount,
		defaultMaxDLQCount,
		defaultMaxPendingAge,
	)
}

// NewMetricsCollectorWithTablesAndThresholds 创建使用指定表名和阈值的指标采集器。
func NewMetricsCollectorWithTablesAndThresholds(
	db db.IDatabase,
	outboxTable, dlqTable string,
	maxPendingCount, maxFailedCount, maxDLQCount int64,
	maxPendingAge time.Duration,
) (*MetricsCollector, error) {
	cfg := DefaultMetricsCollectorConfig()
	cfg.OutboxTable = outboxTable
	cfg.DLQTable = dlqTable
	cfg.MaxPendingCount = maxPendingCount
	cfg.MaxFailedCount = maxFailedCount
	cfg.MaxDLQCount = maxDLQCount
	cfg.MaxPendingAge = maxPendingAge
	return NewMetricsCollectorWithConfig(db, cfg)
}

func newMetricsCollector(db db.IDatabase, cfg MetricsCollectorConfig, validateTables bool) (*MetricsCollector, error) {
	if db == nil && validateTables {
		return nil, errors.NewCode(errors.InvalidInput, "metrics collector db cannot be nil")
	}
	if cfg.OutboxTable == "" {
		cfg.OutboxTable = DefaultOutboxTable
	}
	if cfg.DLQTable == "" {
		cfg.DLQTable = DefaultOutboxDLQTable
	}
	if cfg.MaxPendingCount <= 0 {
		cfg.MaxPendingCount = defaultMaxPendingCount
	}
	if cfg.MaxFailedCount <= 0 {
		cfg.MaxFailedCount = defaultMaxFailedCount
	}
	if cfg.MaxDLQCount <= 0 {
		cfg.MaxDLQCount = defaultMaxDLQCount
	}
	if cfg.MaxPendingAge <= 0 {
		cfg.MaxPendingAge = defaultMaxPendingAge
	}
	if validateTables {
		if err := validateTableName(cfg.OutboxTable); err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "invalid outbox table name").
				WithContext("table", cfg.OutboxTable)
		}
		if err := validateTableName(cfg.DLQTable); err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "invalid dlq table name").
				WithContext("table", cfg.DLQTable)
		}
	}
	d := dialect.FromDatabase(db)
	return &MetricsCollector{
		db:              db,
		outboxTable:     cfg.OutboxTable,
		dlqTable:        cfg.DLQTable,
		outboxTableSQL:  d.QuoteIdentifier(cfg.OutboxTable),
		dlqTableSQL:     d.QuoteIdentifier(cfg.DLQTable),
		maxPendingCount: cfg.MaxPendingCount,
		maxFailedCount:  cfg.MaxFailedCount,
		maxDLQCount:     cfg.MaxDLQCount,
		maxPendingAge:   cfg.MaxPendingAge,
	}, nil
}

// Collect 汇总当前 Outbox 的状态计数、重试情况和时间维度指标。
// 配置的 Outbox 与 DLQ 表必须可查询；任一采集失败返回错误。
func (c *MetricsCollector) Collect(ctx context.Context) (*outbox.OutboxMetrics, error) {
	if c == nil {
		return nil, errors.NewCode(errors.InvalidInput, "metrics collector is nil")
	}
	if c.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "metrics collector db cannot be nil")
	}
	metrics := &outbox.OutboxMetrics{
		CollectedAt: time.Now(),
	}

	// 1. 采集各状态的记录数
	if err := c.collectStatusCounts(ctx, metrics); err != nil {
		return nil, errors.Wrap(err, errors.Database, "collect status counts failed")
	}

	// 2. 采集 DLQ 记录数
	if err := c.collectDLQCount(ctx, metrics); err != nil {
		return nil, errors.Wrap(err, errors.Database, "collect dlq count failed")
	}

	// 3. 采集重试统计
	if err := c.collectRetryStats(ctx, metrics); err != nil {
		return nil, errors.Wrap(err, errors.Database, "collect retry stats failed")
	}

	// 4. 采集时间统计
	if err := c.collectTimeStats(ctx, metrics); err != nil {
		return nil, errors.Wrap(err, errors.Database, "collect time stats failed")
	}

	return metrics, nil
}

// collectStatusCounts 统计各状态记录数量。
func (c *MetricsCollector) collectStatusCounts(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	query := fmt.Sprintf(`
			SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
			COALESCE(SUM(CASE WHEN status = 'published' THEN 1 ELSE 0 END), 0) as published,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(CASE WHEN status = 'dead_lettered' THEN 1 ELSE 0 END), 0) as dead_lettered
		FROM %s
	`, c.quotedOutboxTable())

	err := c.db.QueryRow(ctx, query).Scan(
		&metrics.PendingCount,
		&metrics.PublishedCount,
		&metrics.FailedCount,
		&metrics.DeadLetteredCount,
	)
	if err != nil {
		return err
	}

	return nil
}

// collectDLQCount 统计死信表中的记录数量。
func (c *MetricsCollector) collectDLQCount(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, c.quotedDLQTable())

	err := c.db.QueryRow(ctx, query).Scan(&metrics.DLQCount)
	if err != nil {
		return err
	}

	return nil
}

// collectRetryStats 汇总当前未完成记录的最大、平均和高频重试次数。
func (c *MetricsCollector) collectRetryStats(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	query := fmt.Sprintf(`
			SELECT
			COALESCE(MAX(retry_count), 0) as max_retry,
			COALESCE(AVG(retry_count), 0) as avg_retry,
			COALESCE(SUM(CASE WHEN retry_count > %d THEN 1 ELSE 0 END), 0) as high_retry
		FROM %s
		WHERE status NOT IN ('published', 'dead_lettered')
	`, defaultHighRetryCountThreshold, c.quotedOutboxTable())

	err := c.db.QueryRow(ctx, query).Scan(
		&metrics.MaxRetryCount,
		&metrics.AvgRetryCount,
		&metrics.HighRetryCount,
	)
	if err != nil {
		return err
	}

	return nil
}

// collectTimeStats 统计最老待处理记录年龄和平均发布时间延迟。
func (c *MetricsCollector) collectTimeStats(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	query := fmt.Sprintf(`
		SELECT MIN(created_at)
		FROM %s
		WHERE status = 'pending'
	`, c.quotedOutboxTable())

	var oldestCreatedAtRaw any
	err := c.db.QueryRow(ctx, query).Scan(&oldestCreatedAtRaw)
	if err != nil {
		return err
	}

	if oldestCreatedAt, ok := parseSQLTimeValue(oldestCreatedAtRaw); ok {
		metrics.OldestPendingAge = time.Since(oldestCreatedAt)
	}

	return c.collectAvgPublishDelay(ctx, metrics)
}

func (c *MetricsCollector) collectAvgPublishDelay(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	expr, ok := avgPublishDelayNanosExpr(dialect.FromDatabase(c.db).Name())
	if !ok {
		return c.collectAvgPublishDelayFallback(ctx, metrics)
	}

	query := fmt.Sprintf(`
		SELECT COALESCE(AVG(%s), 0)
		FROM %s
		WHERE status = 'published' AND published_at IS NOT NULL AND (%s) >= 0
	`, expr, c.quotedOutboxTable(), expr)

	var avgNanosRaw any
	if err := c.db.QueryRow(ctx, query).Scan(&avgNanosRaw); err != nil {
		return err
	}
	if avgNanos, ok := parseSQLFloatValue(avgNanosRaw); ok && avgNanos > 0 {
		metrics.AvgPublishDelay = time.Duration(avgNanos).Round(time.Millisecond)
	}
	return nil
}

func avgPublishDelayNanosExpr(dialectName coredialect.Name) (string, bool) {
	switch dialectName {
	case coredialect.NameSQLite:
		return "(julianday(substr(CAST(published_at AS TEXT), 1, 19)) - julianday(substr(CAST(created_at AS TEXT), 1, 19))) * 86400000000000", true
	case coredialect.NameMySQL:
		return "TIMESTAMPDIFF(MICROSECOND, created_at, published_at) * 1000", true
	case coredialect.NamePostgres:
		return "EXTRACT(EPOCH FROM (published_at - created_at)) * 1000000000", true
	default:
		return "", false
	}
}

func (c *MetricsCollector) collectAvgPublishDelayFallback(ctx context.Context, metrics *outbox.OutboxMetrics) error {
	query := fmt.Sprintf(`
		SELECT created_at, published_at
		FROM %s
		WHERE status = 'published' AND published_at IS NOT NULL
		ORDER BY published_at DESC
		LIMIT ?
	`, c.quotedOutboxTable())

	rows, err := c.db.Query(ctx, query, defaultAvgPublishDelaySample)
	if err != nil {
		return err
	}
	defer rows.Close()

	var total time.Duration
	var count int64
	for rows.Next() {
		var createdAtRaw, publishedAtRaw any
		if err := rows.Scan(&createdAtRaw, &publishedAtRaw); err != nil {
			return err
		}
		createdAt, createdOK := parseSQLTimeValue(createdAtRaw)
		publishedAt, publishedOK := parseSQLTimeValue(publishedAtRaw)
		if !createdOK || !publishedOK || publishedAt.Before(createdAt) {
			continue
		}
		total += publishedAt.Sub(createdAt)
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 0 {
		metrics.AvgPublishDelay = total / time.Duration(count)
	}

	return nil
}

func (c *MetricsCollector) quotedOutboxTable() string {
	if c == nil || c.outboxTableSQL == "" {
		return ""
	}
	return c.outboxTableSQL
}

func (c *MetricsCollector) quotedDLQTable() string {
	if c == nil || c.dlqTableSQL == "" {
		return ""
	}
	return c.dlqTableSQL
}

func parseSQLFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case []byte:
		return parseFloatString(string(typed))
	case string:
		return parseFloatString(typed)
	}
	return 0, false
}

func parseFloatString(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed, err == nil
}

func parseSQLTimeValue(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		return typed, true
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, true
	case sql.NullTime:
		if !typed.Valid {
			return time.Time{}, false
		}
		return typed.Time, true
	case string:
		if parsed, ok := parseSQLTimeString(typed); ok {
			return parsed, true
		}
	case []byte:
		if parsed, ok := parseSQLTimeString(string(typed)); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseSQLTimeString(value string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// HealthStatus 根据当前采样结果判断 Outbox 是否健康。
func (c *MetricsCollector) HealthStatus(ctx context.Context) (outbox.HealthStatus, string, error) {
	metrics, err := c.Collect(ctx)
	if err != nil {
		return outbox.HealthStatusUnhealthy, "failed to collect metrics", err
	}
	return c.healthStatus(metrics)
}

func (c *MetricsCollector) healthStatus(metrics *outbox.OutboxMetrics) (outbox.HealthStatus, string, error) {
	if metrics == nil {
		return outbox.HealthStatusUnhealthy, "metrics is nil", errors.NewCode(errors.InvalidInput, "metrics cannot be nil")
	}
	// 检查各项指标
	var issues []string

	// 1. 待处理记录数过多
	if metrics.PendingCount > c.maxPendingCount {
		issues = append(issues, fmt.Sprintf("pending count too high: %d (threshold: %d)",
			metrics.PendingCount, c.maxPendingCount))
	}

	// 2. 失败记录数过多
	if metrics.FailedCount > c.maxFailedCount {
		issues = append(issues, fmt.Sprintf("failed count too high: %d (threshold: %d)",
			metrics.FailedCount, c.maxFailedCount))
	}

	// 3. DLQ 记录数过多
	if metrics.DLQCount > c.maxDLQCount {
		issues = append(issues, fmt.Sprintf("DLQ count too high: %d (threshold: %d)",
			metrics.DLQCount, c.maxDLQCount))
	}

	// 4. 最老待处理记录年龄过大
	if metrics.OldestPendingAge > c.maxPendingAge {
		issues = append(issues, fmt.Sprintf("oldest pending age too old: %s (threshold: %s)",
			metrics.OldestPendingAge, c.maxPendingAge))
	}

	// 判断健康状态
	if len(issues) == 0 {
		return outbox.HealthStatusHealthy, "all metrics within thresholds", nil
	}

	if len(issues) <= 2 {
		return outbox.HealthStatusDegraded, fmt.Sprintf("degraded: %v", issues), nil
	}

	return outbox.HealthStatusUnhealthy, fmt.Sprintf("unhealthy: %v", issues), nil
}

// TakeSnapshot 获取指标快照。
func (c *MetricsCollector) TakeSnapshot(ctx context.Context, labels map[string]string) (*outbox.MetricsSnapshot, error) {
	metrics, err := c.Collect(ctx)
	if err != nil {
		return nil, err
	}

	health, issues, err := c.healthStatus(metrics)
	if err != nil {
		return nil, err
	}

	return &outbox.MetricsSnapshot{
		Timestamp: time.Now(),
		Metrics:   metrics,
		Health:    health,
		Issues:    issues,
		Labels:    labels,
	}, nil
}
