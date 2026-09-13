package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/db"
	"gochen/db/dialect"
	gerrors "gochen/errors"
	"gochen/eventing"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

type rowsAffectedErrRetryTx struct {
	status outbox.OutboxStatus
}

func (tx rowsAffectedErrRetryTx) Query(context.Context, string, ...any) (db.IRows, error) {
	return nil, stderrors.New("unexpected query")
}

func (tx rowsAffectedErrRetryTx) QueryRow(context.Context, string, ...any) db.IRow {
	return retryStatusRow{status: tx.status}
}

func (tx rowsAffectedErrRetryTx) Exec(context.Context, string, ...any) (sql.Result, error) {
	return rowsAffectedErrOutboxResult{}, nil
}

func (tx rowsAffectedErrRetryTx) Begin(context.Context) (db.ITransaction, error) {
	return nil, stderrors.New("unexpected begin")
}

func (tx rowsAffectedErrRetryTx) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	return nil, stderrors.New("unexpected begin tx")
}

func (tx rowsAffectedErrRetryTx) Ping(context.Context) error { return nil }

func (tx rowsAffectedErrRetryTx) Close() error { return nil }

func (tx rowsAffectedErrRetryTx) Commit() error { return nil }

func (tx rowsAffectedErrRetryTx) Rollback() error { return nil }

type retryStatusRow struct {
	status outbox.OutboxStatus
}

func (r retryStatusRow) Scan(dest ...any) error {
	if len(dest) == 0 {
		return stderrors.New("missing destination")
	}
	status, ok := dest[0].(*outbox.OutboxStatus)
	if !ok {
		return stderrors.New("unexpected destination")
	}
	*status = r.status
	return nil
}

func (r retryStatusRow) Err() error { return nil }

func TestSQLDLQRepository_MoveToDLQWithoutAutoCleanupMarksOriginalDeadLettered(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-dlq", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)

	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	entries, err = repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, entries, 0)

	var status string
	var claimToken string
	var retryCount int
	require.NoError(t, database.QueryRow(ctx,
		`SELECT status, claim_token, retry_count FROM event_outbox WHERE id = ?`,
		entry.ID,
	).Scan(&status, &claimToken, &retryCount))
	assert.Equal(t, string(outbox.OutboxStatusDeadLettered), status)
	assert.Empty(t, claimToken)
	assert.Equal(t, 3, retryCount)

	count, err := dlq.GetDLQCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSQLDLQRepository_MoveToDLQUsesConfiguredOutboxTable(t *testing.T) {
	database := setupTestDB(t)
	setupOutboxTable(t, database, "custom_event_outbox")
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	repo.outboxTable = "custom_event_outbox"
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-custom-dlq", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry := persistFailedDLQEntryInTable(t, database, "custom_event_outbox", entries[0], "publish failed", 3)

	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	var status string
	require.NoError(t, database.QueryRow(ctx,
		`SELECT status FROM custom_event_outbox WHERE id = ?`,
		entry.ID,
	).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusDeadLettered), status)

	var defaultCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&defaultCount))
	assert.Equal(t, 0, defaultCount)
}

func TestSQLDLQRepository_MoveToDLQUsesOutboxTableProvider(t *testing.T) {
	database := setupTestDB(t)
	setupOutboxTable(t, database, "wrapped_event_outbox")
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	repo.outboxTable = "wrapped_event_outbox"
	wrapped := tableProvidingOutboxRepository[int64]{IOutboxRepository: repo, table: "wrapped_event_outbox"}
	dlq, err := NewSQLDLQRepository(database, wrapped, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-wrapped-dlq", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry := persistFailedDLQEntryInTable(t, database, "wrapped_event_outbox", entries[0], "publish failed", 3)

	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	var status string
	require.NoError(t, database.QueryRow(ctx,
		`SELECT status FROM wrapped_event_outbox WHERE id = ?`,
		entry.ID,
	).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusDeadLettered), status)
}

func TestSQLDLQRepository_MoveToDLQRollsBackWhenOutboxRowMissing(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	entry := failedDLQEntry(outbox.OutboxEntry[int64]{
		ID:            999,
		AggregateID:   1,
		AggregateType: "TestAggregate",
		EventID:       "event-missing-outbox",
		EventType:     "TestEvent",
		EventData:     `{}`,
	}, "publish failed", 3)

	err = dlq.MoveToDLQ(context.Background(), entry)
	require.Error(t, err)

	count, countErr := dlq.GetDLQCount(context.Background())
	require.NoError(t, countErr)
	assert.Equal(t, int64(0), count)
}

