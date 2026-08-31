package sqlstore

import (
	"context"
	"strings"
	"time"

	"gochen-runtime/db/sql/sqlbuilder"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
)

// BatchOperations 定义批量Operations。
type BatchOperations struct {
	db        db.IDatabase
	tableName string
}

const (
	batchOperationMaxParams          = 900
	publishedBatchConstantParamCount = 6
	publishedBatchParamsPerEntry     = 2
	failedBatchConstantParamCount    = 2
	failedBatchParamsPerEntry        = 6
	deleteBatchConstantParamCount    = 1
	deleteBatchParamsPerEntry        = 1
)

// NewBatchOperations 创建批量Operations。
func NewBatchOperations(db db.IDatabase) outbox.IBatchRepository {
	return NewBatchOperationsWithTable(db, "")
}

// NewBatchOperationsWithTable 创建批量Operations并带表。
func NewBatchOperationsWithTable(db db.IDatabase, tableName string) outbox.IBatchRepository {
	if tableName == "" {
		tableName = outbox.DefaultOutboxTable
	}
	return &BatchOperations{db: db, tableName: tableName}
}

func (b *BatchOperations) validate() error {
	if b == nil {
		return errors.NewCode(errors.InvalidInput, "batch operations is nil")
	}
	if b.db == nil {
		return errors.NewCode(errors.InvalidInput, "batch operations db cannot be nil")
	}
	if strings.TrimSpace(b.tableName) == "" {
		return errors.NewCode(errors.InvalidInput, "batch operations table name cannot be empty")
	}
	return nil
}

// MarkAsPublishedBatch 批量标记为已发布。
//
// 说明：
// - 使用单个 SQL 语句更新多条记录：
// - UPDATE event_outbox
// - SET status = 'published', published_at = ?
// - WHERE id IN (1, 2, 3, ...)
func (b *BatchOperations) MarkAsPublishedBatch(ctx context.Context, entries []outbox.ClaimedEntry) error {
	if err := b.validate(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	maxEntries := batchChunkSize(batchOperationMaxParams, publishedBatchConstantParamCount, publishedBatchParamsPerEntry)
	tx, err := b.db.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "begin transaction for batch mark as published failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for len(entries) > maxEntries {
		if err := b.markAsPublishedBatchChunk(ctx, tx, entries[:maxEntries]); err != nil {
			return err
		}
		entries = entries[maxEntries:]
	}
	if err := b.markAsPublishedBatchChunk(ctx, tx, entries); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, errors.Database, "commit batch mark as published failed")
	}
	committed = true
	return nil
}

func (b *BatchOperations) markAsPublishedBatchChunk(ctx context.Context, tx db.ITransaction, entries []outbox.ClaimedEntry) error {
	if len(entries) == 0 {
		return nil
	}

	now := time.Now()

	sq, err := sqlbuilder.New(tx)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	result, err := sq.Update(b.tableName).
		Set("status", outbox.OutboxStatusPublished).
		Set("published_at", now).
		Set("claim_token", "").
		Set("lease_until", nil).
		Set("next_retry_at", nil).
		Where(claimedEntriesWhere(entries), claimedEntriesArgs(entries)...).
		Where("status = ?", outbox.OutboxStatusProcessing).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "batch mark as published failed")
	}

	if err := ensureRowsAffected(result, int64(len(entries)), "batch mark as published affected unexpected rows"); err != nil {
		return err
	}

	return nil
}

// MarkAsFailedBatch 批量标记为失败。
//
// 说明：
// - 使用 CASE WHEN 语句批量更新不同的错误信息和重试时间。
func (b *BatchOperations) MarkAsFailedBatch(ctx context.Context, entries []outbox.FailedEntry) error {
	if err := b.validate(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	maxEntries := batchChunkSize(batchOperationMaxParams, failedBatchConstantParamCount, failedBatchParamsPerEntry)
	tx, err := b.db.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "begin transaction for batch mark as failed failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for len(entries) > maxEntries {
		if err := b.markAsFailedBatchChunk(ctx, tx, entries[:maxEntries]); err != nil {
			return err
		}
		entries = entries[maxEntries:]
	}
	if err := b.markAsFailedBatchChunk(ctx, tx, entries); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, errors.Database, "commit batch mark as failed failed")
	}
	committed = true
	return nil
}

