package sqlstore

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	coredialect "gochen/db/dialect"
	"strings"
	"testing"
	"time"

	"gochen/testkit/require"

	"gochen-runtime/db/dialect"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
)

func TestMetricsCollectorForRepositoryUsesConfiguredTables(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupOutboxTable(t, database, "custom_metrics_outbox")
	setupDLQTable(t, database, "custom_metrics_dlq")

	now := time.Now().UTC()
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(99), "WrongAggregate", "wrong-1", "WrongEvent", "{}", "pending", 0, now)
	require.NoError(t, err)

	_, err = database.Exec(ctx, `
		INSERT INTO custom_metrics_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at, published_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, NULL),
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, NULL),
			(?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, int64(1), "TestAggregate", "custom-1", "TestEvent", "{}", "pending", 2, now.Add(-10*time.Minute),
		int64(2), "TestAggregate", "custom-2", "TestEvent", "{}", "published", 0, now.Add(-5*time.Minute), now,
		int64(3), "TestAggregate", "custom-3", "TestEvent", "{}", "failed", 5, now.Add(-3*time.Minute),
		int64(4), "TestAggregate", "custom-4", "TestEvent", "{}", "dead_lettered", 7, now.Add(-2*time.Minute))
	require.NoError(t, err)

	_, err = database.Exec(ctx, `
		INSERT INTO custom_metrics_dlq (original_entry_id, aggregate_id, aggregate_type, event_id, event_type, event_data, retry_count, moved_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(1), int64(1), "TestAggregate", "dlq-1", "TestEvent", "{}", 5, now,
		int64(2), int64(2), "TestAggregate", "dlq-2", "TestEvent", "{}", 6, now)
	require.NoError(t, err)

	repo := tableProvidingOutboxRepository[int64]{
		table:    "custom_metrics_outbox",
		dlqTable: "custom_metrics_dlq",
	}
	collector, err := NewMetricsCollectorForRepository(database, repo)
	require.NoError(t, err)

	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), metrics.PendingCount)
	require.Equal(t, int64(1), metrics.PublishedCount)
	require.Equal(t, int64(1), metrics.FailedCount)
	require.Equal(t, int64(1), metrics.DeadLetteredCount)
	require.Equal(t, int64(2), metrics.DLQCount)
	require.Equal(t, 5, metrics.MaxRetryCount)
	require.Equal(t, int64(1), metrics.HighRetryCount)
}

func TestMetricsCollectorForRepositoryRejectsRepositoryWithoutTableName(t *testing.T) {
	database := setupTestDB(t)
	repo := &MockOutboxRepository{}

	collector, err := NewMetricsCollectorForRepository(database, repo)
	require.Error(t, err)
	require.Nil(t, collector)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "table name is required")
}

func TestMetricsCollectorCollectsZeroCountsForEmptyTables(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")

	collector := NewMetricsCollector(database)

	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), metrics.PendingCount)
	require.Equal(t, int64(0), metrics.PublishedCount)
	require.Equal(t, int64(0), metrics.FailedCount)
	require.Equal(t, int64(0), metrics.DeadLetteredCount)
	require.Equal(t, int64(0), metrics.HighRetryCount)
}

func TestMetricsCollectorReportsMissingDLQTable(t *testing.T) {
	collector := NewMetricsCollector(setupTestDB(t))
	metrics, err := collector.Collect(context.Background())
	require.Nil(t, metrics)
	require.True(t, errors.Is(err, errors.Database))
	require.Contains(t, err.Error(), "collect dlq count failed")
	require.Contains(t, err.Error(), DefaultOutboxDLQTable)
}

type dlqQueryFailureDB struct {
	db.IDatabase
	cause error
}

func (d dlqQueryFailureDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if strings.Contains(query, `"event_outbox_dlq"`) {
		return dlqQueryFailureRow{cause: d.cause}
	}
	return d.IDatabase.QueryRow(ctx, query, args...)
}

type dlqQueryFailureRow struct{ cause error }

func (r dlqQueryFailureRow) Scan(...any) error { return r.cause }

func (r dlqQueryFailureRow) Err() error { return r.cause }

func TestMetricsCollectorPropagatesDLQFailures(t *testing.T) {
	for _, failure := range []struct {
		name  string
		cause error
	}{
		{"permission", stderrors.New("permission denied")},
		{"connection", stderrors.New("connection reset")},
		{"cancellation", context.Canceled},
	} {
		t.Run(failure.name, func(t *testing.T) {
			database := setupTestDB(t)
			setupDLQTable(t, database, DefaultOutboxDLQTable)
			collector := NewMetricsCollector(dlqQueryFailureDB{IDatabase: database, cause: failure.cause})
			ctx := context.Background()
			metrics, err := collector.Collect(ctx)
			require.Nil(t, metrics)
			require.True(t, errors.Is(err, errors.Database))
			require.True(t, stderrors.Is(err, failure.cause))
			status, _, err := collector.HealthStatus(ctx)
			require.Equal(t, outbox.HealthStatusUnhealthy, status)
			require.True(t, stderrors.Is(err, failure.cause))
			snapshot, err := collector.TakeSnapshot(ctx, nil)
			require.Nil(t, snapshot)
			require.True(t, stderrors.Is(err, failure.cause))
		})
	}
}