func TestSQLDLQRepository_MoveToDLQRollsBackWhenOutboxRowStatusIsStale(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-stale-status", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := failedDLQEntry(entries[0], "publish failed", 3)
	err = dlq.MoveToDLQ(ctx, entry)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	count, countErr := dlq.GetDLQCount(ctx)
	require.NoError(t, countErr)
	assert.Equal(t, int64(0), count)

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE id = ?`, entry.ID).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusProcessing), status)
}

func TestSQLDLQRepository_MoveToDLQRejectsNonFailedEntry(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	entry := outbox.OutboxEntry[int64]{
		ID:            1,
		AggregateID:   1,
		AggregateType: "TestAggregate",
		EventID:       "event-not-failed",
		EventType:     "TestEvent",
		EventData:     `{}`,
		Status:        outbox.OutboxStatusPending,
	}

	err = dlq.MoveToDLQ(context.Background(), entry)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	count, countErr := dlq.GetDLQCount(context.Background())
	require.NoError(t, countErr)
	assert.Equal(t, int64(0), count)
}

func TestSQLDLQRepository_MoveToDLQRollsBackWhenEventIDIsStale(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-original-id", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)
	entry.EventID = "event-stale-id"
	err = dlq.MoveToDLQ(ctx, entry)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	count, countErr := dlq.GetDLQCount(ctx)
	require.NoError(t, countErr)
	assert.Equal(t, int64(0), count)
}

func TestSQLDLQRepository_MarkFailedAndMoveToDLQIsAtomic(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlqIface, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)
	dlq := dlqIface.(outbox.IAtomicDLQMover[int64])

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-atomic-dlq", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	err = dlq.MarkFailedAndMoveToDLQ(ctx, entries[0], entries[0].ClaimToken, "publish failed")
	require.NoError(t, err)

	var status, claimToken, lastError string
	var retryCount int
	require.NoError(t, database.QueryRow(ctx,
		`SELECT status, claim_token, retry_count, last_error FROM event_outbox WHERE id = ?`,
		entries[0].ID,
	).Scan(&status, &claimToken, &retryCount, &lastError))
	assert.Equal(t, string(outbox.OutboxStatusDeadLettered), status)
	assert.Empty(t, claimToken)
	assert.Equal(t, 1, retryCount)
	assert.Equal(t, "publish failed", lastError)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE original_entry_id = ?`, entries[0].ID).Scan(&dlqCount))
	assert.Equal(t, 1, dlqCount)
}

func TestSQLDLQRepository_MarkFailedAndMoveToDLQRollsBackOnClaimConflict(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlqIface, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)
	dlq := dlqIface.(outbox.IAtomicDLQMover[int64])

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-atomic-conflict", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	err = dlq.MarkFailedAndMoveToDLQ(ctx, entries[0], "wrong-claim", "publish failed")
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	count, countErr := dlqIface.GetDLQCount(ctx)
	require.NoError(t, countErr)
	assert.Equal(t, int64(0), count)

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE id = ?`, entries[0].ID).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusProcessing), status)
}

func TestSQLDLQRepository_RetryFromDLQPreservesFailureReason(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-retry-dlq", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)
	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE original_entry_id = ?`, entry.ID).Scan(&entryID))
	require.NoError(t, dlq.RetryFromDLQ(ctx, entryID))

	row := database.QueryRow(ctx, `SELECT status, retry_count, last_error FROM event_outbox WHERE event_id = ?`, entry.EventID)
	var status string
	var retryCount int
	var lastError string
	require.NoError(t, row.Scan(&status, &retryCount, &lastError))
	assert.Equal(t, string(outbox.OutboxStatusPending), status)
	assert.Equal(t, 0, retryCount)
	assert.Equal(t, "publish failed", lastError)
}