func (b *BatchOperations) markAsFailedBatchChunk(ctx context.Context, tx db.ITransaction, entries []outbox.FailedEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 构造 CASE WHEN 子句
	errorCases := make([]sqlbuilder.CaseWhenEq, 0, len(entries))
	retryCases := make([]sqlbuilder.CaseWhenEq, 0, len(entries))

	for _, entry := range entries {
		errorCases = append(errorCases, sqlbuilder.CaseWhenEq{
			When: entry.ID,
			Then: entry.Error,
		})
		retryCases = append(retryCases, sqlbuilder.CaseWhenEq{
			When: entry.ID,
			Then: entry.NextRetryAt,
		})
	}

	sq, err := sqlbuilder.New(tx)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	builder := sq.Update(b.tableName).
		Set("status", outbox.OutboxStatusFailed).
		Set("claim_token", "").
		Set("lease_until", nil).
		SetIncrement("retry_count", 1).
		SetCaseWhenEq("last_error", "id", errorCases).
		SetCaseWhenEq("next_retry_at", "id", retryCases).
		Where(claimedEntriesWhereFromFailed(entries), claimedEntriesArgsFromFailed(entries)...).
		Where("status = ?", outbox.OutboxStatusProcessing)

	result, err := builder.Exec(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "batch mark as failed failed")
	}

	if err := ensureRowsAffected(result, int64(len(entries)), "batch mark as failed affected unexpected rows"); err != nil {
		return err
	}

	return nil
}

func batchChunkSize(maxParams int, constantParams int, paramsPerEntry int) int {
	if maxParams <= constantParams || paramsPerEntry <= 0 {
		return 1
	}
	size := (maxParams - constantParams) / paramsPerEntry
	if size < 1 {
		return 1
	}
	return size
}

func claimedEntriesWhere(entries []outbox.ClaimedEntry) string {
	parts := make([]string, len(entries))
	for i := range entries {
		parts[i] = "(id = ? AND claim_token = ?)"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func claimedEntriesArgs(entries []outbox.ClaimedEntry) []any {
	args := make([]any, 0, len(entries)*2)
	for _, entry := range entries {
		args = append(args, entry.ID, entry.ClaimToken)
	}
	return args
}

func claimedEntriesWhereFromFailed(entries []outbox.FailedEntry) string {
	claimed := make([]outbox.ClaimedEntry, 0, len(entries))
	for _, entry := range entries {
		claimed = append(claimed, entry.ClaimedEntry)
	}
	return claimedEntriesWhere(claimed)
}

func claimedEntriesArgsFromFailed(entries []outbox.FailedEntry) []any {
	claimed := make([]outbox.ClaimedEntry, 0, len(entries))
	for _, entry := range entries {
		claimed = append(claimed, entry.ClaimedEntry)
	}
	return claimedEntriesArgs(claimed)
}

// DeletePublishedBatch 删除对象。
//
// 说明：
// - DeletePublishedBatch 批量删除已发布记录。
func (b *BatchOperations) DeletePublishedBatch(ctx context.Context, entryIDs []int64) error {
	if err := b.validate(); err != nil {
		return err
	}
	if len(entryIDs) == 0 {
		return nil
	}

	maxEntries := batchChunkSize(batchOperationMaxParams, deleteBatchConstantParamCount, deleteBatchParamsPerEntry)
	for len(entryIDs) > maxEntries {
		if err := b.deletePublishedBatchChunk(ctx, entryIDs[:maxEntries]); err != nil {
			return err
		}
		entryIDs = entryIDs[maxEntries:]
	}
	return b.deletePublishedBatchChunk(ctx, entryIDs)
}

func (b *BatchOperations) deletePublishedBatchChunk(ctx context.Context, entryIDs []int64) error {
	if len(entryIDs) == 0 {
		return nil
	}

	args := make([]any, len(entryIDs))
	for i, id := range entryIDs {
		args[i] = id
	}

	sq, err := sqlbuilder.New(b.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	_, err = sq.DeleteFrom(b.tableName).
		Where(inIDsClause("id", len(entryIDs)), args...).
		Where("status = ?", outbox.OutboxStatusPublished).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "batch delete published failed")
	}

	return nil
}
