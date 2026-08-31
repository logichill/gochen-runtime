package sqlstore

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

type rowsAffectedErrOutboxDB struct {
	db.IDatabase
}

func (d rowsAffectedErrOutboxDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.IDatabase.Exec(ctx, query, args...)
	if err == nil {
		return rowsAffectedErrOutboxResult{Result: result}, nil
	}
	return result, err
}

type rowsAffectedErrOutboxResult struct {
	sql.Result
}

func (r rowsAffectedErrOutboxResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unavailable")
}

// TestSQLOutboxRepository_SaveWithEvents 验证 SQLOutboxRepository SaveWithEvents。
func TestSQLOutboxRepository_SaveWithEvents(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	aggregateID := int64(123)

	events := []eventing.Event[int64]{
		newTestEvent(aggregateID, 1, "event-1", map[string]any{"value": 100}),
		newTestEvent(aggregateID, 2, "event-2", map[string]any{"value": 200}),
	}

	err = repo.SaveWithEvents(ctx, aggregateID, events)
	assert.NoError(t, err)

	assert.Len(t, eventStore.events, 2)
	assert.Equal(t, "event-1", eventStore.events[0].GetID())
	assert.Equal(t, "event-2", eventStore.events[1].GetID())

	entries, err := repo.ClaimPendingEntries(ctx, 10)
	assert.NoError(t, err)
	assert.Len(t, entries, 2)

	entry1 := entries[0]
	assert.Equal(t, aggregateID, entry1.AggregateID)
	assert.Equal(t, "TestAggregate", entry1.AggregateType)
	assert.Equal(t, "event-1", entry1.EventID)
	assert.Equal(t, "TestEvent", entry1.EventType)
	assert.Equal(t, outbox.OutboxStatusProcessing, entry1.Status)
	assert.NotEmpty(t, entry1.ClaimToken)
	assert.Equal(t, 0, entry1.RetryCount)
	assert.Nil(t, entry1.PublishedAt)
	assert.NotNil(t, entry1.LeaseUntil)
	assert.Nil(t, entry1.NextRetryAt)
	assert.NotEmpty(t, entry1.EventData)
}