func TestSQLDLQRepository_EnsureOutboxRetryableUsesLatestEventIDRow(t *testing.T) {
	database := setupTestDB(t)
	_, err := database.Exec(context.Background(), `
		CREATE TABLE retry_order_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL,
			status TEXT NOT NULL,
			claim_token TEXT NOT NULL DEFAULT '',
			lease_until DATETIME NULL,
			next_retry_at DATETIME NULL,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NULL
		)
	`)
	require.NoError(t, err)
	_, err = database.Exec(context.Background(), `
		INSERT INTO retry_order_outbox (event_id, status) VALUES (?, ?), (?, ?)
	`, "event-retry-order", outbox.OutboxStatusFailed, "event-retry-order", outbox.OutboxStatusPending)
	require.NoError(t, err)

	tx, err := database.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()

	repo := &SQLDLQRepository[int64]{outboxTable: "retry_order_outbox", dialect: dialect.New("")}
	exists, appErr := repo.ensureOutboxRetryableByEventID(context.Background(), tx, "event-retry-order", "retry failed")
	require.Error(t, appErr)
	assert.False(t, exists)
	assert.Truef(t, gerrors.Is(appErr, gerrors.Conflict), "expected latest pending row to block retry, got %v", appErr)
}

func TestSQLDLQRepository_EnsureOutboxRetryableReturnsRowsAffectedError(t *testing.T) {
	repo := &SQLDLQRepository[int64]{outboxTable: "event_outbox", dialect: dialect.New("")}

	exists, appErr := repo.ensureOutboxRetryableByEventID(
		context.Background(),
		rowsAffectedErrRetryTx{status: outbox.OutboxStatusFailed},
		"event-rows-affected-error",
		"publish failed",
	)

	require.Error(t, appErr)
	assert.False(t, exists)
	assert.Truef(t, gerrors.Is(appErr, gerrors.Database), "expected Database error, got %v", appErr)
}

func TestSQLDLQRepository_EnsureOutboxRetryableUpdatesOnlySelectedRow(t *testing.T) {
	database := setupTestDB(t)
	_, err := database.Exec(context.Background(), `
		CREATE TABLE retry_duplicate_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL,
			status TEXT NOT NULL,
			claim_token TEXT NOT NULL DEFAULT '',
			lease_until DATETIME NULL,
			next_retry_at DATETIME NULL,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NULL
		)
	`)
	require.NoError(t, err)
	_, err = database.Exec(context.Background(), `
		INSERT INTO retry_duplicate_outbox (event_id, status, retry_count, last_error) VALUES
			(?, ?, ?, ?),
			(?, ?, ?, ?)
	`, "event-duplicate", outbox.OutboxStatusFailed, 9, "older failure",
		"event-duplicate", outbox.OutboxStatusFailed, 7, "newer failure")
	require.NoError(t, err)

	tx, err := database.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()

	repo := &SQLDLQRepository[int64]{outboxTable: "retry_duplicate_outbox", dialect: dialect.New("")}
	exists, appErr := repo.ensureOutboxRetryableByEventID(context.Background(), tx, "event-duplicate", "retry failed")
	require.Nil(t, appErr)
	require.True(t, exists)
	require.NoError(t, tx.Commit())

	rows, err := database.Query(context.Background(), `SELECT id, status, retry_count, last_error FROM retry_duplicate_outbox ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()

	type rowState struct {
		id         int64
		status     string
		retryCount int
		lastError  string
	}
	var got []rowState
	for rows.Next() {
		var state rowState
		require.NoError(t, rows.Scan(&state.id, &state.status, &state.retryCount, &state.lastError))
		got = append(got, state)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2)
	require.Equal(t, string(outbox.OutboxStatusFailed), got[0].status)
	require.Equal(t, 9, got[0].retryCount)
	require.Equal(t, "older failure", got[0].lastError)
	require.Equal(t, string(outbox.OutboxStatusPending), got[1].status)
	require.Equal(t, 0, got[1].retryCount)
	require.Equal(t, "retry failed", got[1].lastError)
}

func TestSQLDLQRepository_RetryFromDLQKeepsDLQWhenOutboxEntryPending(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	event := newTestEvent(1, 1, "event-retry-duplicate", nil)
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{event}))

	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox_dlq (
			original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
			event_data, failure_reason, retry_count, moved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, 999, 1, event.GetAggregateType(), event.GetID(), event.GetType(), mustMarshalEventData(t, event), "publish failed", 3, time.Now())
	require.NoError(t, err)

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&entryID))
	err = dlq.RetryFromDLQ(ctx, entryID)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	var outboxCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, event.GetID()).Scan(&outboxCount))
	assert.Equal(t, 1, outboxCount)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&dlqCount))
	assert.Equal(t, 1, dlqCount)
}

func TestSQLDLQRepository_RetryFromDLQKeepsDLQWhenOutboxEntryProcessing(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	event := newTestEvent(1, 1, "event-retry-processing", nil)
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{event}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox_dlq (
			original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
			event_data, failure_reason, retry_count, moved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, 999, 1, event.GetAggregateType(), event.GetID(), event.GetType(), mustMarshalEventData(t, event), "publish failed", 3, time.Now())
	require.NoError(t, err)

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&entryID))
	err = dlq.RetryFromDLQ(ctx, entryID)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Conflict))

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE id = ?`, entries[0].ID).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusProcessing), status)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&dlqCount))
	assert.Equal(t, 1, dlqCount)
}

