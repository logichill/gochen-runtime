package sqlstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

func TestCleanupService_ArchiveOldRecords_EnsuresNewArchiveColumns(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	_, err := database.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS event_outbox_archive (
			id INTEGER PRIMARY KEY,
			aggregate_id INTEGER NOT NULL,
			aggregate_type TEXT NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			event_type TEXT NOT NULL,
			event_data TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			published_at DATETIME NULL,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NULL
		)
	`)
	require.NoError(t, err)

	publishedAt := time.Now().AddDate(0, 0, -10)
	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-1", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusPublished, "", publishedAt, publishedAt, 0, "", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays:  1,
		BatchSize:      10,
		ArchiveEnabled: true,
		ArchiveTable:   "event_outbox_archive",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(1), result.ArchivedCount)

	var archiveCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox_archive`).Scan(&archiveCount))
	assert.Equal(t, 1, archiveCount)

	var outboxCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount)

	columns, err := service.archiveTableColumns(ctx)
	require.NoError(t, err)
	_, hasClaimToken := columns["claim_token"]
	_, hasLeaseUntil := columns["lease_until"]
	_, hasNextRetryAt := columns["next_retry_at"]
	assert.True(t, hasClaimToken)
	assert.True(t, hasLeaseUntil)
	assert.True(t, hasNextRetryAt)
}

func TestCleanupService_DeleteOldRecords_WorksOnSQLiteWithoutDeleteLimit(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	publishedAt := time.Now().AddDate(0, 0, -10)
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-1", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusPublished, "", publishedAt, publishedAt, 0, "", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays:  1,
		BatchSize:      10,
		ArchiveEnabled: false,
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(1), result.DeletedCount)

	var outboxCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount)
}

func TestCleanupService_StatisticsHandlesEmptyTable(t *testing.T) {
	database := setupTestDB(t)
	service, err := NewCleanupService(database, CleanupPolicy{}, logging.NewNoopLogger())
	require.NoError(t, err)

	stats, err := service.Statistics(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(0), stats.PendingCount)
	require.Equal(t, int64(0), stats.PublishedCount)
	require.Equal(t, int64(0), stats.FailedCount)
	require.Equal(t, int64(0), stats.DeadLetteredCount)
	require.True(t, stats.OldestCreatedAt.IsZero())
	require.True(t, stats.NewestCreatedAt.IsZero())
}

func TestCleanupService_DeleteOldRecords_CleansDeadLetteredRows(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	createdAt := time.Now().AddDate(0, 0, -10)
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-dead", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusDeadLettered, "", createdAt, nil, 3, "dead-lettered", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays:  1,
		BatchSize:      10,
		ArchiveEnabled: false,
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(1), result.DeletedCount)

	var outboxCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount)
}

func TestCleanupService_StatisticsIncludesDeadLetteredCount(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	now := time.Now()
	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-published", "TestEvent", `{"ok":true}`, outbox.OutboxStatusPublished, "", now, now, 0, "", nil, nil,
		2, 1002, "Agg", "event-dead", "TestEvent", `{"ok":true}`, outbox.OutboxStatusDeadLettered, "", now, nil, 2, "failed", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{}, logging.NewNoopLogger())
	require.NoError(t, err)

	stats, err := service.Statistics(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.PublishedCount)
	require.Equal(t, int64(1), stats.DeadLetteredCount)
}

func TestCleanupService_ArchiveOldRecords_UsesSameBatchIDsForInsertAndDelete(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	base := time.Now().AddDate(0, 0, -10)
	for i, id := range []int64{10, 20, 30} {
		publishedAt := base.Add(time.Duration(i) * time.Minute)
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			id, 1000+id, "Agg", fmt.Sprintf("event-%d", id), "TestEvent", `{"ok":true}`,
			outbox.OutboxStatusPublished, "", publishedAt, publishedAt, 0, "", nil, nil,
		)
		require.NoError(t, err)
	}

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays:  1,
		BatchSize:      2,
		ArchiveEnabled: true,
		ArchiveTable:   "event_outbox_archive",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(3), result.ArchivedCount)

	archivedIDs := queryInt64List(t, database, ctx, `SELECT id FROM event_outbox_archive`)
	remainingIDs := queryInt64List(t, database, ctx, `SELECT id FROM event_outbox`)
	sort.Slice(archivedIDs, func(i, j int) bool { return archivedIDs[i] < archivedIDs[j] })
	sort.Slice(remainingIDs, func(i, j int) bool { return remainingIDs[i] < remainingIDs[j] })
	assert.Equal(t, []int64{10, 20, 30}, archivedIDs)
	assert.Empty(t, remainingIDs)
}