func TestSQLOutboxRepository_WithOutboxTableUsesConfiguredTable(t *testing.T) {
	database := setupTestDB(t)
	setupOutboxTable(t, database, "custom_event_outbox")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(
		database,
		eventStore,
		logging.NewNoopLogger(),
		WithOutboxTable("custom_event_outbox"),
	)
	require.NoError(t, err)
	require.Equal(t, "custom_event_outbox", repo.OutboxTableName())

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 123, []eventing.Event[int64]{
		newTestEvent(123, 1, "event-custom-table", map[string]any{"value": 100}),
	}))

	var customCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM custom_event_outbox WHERE event_id = ?`, "event-custom-table").Scan(&customCount))
	require.Equal(t, 1, customCount)
	var defaultCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, "event-custom-table").Scan(&defaultCount))
	require.Equal(t, 0, defaultCount)
}

func TestSQLOutboxRepository_WithOutboxTableRejectsInvalidTableName(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	_, err := NewSimpleSQLOutboxRepository(
		database,
		eventStore,
		logging.NewNoopLogger(),
		WithOutboxTable("event_outbox; DROP TABLE event_outbox"),
	)
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected InvalidInput for invalid table name, got %v", err)
}

func TestSQLOutboxRepository_SaveWithEventsChunksOutboxInsertsByParameterBudget(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	aggregateID := int64(123)
	eventCount := outboxInsertChunkSize(9) + 2
	events := make([]eventing.Event[int64], 0, eventCount)
	for i := 1; i <= eventCount; i++ {
		events = append(events, newTestEvent(aggregateID, uint64(i), fmt.Sprintf("event-chunk-%d", i), nil))
	}

	require.NoError(t, repo.SaveWithEvents(ctx, aggregateID, events))
	require.Len(t, eventStore.events, eventCount)

	entries, err := repo.ClaimPendingEntries(ctx, eventCount+1)
	require.NoError(t, err)
	require.Len(t, entries, eventCount)
}

func TestSQLOutboxRepository_SaveWithEvents_DuplicateOutboxEntryIsIdempotent(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	events := []eventing.Event[int64]{
		newTestEvent(123, 1, "event-idempotent", map[string]any{"value": 100}),
	}

	require.NoError(t, repo.SaveWithEvents(ctx, 123, events))
	require.NoError(t, repo.SaveWithEvents(ctx, 123, events))

	entries, err := repo.ClaimPendingEntries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "event-idempotent", entries[0].EventID)
}

func TestSQLOutboxRepository_SaveWithEvents_DuplicateOutboxEntryDifferentContentFails(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 123, []eventing.Event[int64]{
		newTestEvent(123, 1, "event-conflict", map[string]any{"value": 100}),
	}))
	err = repo.SaveWithEvents(ctx, 123, []eventing.Event[int64]{
		newTestEvent(123, 1, "event-conflict", map[string]any{"value": 200}),
	})

	require.Error(t, err)
	assert.Truef(t, errors.Is(err, errors.Duplicate), "expected duplicate error, got %v", err)
}

func TestSQLOutboxRepository_SaveWithEvents_RejectsZeroVersion(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	err = repo.SaveWithEvents(context.Background(), 123, []eventing.Event[int64]{
		newTestEvent(123, 0, "event-zero-version", map[string]any{"value": 100}),
	})

	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input error, got %v", err)
	require.Empty(t, eventStore.events)
}

func TestSQLOutboxRepository_SaveOutboxEntries_DuplicateFallbackUsesTxFirst(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	evt := newTestEvent(123, 1, "event-repeatable-read", map[string]any{"value": 100})
	eventData, err := repo.serializeEvent(&evt)
	require.NoError(t, err)

	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox (
			aggregate_id, aggregate_type, event_id, event_type, event_data, status, claim_token, created_at, retry_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, int64(123), "TestAggregate", evt.GetID(), "TestEvent", eventData, outbox.OutboxStatusPending, "", time.Now(), 0)
	require.NoError(t, err)

	tx := &duplicateInsertTx{IDatabase: database}
	err = repo.saveOutboxEntries(ctx, tx, 123, []eventing.IStorableEvent[int64]{&evt})
	require.NoError(t, err)
	require.Equal(t, 1, tx.queryRowCalls, "duplicate fallback should check tx-visible state before repository db")
	require.Greater(t, tx.rollbackTo, 0, "duplicate fallback should rollback to savepoint before checking duplicate")
}

func TestSQLOutboxRepository_FilterMissingOutboxEntriesUsesBatchLookup(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	events := []eventing.Event[int64]{
		newTestEvent(123, 1, "event-batch-idempotent-1", map[string]any{"value": 1}),
		newTestEvent(123, 2, "event-batch-idempotent-2", map[string]any{"value": 2}),
		newTestEvent(123, 3, "event-batch-idempotent-3", map[string]any{"value": 3}),
	}
	prepared := make([]outboxEntryInsert[int64], 0, len(events))
	for i := range events {
		eventData, err := repo.serializeEvent(&events[i])
		require.NoError(t, err)
		_, err = database.Exec(ctx, `
			INSERT INTO event_outbox (
				aggregate_id, aggregate_type, event_id, event_type, event_data, status, claim_token, created_at, retry_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, int64(123), "TestAggregate", events[i].GetID(), "TestEvent", eventData, outbox.OutboxStatusPending, "", time.Now(), 0)
		require.NoError(t, err)
		prepared = append(prepared, outboxEntryInsert[int64]{
			event:         &events[i],
			eventData:     eventData,
			aggregateType: events[i].GetAggregateType(),
			eventID:       events[i].GetID(),
			eventType:     events[i].GetType(),
			createdAt:     time.Now(),
		})
	}

	countingDB := &batchOutboxLookupDB{IDatabase: database}
	missing, err := repo.filterMissingOutboxEntries(ctx, countingDB, 123, prepared)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Equal(t, 1, countingDB.batchLookups)
}

func TestSQLOutboxRepository_SameOutboxEntryRejectsUnsafeTableName(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	repo.outboxTable = "event_outbox;drop"

	evt := newTestEvent(123, 1, "event-unsafe-table", map[string]any{"value": 100})
	eventData, err := repo.serializeEvent(&evt)
	require.NoError(t, err)

	same, err := repo.sameOutboxEntry(context.Background(), database, 123, &evt, eventData)
	require.Error(t, err)
	require.False(t, same)
}