func TestSQLDLQRepository_RetryFromDLQResetsDeadLetteredOutboxEntry(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-retry-dead-lettered", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)
	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE original_entry_id = ?`, entry.ID).Scan(&entryID))
	require.NoError(t, dlq.RetryFromDLQ(ctx, entryID))

	var status, claimToken string
	var retryCount int
	var lastError string
	require.NoError(t, database.QueryRow(ctx,
		`SELECT status, claim_token, retry_count, last_error FROM event_outbox WHERE event_id = ?`,
		entry.EventID,
	).Scan(&status, &claimToken, &retryCount, &lastError))
	assert.Equal(t, string(outbox.OutboxStatusPending), status)
	assert.Empty(t, claimToken)
	assert.Equal(t, 0, retryCount)
	assert.Equal(t, "publish failed", lastError)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE id = ?`, entryID).Scan(&dlqCount))
	assert.Equal(t, 0, dlqCount)
}

func TestSQLDLQRepository_RetryFromDLQDuplicateFallbackRollsBackSavepoint(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	wrappedDB := &dlqDuplicateRetryDB{IDatabase: database}
	dlq, err := NewSQLDLQRepository(wrappedDB, repo, 3, false)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-retry-dead-lettered-aborted", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)
	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE original_entry_id = ?`, entry.ID).Scan(&entryID))
	require.NoError(t, dlq.RetryFromDLQ(ctx, entryID))

	require.NotNil(t, wrappedDB.lastTx)
	require.Equal(t, 1, wrappedDB.lastTx.insertDuplicates)
	require.Greater(t, wrappedDB.lastTx.rollbackTo, 0)

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE event_id = ?`, entry.EventID).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusPending), status)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE id = ?`, entryID).Scan(&dlqCount))
	assert.Equal(t, 0, dlqCount)
}

func TestSQLDLQRepository_RetryFromDLQDropsWhenOutboxEntryPublished(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	event := newTestEvent(1, 1, "event-retry-published", nil)
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{event}))
	_, err = database.Exec(ctx, `UPDATE event_outbox SET status = ? WHERE event_id = ?`, outbox.OutboxStatusPublished, event.GetID())
	require.NoError(t, err)

	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox_dlq (
			original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
			event_data, failure_reason, retry_count, moved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, 999, 1, event.GetAggregateType(), event.GetID(), event.GetType(), mustMarshalEventData(t, event), "publish failed", 3, time.Now())
	require.NoError(t, err)

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&entryID))
	require.NoError(t, dlq.RetryFromDLQ(ctx, entryID))

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE event_id = ?`, event.GetID()).Scan(&status))
	assert.Equal(t, string(outbox.OutboxStatusPublished), status)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE id = ?`, entryID).Scan(&dlqCount))
	assert.Equal(t, 0, dlqCount)
}

func TestSQLDLQRepository_RetryFromDLQKeepsDLQWhenOutboxStatusUnknown(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(database, repo, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	event := newTestEvent(1, 1, "event-retry-unknown-status", nil)
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{event}))
	_, err = database.Exec(ctx, `UPDATE event_outbox SET status = ? WHERE event_id = ?`, "archived", event.GetID())
	require.NoError(t, err)

	_, err = database.Exec(ctx, `
		INSERT INTO event_outbox_dlq (
			original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
			event_data, failure_reason, retry_count, moved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, 999, 1, event.GetAggregateType(), event.GetID(), event.GetType(), mustMarshalEventData(t, event), "publish failed", 3, time.Now())
	require.NoError(t, err)

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM event_outbox_dlq WHERE event_id = ?`, event.GetID()).Scan(&entryID))
	require.Error(t, dlq.RetryFromDLQ(ctx, entryID))

	var status string
	require.NoError(t, database.QueryRow(ctx, `SELECT status FROM event_outbox WHERE event_id = ?`, event.GetID()).Scan(&status))
	assert.Equal(t, "archived", status)

	var dlqCount int
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(*) FROM event_outbox_dlq WHERE id = ?`, entryID).Scan(&dlqCount))
	assert.Equal(t, 1, dlqCount)
}