func TestCleanupService_ArchiveOldRecords_IgnoresAlreadyArchivedRows(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()

	_, err := database.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS event_outbox_archive (
			id INTEGER PRIMARY KEY,
			aggregate_id INTEGER NOT NULL,
			aggregate_type TEXT NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			event_type TEXT NOT NULL,
			event_data TEXT NOT NULL,
			status TEXT NOT NULL,
			claim_token TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			published_at DATETIME NULL,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NULL,
			lease_until DATETIME NULL,
			next_retry_at DATETIME NULL
		)
	`)
	require.NoError(t, err)

	publishedAt := time.Now().AddDate(0, 0, -10)
	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-1", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusPublished, "", publishedAt, publishedAt, 0, "", nil, nil,
	)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox_archive (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-1", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusPublished, "", publishedAt, publishedAt, 0, "", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays:  1,
		BatchSize:      10,
		ArchiveEnabled: true,
		ArchiveTable:   "event_outbox_archive",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(1), result.ArchivedCount)

	var outboxCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&outboxCount))
	assert.Equal(t, 0, outboxCount)

	var archiveCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox_archive`).Scan(&archiveCount))
	assert.Equal(t, 1, archiveCount)
}

func TestCleanupService_BuildArchiveInsertByIDsQuery_QuotesArchiveTable(t *testing.T) {
	database := setupTestDB(t)
	service, err := NewCleanupService(database, CleanupPolicy{
		ArchiveEnabled: true,
		ArchiveTable:   "EventOutboxArchive",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	query, args := service.buildArchiveInsertByIDsQuery([]int64{1, 2})

	assert.True(t, strings.Contains(query, `"EventOutboxArchive"`), query)
	assert.Equal(t, []any{int64(1), int64(2)}, args)
}

func TestCleanupService_ArchiveTableColumns_SupportsSQLiteSchemaQualifiedTable(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	service, err := NewCleanupService(database, CleanupPolicy{
		ArchiveEnabled: true,
		ArchiveTable:   "main.EventOutboxArchive",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	require.NoError(t, service.ensureArchiveTable(ctx))
	columns, err := service.archiveTableColumns(ctx)

	require.NoError(t, err)
	assert.Contains(t, columns, "claim_token")
	assert.Contains(t, columns, "lease_until")
	assert.Contains(t, columns, "next_retry_at")
}

func TestValidateTableName_AllowsTableOrSchemaQualifiedTable(t *testing.T) {
	for _, name := range []string{"event_outbox_archive", "EventOutboxArchive", "archive_2026", "public.event_outbox_archive"} {
		require.NoError(t, validateTableName(name), name)
	}
}

func TestValidateTableName_RejectsInvalidQualifiedTableNames(t *testing.T) {
	for _, name := range []string{"", ".", ".archive", "schema.", "schema..archive", "a.b.c", "schema.archive;drop", "1archive", "schema.1archive"} {
		require.Error(t, validateTableName(name), name)
	}
}

func TestCleanupService_RejectsNilDatabase(t *testing.T) {
	service, err := NewCleanupService(nil, CleanupPolicy{}, logging.NewNoopLogger())
	require.Error(t, err)
	require.Nil(t, service)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestCleanupService_UsesConfiguredOutboxTable(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	setupOutboxTable(t, database, "custom_event_outbox")

	createdAt := time.Now().AddDate(0, 0, -10)
	_, err := database.Exec(ctx, `
		INSERT INTO custom_event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		1, 1001, "Agg", "event-1", "TestEvent", `{"ok":true}`,
		outbox.OutboxStatusPublished, "", createdAt, createdAt, 0, "", nil, nil,
	)
	require.NoError(t, err)

	service, err := NewCleanupService(database, CleanupPolicy{
		RetentionDays: 1,
		BatchSize:     10,
		OutboxTable:   "custom_event_outbox",
	}, logging.NewNoopLogger())
	require.NoError(t, err)

	result, err := service.Cleanup(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.DeletedCount)

	var count int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM custom_event_outbox`).Scan(&count))
	require.Equal(t, 0, count)
}

func queryInt64List(t *testing.T, database db.IDatabase, ctx context.Context, query string, args ...any) []int64 {
	t.Helper()

	rows, err := database.Query(ctx, query, args...)
	require.NoError(t, err)
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		result = append(result, id)
	}
	require.NoError(t, rows.Err())
	return result
}