func TestSQLOutboxRepository_SameOutboxEntryQuotesTableName(t *testing.T) {
	eventStore := &MockEventStoreWithDB{}
	db := quotedTableCaptureDB{}
	repo, err := NewSimpleSQLOutboxRepository(db, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	repo.outboxTable = "tenant_01.event_outbox"

	evt := newTestEvent(123, 1, "event-quoted-table", map[string]any{"value": 100})
	eventData, err := repo.serializeEvent(&evt)
	require.NoError(t, err)
	db.eventData = eventData

	same, err := repo.sameOutboxEntry(context.Background(), db, 123, &evt, eventData)
	require.NoError(t, err)
	require.True(t, same)
}

type quotedTableCaptureDB struct {
	db.IDatabase
	eventData string
}

func (quotedTableCaptureDB) DialectName() string { return "sqlite" }

func (d quotedTableCaptureDB) QueryRow(_ context.Context, query string, args ...any) db.IRow {
	if !strings.Contains(query, `FROM "tenant_01"."event_outbox"`) {
		return errorRow{err: fmt.Errorf("query did not quote table name: %s", query)}
	}
	if len(args) != 1 || args[0] != "event-quoted-table" {
		return errorRow{err: fmt.Errorf("unexpected args: %v", args)}
	}
	return sameOutboxEntryRow{eventData: d.eventData}
}

type sameOutboxEntryRow struct {
	eventData string
}

func (r sameOutboxEntryRow) Scan(dest ...any) error {
	*dest[0].(*any) = int64(123)
	*dest[1].(*string) = "TestAggregate"
	*dest[2].(*string) = "TestEvent"
	*dest[3].(*string) = r.eventData
	return nil
}

func (sameOutboxEntryRow) Err() error { return nil }

func TestIsOutboxDuplicateKeyErrorRejectsGenericIntegritySQLState(t *testing.T) {
	// MySQL 唯一冲突通过 1062/"duplicate entry" 文本识别。
	require.True(t, isOutboxDuplicateKeyError(stderrors.New("Error 1062: Duplicate entry 'x' for key 'PRIMARY'")))
	// 通用完整性约束 SQLSTATE 23000/23001 可能是 FK/NOT NULL/CHECK，不应被当作唯一冲突。
	require.False(t, isOutboxDuplicateKeyError(stderrors.New("Error 1452 (SQLSTATE 23000): foreign key constraint fails")))
	require.False(t, isOutboxDuplicateKeyError(stderrors.New("SQLSTATE 23001: restrict violation")))
}

func TestIsOutboxDuplicateKeyErrorRecognizesSQLiteConstraint(t *testing.T) {
	database := setupTestDB(t)

	ctx := context.Background()
	_, err := database.Exec(ctx, "CREATE TABLE t (id TEXT PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.Exec(ctx, "INSERT INTO t (id) VALUES (?)", "a")
	require.NoError(t, err)
	_, err = database.Exec(ctx, "INSERT INTO t (id) VALUES (?)", "a")
	require.Error(t, err)
	require.True(t, isOutboxDuplicateKeyError(err))
}

type sqliteDriverMethodError struct {
	code int
}

func (e sqliteDriverMethodError) Error() string { return "sqlite constraint" }
func (e sqliteDriverMethodError) Code() int     { return e.code }

type sqliteDriverFieldError struct {
	Code         int
	ExtendedCode int
}

func (e sqliteDriverFieldError) Error() string { return "sqlite constraint" }

func TestIsOutboxDuplicateKeyErrorRecognizesDriverAgnosticSQLiteCodes(t *testing.T) {
	// 精确的扩展唯一码 2067(SQLITE_CONSTRAINT_UNIQUE) 识别为唯一冲突。
	require.True(t, isOutboxDuplicateKeyError(fmt.Errorf("wrapped: %w", sqliteDriverFieldError{ExtendedCode: 2067})))
	// 基础码 19(SQLITE_CONSTRAINT) 还覆盖 FK/NOT NULL/CHECK，不应被当作唯一冲突。
	require.False(t, isOutboxDuplicateKeyError(fmt.Errorf("wrapped: %w", sqliteDriverMethodError{code: 19})))
}

type duplicateInsertTx struct {
	db.IDatabase
	queryRowCalls int
	aborted       bool
	rollbackTo    int
}

func (t *duplicateInsertTx) Exec(context.Context, string, ...any) (sql.Result, error) {
	t.aborted = true
	return nil, stderrors.New("SQLSTATE 23505: duplicate key")
}

func (t *duplicateInsertTx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	t.queryRowCalls++
	if t.aborted {
		return errorRow{err: stderrors.New("current transaction is aborted")}
	}
	return t.IDatabase.QueryRow(ctx, query, args...)
}

func (t *duplicateInsertTx) Commit() error {
	return nil
}

func (t *duplicateInsertTx) Rollback() error {
	return nil
}

func (t *duplicateInsertTx) CreateSavepoint(context.Context, string) error {
	return nil
}

func (t *duplicateInsertTx) RollbackToSavepoint(context.Context, string) error {
	t.aborted = false
	t.rollbackTo++
	return nil
}

func (t *duplicateInsertTx) ReleaseSavepoint(context.Context, string) error {
	return nil
}

type batchOutboxLookupDB struct {
	db.IDatabase
	batchLookups int
}

func (d *batchOutboxLookupDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "event_id IN") {
		d.batchLookups++
	}
	return d.IDatabase.Query(ctx, query, args...)
}

type errorRow struct {
	err error
}

func (r errorRow) Scan(...any) error { return r.err }
func (r errorRow) Err() error        { return r.err }

// TestSQLOutboxRepository_ClaimPendingEntries 验证 SQLOutboxRepository ClaimPendingEntries。
func TestSQLOutboxRepository_ClaimPendingEntries(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()

	events := []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
		newTestEvent(2, 1, "event-2", nil),
	}

	err = repo.SaveWithEvents(ctx, 1, events[:1])
	assert.NoError(t, err)

	err = repo.SaveWithEvents(ctx, 2, events[1:])
	assert.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 10)
	assert.NoError(t, err)
	assert.Len(t, entries, 2)

	assert.Equal(t, "event-1", entries[0].EventID)
	assert.Equal(t, "event-2", entries[1].EventID)

	entries, err = repo.ClaimPendingEntries(ctx, 1)
	assert.NoError(t, err)
	assert.Len(t, entries, 0)
}