func TestSQLDLQRepository_UsesConfiguredDLQTable(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "custom_event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	wrapped := tableProvidingOutboxRepository[int64]{
		IOutboxRepository: repo,
		table:             "event_outbox",
		dlqTable:          "custom_event_outbox_dlq",
	}
	dlq, err := NewSQLDLQRepository(database, wrapped, 3, true)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-custom-dlq-table", nil),
	}))
	entries, err := repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := persistFailedDLQEntry(t, database, entries[0], "publish failed", 3)
	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	count, err := dlq.GetDLQCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	dlqEntries, err := dlq.GetDLQEntries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, dlqEntries, 1)
	assert.Equal(t, entry.EventID, dlqEntries[0].EventID)

	var entryID int64
	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM custom_event_outbox_dlq WHERE original_entry_id = ?`, entry.ID).Scan(&entryID))
	require.NoError(t, dlq.DeleteDLQEntry(ctx, entryID))

	count, err = dlq.GetDLQCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	require.NoError(t, repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 2, "event-custom-dlq-retry", nil),
	}))
	entries, err = repo.ClaimPendingEntries(ctx, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry = persistFailedDLQEntry(t, database, entries[0], "publish failed retry", 3)
	require.NoError(t, dlq.MoveToDLQ(ctx, entry))

	require.NoError(t, database.QueryRow(ctx, `SELECT id FROM custom_event_outbox_dlq WHERE original_entry_id = ?`, entry.ID).Scan(&entryID))
	require.NoError(t, dlq.RetryFromDLQ(ctx, entryID))

	count, err = dlq.GetDLQCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestSQLDLQRepository_DeleteDLQEntryRowsAffectedUnavailable(t *testing.T) {
	database := setupTestDB(t)
	setupDLQTable(t, database, "event_outbox_dlq")
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)
	dlq, err := NewSQLDLQRepository(unsupportedRowsAffectedOutboxDB{IDatabase: database}, repo, 3, false)
	require.NoError(t, err)

	err = dlq.DeleteDLQEntry(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, gerrors.Is(err, gerrors.Unsupported))
}

type unsupportedRowsAffectedOutboxDB struct {
	db.IDatabase
}

func (d unsupportedRowsAffectedOutboxDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	_, err := d.IDatabase.Exec(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return unsupportedRowsAffectedResult{}, nil
}

type unsupportedRowsAffectedResult struct{}

func (unsupportedRowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }
func (unsupportedRowsAffectedResult) RowsAffected() (int64, error) {
	return 0, driver.ErrSkip
}

func TestSQLDLQRepository_UsesValidQualifiedProviderTableNames(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	wrapped := tableProvidingOutboxRepository[int64]{
		IOutboxRepository: repo,
		table:             "tenant_01.event_outbox",
		dlqTable:          "tenant_01.event_outbox_dlq",
	}

	dlq, err := NewSQLDLQRepository(database, wrapped, 3, true)
	require.NoError(t, err)

	sqlDLQ := dlq.(*SQLDLQRepository[int64])
	assert.Equal(t, "tenant_01.event_outbox", sqlDLQ.outboxTable)
	assert.Equal(t, "tenant_01.event_outbox_dlq", sqlDLQ.dlqTable)
	assert.Equal(t, `"tenant_01"."event_outbox"`, sqlDLQ.quotedOutboxTable())
	assert.Equal(t, `"tenant_01"."event_outbox_dlq"`, sqlDLQ.quotedDLQTable())
}

func TestSQLDLQRepository_RejectsInvalidProviderOutboxTable(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	wrapped := tableProvidingOutboxRepository[int64]{
		IOutboxRepository: repo,
		table:             "event_outbox; DROP TABLE event_outbox_dlq",
		dlqTable:          "event_outbox_dlq",
	}

	dlq, err := NewSQLDLQRepository(database, wrapped, 3, true)
	require.Error(t, err)
	assert.Nil(t, dlq)
	assert.True(t, gerrors.Is(err, gerrors.InvalidInput))
	assert.Contains(t, err.Error(), "invalid outbox table name")
}

func TestSQLDLQRepository_RejectsInvalidProviderDLQTable(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	wrapped := tableProvidingOutboxRepository[int64]{
		IOutboxRepository: repo,
		table:             "event_outbox",
		dlqTable:          "event_outbox_dlq; DROP TABLE event_outbox",
	}

	dlq, err := NewSQLDLQRepository(database, wrapped, 3, true)
	require.Error(t, err)
	assert.Nil(t, dlq)
	assert.True(t, gerrors.Is(err, gerrors.InvalidInput))
	assert.Contains(t, err.Error(), "invalid dlq table name")
}

type tableProvidingOutboxRepository[ID comparable] struct {
	outbox.IOutboxRepository[ID]
	table    string
	dlqTable string
}

func (r tableProvidingOutboxRepository[ID]) OutboxTableName() string {
	return r.table
}

func (r tableProvidingOutboxRepository[ID]) DLQTableName() string {
	return r.dlqTable
}

func failedDLQEntry(entry outbox.OutboxEntry[int64], reason string, retryCount int) outbox.OutboxEntry[int64] {
	entry.Status = outbox.OutboxStatusFailed
	entry.ClaimToken = ""
	entry.LeaseUntil = nil
	entry.LastError = reason
	entry.RetryCount = retryCount
	return entry
}

func persistFailedDLQEntry(tb testing.TB, database db.IDatabase, entry outbox.OutboxEntry[int64], reason string, retryCount int) outbox.OutboxEntry[int64] {
	tb.Helper()
	return persistFailedDLQEntryInTable(tb, database, "event_outbox", entry, reason, retryCount)
}

func persistFailedDLQEntryInTable(tb testing.TB, database db.IDatabase, tableName string, entry outbox.OutboxEntry[int64], reason string, retryCount int) outbox.OutboxEntry[int64] {
	tb.Helper()
	entry = failedDLQEntry(entry, reason, retryCount)
	_, err := database.Exec(context.Background(), fmt.Sprintf(`
		UPDATE %s
		SET status = ?, claim_token = '', lease_until = NULL, next_retry_at = NULL, last_error = ?, retry_count = ?
		WHERE id = ?
	`, tableName), outbox.OutboxStatusFailed, reason, retryCount, entry.ID)
	require.NoError(tb, err)
	return entry
}

func mustMarshalEventData(tb testing.TB, event eventing.Event[int64]) string {
	tb.Helper()
	entry, err := outbox.EventToOutboxEntry(event.GetAggregateID(), event)
	require.NoError(tb, err)
	return entry.EventData
}

type dlqDuplicateRetryDB struct {
	db.IDatabase
	lastTx *dlqDuplicateRetryTx
}

func (d *dlqDuplicateRetryDB) Begin(ctx context.Context) (db.ITransaction, error) {
	return d.BeginTx(ctx, nil)
}

func (d *dlqDuplicateRetryDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	tx, err := d.IDatabase.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	wrapped := &dlqDuplicateRetryTx{ITransaction: tx}
	d.lastTx = wrapped
	return wrapped, nil
}

type dlqDuplicateRetryTx struct {
	db.ITransaction
	aborted          bool
	insertDuplicates int
	rollbackTo       int
}

func (t *dlqDuplicateRetryTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if t.aborted {
		return nil, stderrors.New("current transaction is aborted")
	}
	normalized := strings.ToLower(query)
	normalized = strings.NewReplacer(`"`, "", "`", "").Replace(normalized)
	if strings.Contains(normalized, "insert into event_outbox") && !strings.Contains(normalized, "event_outbox_dlq") {
		t.aborted = true
		t.insertDuplicates++
		return nil, stderrors.New("SQLSTATE 23505: duplicate key")
	}
	return t.ITransaction.Exec(ctx, query, args...)
}

func (t *dlqDuplicateRetryTx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if t.aborted {
		return errorRow{err: stderrors.New("current transaction is aborted")}
	}
	return t.ITransaction.QueryRow(ctx, query, args...)
}

func (t *dlqDuplicateRetryTx) CreateSavepoint(ctx context.Context, name string) error {
	return nil
}

func (t *dlqDuplicateRetryTx) RollbackToSavepoint(ctx context.Context, name string) error {
	t.aborted = false
	t.rollbackTo++
	return nil
}

func (t *dlqDuplicateRetryTx) ReleaseSavepoint(ctx context.Context, name string) error {
	return nil
}
