package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/errors"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

func TestBatchOperations_MarkAsFailedBatch_RollsBackOnClaimConflict(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	leaseUntil := now.Add(time.Minute)

	_, err := database.Exec(ctx, `
		INSERT INTO event_outbox (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, retry_count, lease_until
		) VALUES
			(1, 1, 'TestAggregate', 'event-1', 'TestEvent', '{}', ?, 'owned-1', ?, 2, ?),
			(2, 2, 'TestAggregate', 'event-2', 'TestEvent', '{}', ?, 'owned-2', ?, 2, ?)
	`, outbox.OutboxStatusProcessing, now, leaseUntil, outbox.OutboxStatusProcessing, now, leaseUntil)
	require.NoError(t, err)

	batchOps := NewBatchOperations(database)
	err = batchOps.MarkAsFailedBatch(ctx, []outbox.FailedEntry{
		{
			ClaimedEntry: outbox.ClaimedEntry{ID: 1, ClaimToken: "owned-1"},
			Error:        "current failure",
			NextRetryAt:  now.Add(time.Hour),
		},
		{
			ClaimedEntry: outbox.ClaimedEntry{ID: 2, ClaimToken: "stale-token"},
			Error:        "stale failure",
			NextRetryAt:  now.Add(time.Hour),
		},
	})
	require.Error(t, err)

	var status, claimToken string
	var retryCount int
	var lastError sql.NullString
	require.NoError(t, database.QueryRow(ctx, `
		SELECT status, claim_token, retry_count, last_error
		FROM event_outbox
		WHERE id = 1
	`).Scan(&status, &claimToken, &retryCount, &lastError))
	assert.Equal(t, string(outbox.OutboxStatusProcessing), status)
	assert.Equal(t, "owned-1", claimToken)
	assert.Equal(t, 2, retryCount)
	assert.False(t, lastError.Valid)

	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)
	require.NoError(t, repo.MarkAsFailed(ctx, 1, "owned-1", "fallback failure", now.Add(time.Hour)))

	require.NoError(t, database.QueryRow(ctx, `
		SELECT status, claim_token, retry_count, last_error
		FROM event_outbox
		WHERE id = 1
	`).Scan(&status, &claimToken, &retryCount, &lastError))
	assert.Equal(t, string(outbox.OutboxStatusFailed), status)
	assert.Empty(t, claimToken)
	assert.Equal(t, 3, retryCount)
	if assert.True(t, lastError.Valid) {
		assert.Equal(t, "fallback failure", lastError.String)
	}
}

func TestBatchOperations_MarkAsFailedBatchChunksRollbackTogether(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	leaseUntil := now.Add(time.Minute)

	chunkSize := batchChunkSize(batchOperationMaxParams, failedBatchConstantParamCount, failedBatchParamsPerEntry)
	entryCount := chunkSize + 1
	entries := make([]outbox.FailedEntry, 0, entryCount)
	for i := 1; i <= entryCount; i++ {
		token := fmt.Sprintf("claim-%d", i)
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, claim_token, created_at, retry_count, lease_until
			) VALUES (?, ?, 'TestAggregate', ?, 'TestEvent', '{}', ?, ?, ?, 0, ?)
		`, i, i, fmt.Sprintf("chunk-event-%d", i), outbox.OutboxStatusProcessing, token, now, leaseUntil)
		require.NoError(t, err)
		if i == entryCount {
			token = "stale-claim"
		}
		entries = append(entries, outbox.FailedEntry{
			ClaimedEntry: outbox.ClaimedEntry{ID: int64(i), ClaimToken: token},
			Error:        "failure",
			NextRetryAt:  now.Add(time.Hour),
		})
	}

	require.Error(t, NewBatchOperations(database).MarkAsFailedBatch(ctx, entries))

	var failed int
	require.NoError(t, database.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE status = ?
	`, outbox.OutboxStatusFailed).Scan(&failed))
	require.Equal(t, 0, failed)
}