func TestMetricsCollectorWithTablesQuotesSchemaQualifiedNames(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	d := dialect.FromDatabase(database)
	require.Equal(t, coredialect.NameSQLite, d.Name())

	_, err := database.Exec(ctx, `ATTACH DATABASE ':memory:' AS metrics_schema`)
	require.NoError(t, err)
	setupOutboxTable(t, database, d.QuoteIdentifier("metrics_schema.event_outbox"))
	setupDLQTable(t, database, d.QuoteIdentifier("metrics_schema.event_outbox_dlq"))

	now := time.Now().UTC()
	_, err = database.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, d.QuoteIdentifier("metrics_schema.event_outbox")),
		int64(1), "TestAggregate", "schema-event", "TestEvent", "{}", "pending", 0, now)
	require.NoError(t, err)

	collector, err := NewMetricsCollectorWithTables(database, "metrics_schema.event_outbox", "metrics_schema.event_outbox_dlq")
	require.NoError(t, err)

	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), metrics.PendingCount)
	require.Equal(t, int64(0), metrics.DLQCount)
	require.Equal(t, `"metrics_schema"."event_outbox"`, collector.quotedOutboxTable())
}

func TestMetricsCollectorWithConfigAppliesTablesAndThresholds(t *testing.T) {
	database := setupTestDB(t)
	setupOutboxTable(t, database, "configured_metrics_outbox")
	setupDLQTable(t, database, "configured_metrics_dlq")

	collector, err := NewMetricsCollectorWithConfig(database, MetricsCollectorConfig{
		OutboxTable:     "configured_metrics_outbox",
		DLQTable:        "configured_metrics_dlq",
		MaxPendingCount: 1,
		MaxFailedCount:  2,
		MaxDLQCount:     3,
		MaxPendingAge:   time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "configured_metrics_outbox", collector.outboxTable)
	require.Equal(t, "configured_metrics_dlq", collector.dlqTable)
	require.Equal(t, int64(1), collector.maxPendingCount)
	require.Equal(t, int64(2), collector.maxFailedCount)
	require.Equal(t, int64(3), collector.maxDLQCount)
	require.Equal(t, time.Minute, collector.maxPendingAge)
}

func TestMetricsCollectorCollectsAvgPublishDelayWithoutDialectFunction(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")

	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at, published_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(1), "TestAggregate", "published-1", "TestEvent", "{}", "published", 0, now.Add(-2*time.Minute), now,
		int64(2), "TestAggregate", "published-2", "TestEvent", "{}", "published", 0, now.Add(-4*time.Minute), now)
	require.NoError(t, err)

	wrappedDB := &fallbackMetricsDB{IDatabase: database}
	collector := NewMetricsCollector(wrappedDB)

	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, 3*time.Minute, metrics.AvgPublishDelay)
	require.NotEmpty(t, wrappedDB.queries)
	require.True(t, strings.Contains(wrappedDB.queries[len(wrappedDB.queries)-1], "LIMIT ?"))
	require.Equal(t, []any{defaultAvgPublishDelaySample}, wrappedDB.args[len(wrappedDB.args)-1])
}

func TestAvgPublishDelayNanosExprCoversDialects(t *testing.T) {
	tests := []struct {
		name coredialect.Name
		want string
		ok   bool
	}{
		{coredialect.NameSQLite, "julianday", true},
		{coredialect.NameMySQL, "TIMESTAMPDIFF", true},
		{coredialect.NamePostgres, "EXTRACT(EPOCH", true},
		{coredialect.Name("unknown"), "", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			expr, ok := avgPublishDelayNanosExpr(tt.name)
			require.Equal(t, tt.ok, ok)
			require.Contains(t, expr, tt.want)
		})
	}
}

func TestMetricsCollectorCollectsAvgPublishDelayWithSQLiteExpression(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")

	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at, published_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(1), "TestAggregate", "sqlite-published-1", "TestEvent", "{}", "published", 0, now.Add(-2*time.Minute), now,
		int64(2), "TestAggregate", "sqlite-published-2", "TestEvent", "{}", "published", 0, now.Add(-4*time.Minute), now)
	require.NoError(t, err)

	collector := NewMetricsCollector(database)
	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, 3*time.Minute, metrics.AvgPublishDelay)
}

func TestMetricsCollectorFallbackSkipsInvalidRows(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")

	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at, published_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(1), "TestAggregate", "valid-published", "TestEvent", "{}", "published", 0, now.Add(-2*time.Minute), now,
		int64(2), "TestAggregate", "negative-published", "TestEvent", "{}", "published", 0, now, now.Add(-time.Minute),
		int64(3), "TestAggregate", "invalid-published", "TestEvent", "{}", "published", 0, "not-a-time", now)
	require.NoError(t, err)

	collector := NewMetricsCollector(&fallbackMetricsDB{IDatabase: database})
	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, metrics.AvgPublishDelay)
}

type fallbackMetricsDB struct {
	db.IDatabase
	queries []string
	args    [][]any
}