func TestSQLOutboxRepository_ClaimPendingEntries_ReclaimsExpiredProcessingLease(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	_, err = database.Exec(ctx,
		`UPDATE event_outbox SET lease_until = ? WHERE id = ?`,
		time.Now().Add(-time.Minute),
		entries[0].ID,
	)
	require.NoError(t, err)

	entries, err = repo.ClaimPendingEntries(ctx, 1)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "event-1", entries[0].EventID)
	assert.Equal(t, outbox.OutboxStatusProcessing, entries[0].Status)
	assert.NotEmpty(t, entries[0].ClaimToken)
}

func TestSQLOutboxRepository_ClaimPendingEntries_UsesConfiguredClaimLease(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	cfg := outbox.DefaultOutboxConfig()
	cfg.ClaimLease = 30 * time.Second
	repo, err := NewSimpleSQLOutboxRepositoryWithConfig(database, eventStore, logging.NewNoopLogger(), cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	before := time.Now()
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].LeaseUntil)
	require.True(t, entries[0].LeaseUntil.After(before.Add(20*time.Second)))
	require.True(t, entries[0].LeaseUntil.Before(before.Add(40*time.Second)))
}

func TestSQLOutboxRepository_ClaimPendingEntries_DoesNotUpdateUnselectedEligibleRows(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-pending", nil),
		newTestEvent(2, 1, "event-failed", nil),
		newTestEvent(3, 1, "event-expired-processing", nil),
	}))

	base := time.Now().Add(-10 * time.Minute)
	_, err = database.Exec(ctx, `
		UPDATE event_outbox
		SET created_at = CASE event_id
				WHEN 'event-pending' THEN ?
				WHEN 'event-failed' THEN ?
				WHEN 'event-expired-processing' THEN ?
			END,
			status = CASE event_id
				WHEN 'event-pending' THEN 'pending'
				WHEN 'event-failed' THEN 'failed'
				WHEN 'event-expired-processing' THEN 'processing'
			END,
			claim_token = CASE event_id
				WHEN 'event-expired-processing' THEN 'expired-token'
				ELSE ''
			END,
			lease_until = CASE event_id
				WHEN 'event-expired-processing' THEN ?
				ELSE NULL
			END,
			next_retry_at = CASE event_id
				WHEN 'event-failed' THEN ?
				ELSE NULL
			END
	`, base, base.Add(time.Minute), base.Add(2*time.Minute), base.Add(-time.Minute), base.Add(-time.Minute))
	require.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "event-pending", entries[0].EventID)

	var failedStatus, failedToken string
	row := database.QueryRow(ctx, `SELECT status, claim_token FROM event_outbox WHERE event_id = ?`, "event-failed")
	require.NoError(t, row.Scan(&failedStatus, &failedToken))
	require.Equal(t, string(outbox.OutboxStatusFailed), failedStatus)
	require.Empty(t, failedToken)

	var expiredStatus, expiredToken string
	row = database.QueryRow(ctx, `SELECT status, claim_token FROM event_outbox WHERE event_id = ?`, "event-expired-processing")
	require.NoError(t, row.Scan(&expiredStatus, &expiredToken))
	require.Equal(t, string(outbox.OutboxStatusProcessing), expiredStatus)
	require.Equal(t, "expired-token", expiredToken)
}