func TestBatchOperations_NilDBReturnsInvalidInput(t *testing.T) {
	batchOps := NewBatchOperations(nil)
	err := batchOps.MarkAsFailedBatch(context.Background(), []outbox.FailedEntry{{
		ClaimedEntry: outbox.ClaimedEntry{ID: 1, ClaimToken: "token"},
		Error:        "failed",
		NextRetryAt:  time.Now(),
	}})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestBatchOperations_MarkAsPublishedBatchChunksByParameterBudget(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	leaseUntil := now.Add(time.Minute)

	entryCount := batchChunkSize(batchOperationMaxParams, publishedBatchConstantParamCount, publishedBatchParamsPerEntry) + 2
	entries := make([]outbox.ClaimedEntry, 0, entryCount)
	for i := 1; i <= entryCount; i++ {
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, claim_token, created_at, retry_count, lease_until
			) VALUES (?, ?, 'TestAggregate', ?, 'TestEvent', '{}', ?, ?, ?, 0, ?)
		`, i, i, fmt.Sprintf("event-%d", i), outbox.OutboxStatusProcessing, fmt.Sprintf("claim-%d", i), now, leaseUntil)
		require.NoError(t, err)
		entries = append(entries, outbox.ClaimedEntry{ID: int64(i), ClaimToken: fmt.Sprintf("claim-%d", i)})
	}

	require.NoError(t, NewBatchOperations(database).MarkAsPublishedBatch(ctx, entries))

	var published int
	require.NoError(t, database.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE status = ?
	`, outbox.OutboxStatusPublished).Scan(&published))
	require.Equal(t, entryCount, published)
}

func TestBatchOperations_MarkAsPublishedBatchChunksRollbackTogether(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	leaseUntil := now.Add(time.Minute)

	chunkSize := batchChunkSize(batchOperationMaxParams, publishedBatchConstantParamCount, publishedBatchParamsPerEntry)
	entryCount := chunkSize + 1
	entries := make([]outbox.ClaimedEntry, 0, entryCount)
	for i := 1; i <= entryCount; i++ {
		token := fmt.Sprintf("claim-%d", i)
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, claim_token, created_at, retry_count, lease_until
			) VALUES (?, ?, 'TestAggregate', ?, 'TestEvent', '{}', ?, ?, ?, 0, ?)
		`, i, i, fmt.Sprintf("publish-rollback-event-%d", i), outbox.OutboxStatusProcessing, token, now, leaseUntil)
		require.NoError(t, err)
		if i == entryCount {
			token = "stale-claim"
		}
		entries = append(entries, outbox.ClaimedEntry{ID: int64(i), ClaimToken: token})
	}

	require.Error(t, NewBatchOperations(database).MarkAsPublishedBatch(ctx, entries))

	var published int
	require.NoError(t, database.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE status = ?
	`, outbox.OutboxStatusPublished).Scan(&published))
	require.Equal(t, 0, published)
}

func TestBatchOperations_MarkAsFailedBatchChunksByParameterBudget(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	leaseUntil := now.Add(time.Minute)

	entryCount := batchChunkSize(batchOperationMaxParams, failedBatchConstantParamCount, failedBatchParamsPerEntry) + 2
	entries := make([]outbox.FailedEntry, 0, entryCount)
	for i := 1; i <= entryCount; i++ {
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, claim_token, created_at, retry_count, lease_until
			) VALUES (?, ?, 'TestAggregate', ?, 'TestEvent', '{}', ?, ?, ?, 0, ?)
		`, i, i, fmt.Sprintf("event-%d", i), outbox.OutboxStatusProcessing, fmt.Sprintf("claim-%d", i), now, leaseUntil)
		require.NoError(t, err)
		entries = append(entries, outbox.FailedEntry{
			ClaimedEntry: outbox.ClaimedEntry{ID: int64(i), ClaimToken: fmt.Sprintf("claim-%d", i)},
			Error:        strings.Repeat("failure", 4),
			NextRetryAt:  now.Add(time.Hour),
		})
	}

	require.NoError(t, NewBatchOperations(database).MarkAsFailedBatch(ctx, entries))

	var failed int
	require.NoError(t, database.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE status = ?
	`, outbox.OutboxStatusFailed).Scan(&failed))
	require.Equal(t, entryCount, failed)
}

func TestBatchOperations_DeletePublishedBatchChunksByParameterBudget(t *testing.T) {
	database := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()

	entryCount := batchChunkSize(batchOperationMaxParams, deleteBatchConstantParamCount, deleteBatchParamsPerEntry) + 2
	ids := make([]int64, 0, entryCount)
	for i := 1; i <= entryCount; i++ {
		_, err := database.Exec(ctx, `
			INSERT INTO event_outbox (
				id, aggregate_id, aggregate_type, event_id, event_type, event_data,
				status, created_at, retry_count
			) VALUES (?, ?, 'TestAggregate', ?, 'TestEvent', '{}', ?, ?, 0)
		`, i, i, fmt.Sprintf("published-event-%d", i), outbox.OutboxStatusPublished, now)
		require.NoError(t, err)
		ids = append(ids, int64(i))
	}

	require.NoError(t, NewBatchOperations(database).DeletePublishedBatch(ctx, ids))

	var remaining int
	require.NoError(t, database.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox WHERE status = ?
	`, outbox.OutboxStatusPublished).Scan(&remaining))
	require.Equal(t, 0, remaining)
}