func (d *fallbackMetricsDB) DialectName() string {
	return "unknown"
}

func (d *fallbackMetricsDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	d.queries = append(d.queries, query)
	d.args = append(d.args, append([]any(nil), args...))
	return d.IDatabase.Query(ctx, query, args...)
}

func TestMetricsCollectorWithTablesRejectsInvalidTableName(t *testing.T) {
	database := setupTestDB(t)

	_, err := NewMetricsCollectorWithTables(database, "event_outbox; DROP TABLE event_outbox", "event_outbox_dlq")
	require.Error(t, err)
}

func TestMetricsCollectorNilDBReturnsInvalidInput(t *testing.T) {
	collector := NewMetricsCollector(nil)
	_, err := collector.Collect(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	collector = NewMetricsCollectorWithThresholds(nil, 1, 1, 1, time.Minute)
	_, err = collector.Collect(context.Background())
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = NewMetricsCollectorWithTables(nil, "event_outbox", "event_outbox_dlq")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestParseSQLFloatValueBranches(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"nil", nil, 0, false},
		{"float64", float64(1.5), 1.5, true},
		{"float32", float32(2.5), 2.5, true},
		{"int64", int64(3), 3, true},
		{"int", 4, 4, true},
		{"bytes", []byte("5.5"), 5.5, true},
		{"string", "6.5", 6.5, true},
		{"invalid", "nope", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSQLFloatValue(tt.in)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSQLTimeValueBranches(t *testing.T) {
	base := time.Date(2026, 6, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		in   any
		ok   bool
	}{
		{"nil", nil, false},
		{"time", base, true},
		{"time pointer", &base, true},
		{"nil pointer", (*time.Time)(nil), false},
		{"null time valid", sql.NullTime{Time: base, Valid: true}, true},
		{"null time invalid", sql.NullTime{}, false},
		{"string", "2026-06-02 03:04:05", true},
		{"bytes", []byte("2026-06-02T03:04:05Z"), true},
		{"invalid", "not-a-time", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseSQLTimeValue(tt.in)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestParseSQLTimeStringWithoutOffsetUsesUTC(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("test-local", 8*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	parsed, ok := parseSQLTimeString("2026-06-02 03:04:05")

	require.True(t, ok)
	require.Equal(t, time.UTC, parsed.Location())
	require.Equal(t, "2026-06-02T03:04:05Z", parsed.Format(time.RFC3339))
}

func TestParseSQLTimeStringLayouts(t *testing.T) {
	inputs := []string{
		"2026-06-02T03:04:05Z",
		"2026-06-02T03:04:05.123456789Z",
		"2026-06-02 03:04:05-07:00",
		"2026-06-02 03:04:05.123456789-07:00",
		"2026-06-02 03:04:05.123456789",
		"2026-06-02 03:04:05",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, ok := parseSQLTimeString(input)
			require.True(t, ok)
		})
	}
}

// TestMetricsCollectorHighRetryCountRespectsThresholdBoundary 回归 R2d-F01：
// 高频重试的判定必须严格大于阈值（retry_count > defaultHighRetryCountThreshold），
// 边界值 retry_count == 3 不计入，retry_count == 4 计入。
func TestMetricsCollectorHighRetryCountRespectsThresholdBoundary(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")

	now := time.Now().UTC()
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (aggregate_id, aggregate_type, event_id, event_type, event_data, status, retry_count, created_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(1), "TestAggregate", "retry-2", "TestEvent", "{}", "pending", 2, now,
		int64(2), "TestAggregate", "retry-3", "TestEvent", "{}", "pending", 3, now,
		int64(3), "TestAggregate", "retry-4", "TestEvent", "{}", "pending", 4, now)
	require.NoError(t, err)

	collector := NewMetricsCollector(database)
	metrics, err := collector.Collect(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, metrics.MaxRetryCount)
	require.Equal(t, int64(1), metrics.HighRetryCount)
}

func TestMetricsCollectorHealthStatusRejectsNilMetrics(t *testing.T) {
	collector := NewMetricsCollector(setupTestDB(t))

	status, message, err := collector.healthStatus(nil)

	require.Equal(t, outbox.HealthStatusUnhealthy, status)
	require.Contains(t, message, "nil")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestMetricsCollectorWithTablesAndThresholdsAffectsHealthStatus(t *testing.T) {
	database := setupTestDB(t)
	collector, err := NewMetricsCollectorWithTablesAndThresholds(database, "event_outbox", "event_outbox_dlq", 1, 1, 1, time.Minute)
	require.NoError(t, err)

	status, message, err := collector.healthStatus(&outbox.OutboxMetrics{
		PendingCount:     2,
		FailedCount:      2,
		DLQCount:         2,
		OldestPendingAge: 2 * time.Minute,
	})

	require.NoError(t, err)
	require.Equal(t, outbox.HealthStatusUnhealthy, status)
	require.Contains(t, message, "pending count too high")
	require.Contains(t, message, "failed count too high")
	require.Contains(t, message, "DLQ count too high")
	require.Contains(t, message, "oldest pending age too old")
}