func TestBatchClaimedEntriesWhere_WrapsDisjunction(t *testing.T) {
	entries := []outbox.ClaimedEntry{
		{ID: 1, ClaimToken: "claim-a"},
		{ID: 2, ClaimToken: "claim-b"},
	}

	require.Equal(t, "((id = ? AND claim_token = ?) OR (id = ? AND claim_token = ?))", claimedEntriesWhere(entries))
	require.Equal(t, []any{int64(1), "claim-a", int64(2), "claim-b"}, claimedEntriesArgs(entries))
}

// TestSQLOutboxRepository_MarkAsPublished 验证 SQLOutboxRepository MarkAsPublished。
func TestSQLOutboxRepository_MarkAsPublished(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()

	events := []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}

	err = repo.SaveWithEvents(ctx, 1, events)
	assert.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)

	entryID := entries[0].ID

	err = repo.MarkAsPublished(ctx, entryID, entries[0].ClaimToken)
	assert.NoError(t, err)

	entries, err = repo.ClaimPendingEntries(ctx, 10)
	assert.NoError(t, err)
	assert.Len(t, entries, 0)
}

// TestSQLOutboxRepository_MarkAsFailed 验证 SQLOutboxRepository MarkAsFailed。
func TestSQLOutboxRepository_MarkAsFailed(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()

	events := []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}

	err = repo.SaveWithEvents(ctx, 1, events)
	assert.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)

	entryID := entries[0].ID
	errorMsg := "test error"
	nextRetryAt := time.Now().Add(time.Minute)

	err = repo.MarkAsFailed(ctx, entryID, entries[0].ClaimToken, errorMsg, nextRetryAt)
	assert.NoError(t, err)

	entries, err = repo.ClaimPendingEntries(ctx, 10)
	assert.NoError(t, err)
	assert.Len(t, entries, 0)

	pastRetry := time.Now().Add(-time.Minute)
	_, err = database.Exec(ctx, `UPDATE event_outbox SET next_retry_at = ? WHERE id = ?`, pastRetry, entryID)
	require.NoError(t, err)

	entries, err = repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	err = repo.MarkAsFailed(ctx, entryID, entries[0].ClaimToken, errorMsg, pastRetry)
	assert.NoError(t, err)

	row := database.QueryRow(ctx, `SELECT status, last_error, retry_count FROM event_outbox WHERE id = ?`, entryID)
	var status string
	var retryCount int
	require.NoError(t, row.Scan(&status, &errorMsg, &retryCount))
	assert.Equal(t, string(outbox.OutboxStatusFailed), status)
	assert.Equal(t, 2, retryCount)

	entries, err = repo.ClaimPendingEntries(ctx, 10)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, outbox.OutboxStatusProcessing, entry.Status)
	assert.Equal(t, errorMsg, entry.LastError)
	assert.Equal(t, 2, entry.RetryCount)
	assert.NotNil(t, entry.LeaseUntil)
	assert.Nil(t, entry.NextRetryAt)
}

func TestSQLOutboxRepository_RenewClaim(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	beforeLease := *entries[0].LeaseUntil
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, repo.RenewClaim(ctx, entries[0].ID, entries[0].ClaimToken))

	row := database.QueryRow(ctx, `SELECT lease_until FROM event_outbox WHERE id = ?`, entries[0].ID)
	var renewedLease time.Time
	require.NoError(t, row.Scan(&renewedLease))
	assert.True(t, renewedLease.After(beforeLease))
}

func TestSQLOutboxRepository_RenewClaim_RequiresCurrentClaimToken(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	err = repo.RenewClaim(ctx, entries[0].ID, "stale-claim")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Conflict))
}

func TestSQLOutboxRepository_MarkAsPublished_RequiresClaimedProcessingEntry(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	row := database.QueryRow(ctx, `SELECT id FROM event_outbox WHERE event_id = ?`, "event-1")
	var entryID int64
	require.NoError(t, row.Scan(&entryID))

	err = repo.MarkAsPublished(ctx, entryID, "stale-claim")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Conflict))
}

func TestSQLOutboxRepository_MarkAsPublished_RejectsStaleClaimTokenAfterReclaim(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}))

	firstClaim, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, firstClaim, 1)

	_, err = database.Exec(ctx,
		`UPDATE event_outbox SET lease_until = ? WHERE id = ?`,
		time.Now().Add(-time.Minute),
		firstClaim[0].ID,
	)
	require.NoError(t, err)

	secondClaim, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, secondClaim, 1)
	require.NotEqual(t, firstClaim[0].ClaimToken, secondClaim[0].ClaimToken)

	err = repo.MarkAsPublished(ctx, firstClaim[0].ID, firstClaim[0].ClaimToken)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Conflict))

	err = repo.MarkAsPublished(ctx, secondClaim[0].ID, secondClaim[0].ClaimToken)
	require.NoError(t, err)
}

func TestSQLOutboxRepository_ClaimPendingEntriesOrdersByCreatedAtThenID(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-order-1", nil),
	}))
	require.NoError(t, repo.SaveWithEvents(ctx, 2, []eventing.Event[int64]{
		newTestEvent(2, 1, "event-order-2", nil),
	}))
	require.NoError(t, repo.SaveWithEvents(ctx, 3, []eventing.Event[int64]{
		newTestEvent(3, 1, "event-order-3", nil),
	}))

	sameCreatedAt := time.Now().Add(-time.Minute).UTC()
	_, err = database.Exec(ctx,
		`UPDATE event_outbox SET created_at = ? WHERE event_id IN (?, ?, ?)`,
		sameCreatedAt, "event-order-1", "event-order-2", "event-order-3",
	)
	require.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 3)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, "event-order-1", entries[0].EventID)
	require.Equal(t, "event-order-2", entries[1].EventID)
	require.Equal(t, "event-order-3", entries[2].EventID)
	require.Less(t, entries[0].ID, entries[1].ID)
	require.Less(t, entries[1].ID, entries[2].ID)
}

// TestSQLOutboxRepository_DeletePublished 验证 SQLOutboxRepository DeletePublished。
func TestSQLOutboxRepository_DeletePublished(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()

	events := []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	}

	err = repo.SaveWithEvents(ctx, 1, events)
	assert.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)

	err = repo.MarkAsPublished(ctx, entries[0].ID, entries[0].ClaimToken)
	assert.NoError(t, err)

	olderThan := time.Now().Add(time.Hour)
	err = repo.DeletePublished(ctx, olderThan)
	assert.NoError(t, err)

	var count int64
	row := database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count scan error: %v", err)
	}
	assert.Equal(t, int64(0), count)
}

func TestSQLOutboxRepository_DeletePublished_FailsWhenRowsAffectedUnavailable(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	err = repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-1", nil),
	})
	require.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, repo.MarkAsPublished(ctx, entries[0].ID, entries[0].ClaimToken))

	repo.db = rowsAffectedErrOutboxDB{IDatabase: database}
	err = repo.DeletePublished(ctx, time.Now().Add(time.Hour))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Database))
}
