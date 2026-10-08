package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	coredialect "gochen/db/dialect"
	"strings"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/internal/sqlresult"
	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

// SQLDLQRepository DLQ 的 SQL 实现。
type SQLDLQRepository[ID comparable] struct {
	db          db.IDatabase
	dialect     coredialect.IDialect
	outboxRepo  outbox.IOutboxRepository[ID]
	outboxTable string
	dlqTable    string
	maxRetries  int
	autoCleanup bool
	codec       codec.ICodec[ID, any]
	logger      logging.ILogger
}

// NewSQLDLQRepository 为 `int64` 聚合 ID 创建一个 SQL DLQ 仓储实现。
func NewSQLDLQRepository(
	db db.IDatabase,
	outboxRepo outbox.IOutboxRepository[int64],
	maxRetries int,
	autoCleanup bool,
) (outbox.IDLQRepository[int64], error) {
	return NewSQLDLQRepositoryWithCodec[int64](db, outboxRepo, idcodec.NewInt64[int64](), maxRetries, autoCleanup)
}

// NewSQLDLQRepositoryWithCodec 创建可自定义聚合 ID 编解码方式的 SQL DLQ 仓储。
func NewSQLDLQRepositoryWithCodec[ID comparable](
	db db.IDatabase,
	outboxRepo outbox.IOutboxRepository[ID],
	idCodec codec.ICodec[ID, any],
	maxRetries int,
	autoCleanup bool,
) (outbox.IDLQRepository[ID], error) {
	if maxRetries <= 0 {
		maxRetries = 5 // 默认 5 次
	}
	if db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db cannot be nil")
	}
	if outboxRepo == nil {
		return nil, errors.NewCode(errors.InvalidInput, "outboxRepo cannot be nil")
	}
	if idCodec == nil {
		return nil, errors.NewCode(errors.InvalidInput, "codec cannot be nil")
	}
	outboxTable := dlqOutboxTable(outboxRepo)
	if err := validateTableName(outboxTable); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid outbox table name").
			WithContext("table", outboxTable)
	}
	dlqTable := dlqTableName(outboxRepo)
	if err := validateTableName(dlqTable); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid dlq table name").
			WithContext("table", dlqTable)
	}
	// r.outboxTable/r.dlqTable are validated by validateTableName before any fmt.Sprintf SQL assembly below.
	return &SQLDLQRepository[ID]{
		db:          db,
		dialect:     dialect.FromDatabase(db),
		outboxRepo:  outboxRepo,
		outboxTable: outboxTable,
		dlqTable:    dlqTable,
		maxRetries:  maxRetries,
		autoCleanup: autoCleanup,
		codec:       idCodec,
		logger:      logging.ComponentLogger("eventing.outbox.dlq"),
	}, nil
}

func dlqOutboxTable[ID comparable](outboxRepo outbox.IOutboxRepository[ID]) string {
	if provider, ok := any(outboxRepo).(IOutboxTableProvider); ok {
		if table := strings.TrimSpace(provider.OutboxTableName()); table != "" {
			return table
		}
	}
	return DefaultOutboxTable
}

func dlqTableName[ID comparable](outboxRepo outbox.IOutboxRepository[ID]) string {
	if provider, ok := any(outboxRepo).(IDLQTableProvider); ok {
		if table := strings.TrimSpace(provider.DLQTableName()); table != "" {
			return table
		}
	}
	return DefaultOutboxDLQTable
}

func (r *SQLDLQRepository[ID]) quotedOutboxTable() string {
	return r.dialect.QuoteIdentifier(r.outboxTable)
}

func (r *SQLDLQRepository[ID]) quotedDLQTable() string {
	return r.dialect.QuoteIdentifier(r.dlqTable)
}

// MoveToDLQ 在事务里把 Outbox 记录插入 DLQ，并按配置决定是否删除原记录。
func (r *SQLDLQRepository[ID]) MoveToDLQ(ctx context.Context, entry outbox.OutboxEntry[ID]) error {
	if entry.Status != outbox.OutboxStatusFailed {
		return errors.NewCode(errors.Conflict, "only failed outbox entries can be moved to DLQ").
			WithContext("outbox_entry_id", entry.ID)
	}
	dlqEntry := buildDLQEntry(entry, entry.LastError, entry.RetryCount)

	return r.withDLQTransaction(ctx,
		"begin transaction for move to dlq failed",
		"commit move to dlq transaction failed",
		func(tx db.ITransaction) error {
			if err := r.insertDLQEntry(ctx, tx, dlqEntry); err != nil {
				return err
			}
			return r.cleanupMovedOutboxEntry(ctx, tx, entry)
		},
	)
}

// MarkFailedAndMoveToDLQ 在同一事务中把当前 claim 的记录标记失败并迁入 DLQ。
func (r *SQLDLQRepository[ID]) MarkFailedAndMoveToDLQ(ctx context.Context, entry outbox.OutboxEntry[ID], claimToken string, errorMsg string) error {
	entry.Status = outbox.OutboxStatusFailed
	entry.ClaimToken = ""
	entry.LeaseUntil = nil
	entry.LastError = errorMsg
	entry.RetryCount++

	dlqEntry := buildDLQEntry(entry, errorMsg, entry.RetryCount)

	return r.withDLQTransaction(ctx,
		"begin transaction for mark failed and move to dlq failed",
		"commit mark failed and move to dlq transaction failed",
		func(tx db.ITransaction) error {
			if err := r.markClaimedOutboxForDLQ(ctx, tx, entry, claimToken, errorMsg); err != nil {
				return err
			}
			return r.insertDLQEntry(ctx, tx, dlqEntry)
		},
	)
}

func (r *SQLDLQRepository[ID]) withDLQTransaction(ctx context.Context, beginMessage, commitMessage string, fn func(db.ITransaction) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, errors.Database, beginMessage)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, errors.Database, commitMessage)
	}
	committed = true
	return nil
}

func (r *SQLDLQRepository[ID]) cleanupMovedOutboxEntry(ctx context.Context, tx db.ITransaction, entry outbox.OutboxEntry[ID]) error {
	if r.autoCleanup {
		deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE id = ? AND event_id = ? AND status = ?`, r.quotedOutboxTable())
		result, err := tx.Exec(ctx, deleteQuery, entry.ID, entry.EventID, outbox.OutboxStatusFailed)
		if err != nil {
			return errors.Wrap(err, errors.Database, "delete outbox entry failed").
				WithContext("outbox_entry_id", entry.ID)
		}
		if err := sqlresult.EnsureRowsAffected(result, 1, "delete outbox entry affected unexpected rows"); err != nil {
			return err.WithContext("outbox_entry_id", entry.ID)
		}
		return nil
	}
	markQuery := fmt.Sprintf(`
		UPDATE %s
		SET status = ?, claim_token = '', lease_until = NULL, next_retry_at = NULL, last_error = ?, retry_count = ?
		WHERE id = ? AND event_id = ? AND status = ?
	`, r.quotedOutboxTable())
	result, err := tx.Exec(ctx, markQuery, outbox.OutboxStatusDeadLettered, entry.LastError, entry.RetryCount, entry.ID, entry.EventID, outbox.OutboxStatusFailed)
	if err != nil {
		return errors.Wrap(err, errors.Database, "mark outbox entry dead-lettered failed").
			WithContext("outbox_entry_id", entry.ID)
	}
	if err := sqlresult.EnsureRowsAffected(result, 1, "mark outbox entry dead-lettered affected unexpected rows"); err != nil {
		return err.WithContext("outbox_entry_id", entry.ID)
	}
	return nil
}

func (r *SQLDLQRepository[ID]) markClaimedOutboxForDLQ(ctx context.Context, tx db.ITransaction, entry outbox.OutboxEntry[ID], claimToken string, errorMsg string) error {
	var result sql.Result
	var err error
	if r.autoCleanup {
		deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE id = ? AND event_id = ? AND claim_token = ? AND status = ?`, r.quotedOutboxTable())
		result, err = tx.Exec(ctx, deleteQuery, entry.ID, entry.EventID, claimToken, outbox.OutboxStatusProcessing)
		if err != nil {
			return errors.Wrap(err, errors.Database, "delete claimed outbox entry failed").
				WithContext("outbox_entry_id", entry.ID)
		}
	} else {
		markQuery := fmt.Sprintf(`
			UPDATE %s
			SET status = ?, claim_token = '', lease_until = NULL, next_retry_at = NULL, last_error = ?, retry_count = ?
			WHERE id = ? AND event_id = ? AND claim_token = ? AND status = ?
		`, r.quotedOutboxTable())
		result, err = tx.Exec(ctx, markQuery,
			outbox.OutboxStatusDeadLettered,
			errorMsg,
			entry.RetryCount,
			entry.ID,
			entry.EventID,
			claimToken,
			outbox.OutboxStatusProcessing,
		)
		if err != nil {
			return errors.Wrap(err, errors.Database, "mark claimed outbox entry dead-lettered failed").
				WithContext("outbox_entry_id", entry.ID)
		}
	}
	if err := sqlresult.EnsureRowsAffected(result, 1, "outbox entry claim is no longer owned"); err != nil {
		return err.WithContext("outbox_entry_id", entry.ID)
	}
	return nil
}

func buildDLQEntry[ID comparable](entry outbox.OutboxEntry[ID], failureReason string, retryCount int) outbox.DLQEntry[ID] {
	return outbox.DLQEntry[ID]{
		OriginalEntryID: entry.ID,
		AggregateID:     entry.AggregateID,
		AggregateType:   entry.AggregateType,
		EventID:         entry.EventID,
		EventType:       entry.EventType,
		EventData:       entry.EventData,
		FailureReason:   failureReason,
		RetryCount:      retryCount,
		MovedAt:         time.Now(),
	}
}

func (r *SQLDLQRepository[ID]) insertDLQEntry(ctx context.Context, tx db.ITransaction, entry outbox.DLQEntry[ID]) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (
			original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
			event_data, failure_reason, retry_count, moved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.quotedDLQTable())

	agg, err := r.codec.Encode(entry.AggregateID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}

	if _, err := tx.Exec(ctx, query,
		entry.OriginalEntryID,
		agg,
		entry.AggregateType,
		entry.EventID,
		entry.EventType,
		entry.EventData,
		entry.FailureReason,
		entry.RetryCount,
		entry.MovedAt,
	); err != nil {
		return errors.Wrap(err, errors.Database, "insert dlq entry failed")
	}
	return nil
}

// GetDLQEntries 按迁入时间倒序读取一批 DLQ 记录。
func (r *SQLDLQRepository[ID]) GetDLQEntries(ctx context.Context, limit int) ([]outbox.DLQEntry[ID], error) {
	if limit <= 0 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT id, original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
		       event_data, failure_reason, retry_count, moved_at
		FROM %s
		ORDER BY moved_at DESC
		LIMIT ?
	`, r.quotedDLQTable())

	rows, err := r.db.Query(ctx, query, limit)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "query dlq entries failed")
	}
	defer rows.Close()

	var entries []outbox.DLQEntry[ID]
	for rows.Next() {
		var entry outbox.DLQEntry[ID]
		var rawAggID any
		err := rows.Scan(
			&entry.ID,
			&entry.OriginalEntryID,
			&rawAggID,
			&entry.AggregateType,
			&entry.EventID,
			&entry.EventType,
			&entry.EventData,
			&entry.FailureReason,
			&entry.RetryCount,
			&entry.MovedAt,
		)
		if err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan dlq entry failed")
		}
		typedAggID, err := r.codec.Decode(rawAggID)
		if err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
				WithContext("dlq_entry_id", entry.ID)
		}
		entry.AggregateID = typedAggID
		entries = append(entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate dlq entries failed")
	}

	return entries, nil
}

// RetryFromDLQ 把指定 DLQ 记录重新插入 Outbox，并在成功后删除 DLQ 记录。
//
// 若 Outbox 已存在同 event_id 记录，则按幂等重试处理；处于 failed/dead_lettered 的原记录会恢复为 pending。
func (r *SQLDLQRepository[ID]) RetryFromDLQ(ctx context.Context, entryID int64) error {
	// 使用事务保证 Outbox 插入与 DLQ 删除的原子性
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, errors.Database, "begin transaction for retry from dlq failed")
	}
	defer func() { _ = tx.Rollback() }()

	// 1. 查询 DLQ 记录
	query := fmt.Sprintf(`
		SELECT id, original_entry_id, aggregate_id, aggregate_type, event_id, event_type,
		       event_data, failure_reason, retry_count, moved_at
		FROM %s
		WHERE id = ?
	`, r.quotedDLQTable())

	var dlqEntry outbox.DLQEntry[ID]
	var rawAggID any
	err = tx.QueryRow(ctx, query, entryID).Scan(
		&dlqEntry.ID,
		&dlqEntry.OriginalEntryID,
		&rawAggID,
		&dlqEntry.AggregateType,
		&dlqEntry.EventID,
		&dlqEntry.EventType,
		&dlqEntry.EventData,
		&dlqEntry.FailureReason,
		&dlqEntry.RetryCount,
		&dlqEntry.MovedAt,
	)
	if err == sql.ErrNoRows {
		return errors.NewCode(errors.NotFound, "dlq entry not found").WithContext("dlq_entry_id", entryID)
	}
	if err != nil {
		return errors.Wrap(err, errors.Database, "query dlq entry failed").
			WithContext("dlq_entry_id", entryID)
	}

	typedAggID, err := r.codec.Decode(rawAggID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
			WithContext("dlq_entry_id", entryID)
	}
	dlqEntry.AggregateID = typedAggID

	// 2. 重新插入到 Outbox（状态为 pending，重试计数清零）
	insertQuery := fmt.Sprintf(`
		INSERT INTO %s (
			aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, retry_count, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.quotedOutboxTable())

	agg, err := r.codec.Encode(dlqEntry.AggregateID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid aggregate id").
			WithContext("dlq_entry_id", entryID)
	}

	err = executeOutboxRecoverableStatement(ctx, tx, "dlq_retry_insert", func() error {
		_, err := tx.Exec(ctx, insertQuery,
			agg,
			dlqEntry.AggregateType,
			dlqEntry.EventID,
			dlqEntry.EventType,
			dlqEntry.EventData,
			outbox.OutboxStatusPending,
			"",
			time.Now(),
			0,
			dlqEntry.FailureReason,
		)
		return err
	})
	if err != nil {
		if !isOutboxDuplicateKeyError(err) {
			return errors.Wrap(err, errors.Database, "insert outbox entry failed").
				WithContext("dlq_entry_id", entryID)
		}
		exists, existsErr := r.ensureOutboxRetryableByEventID(ctx, tx, dlqEntry.EventID, dlqEntry.FailureReason)
		if existsErr != nil {
			return existsErr.WithContext("dlq_entry_id", entryID)
		}
		if !exists {
			return errors.Wrap(err, errors.Database, "insert outbox entry failed").
				WithContext("dlq_entry_id", entryID)
		}
	}

	// 3. 删除 DLQ 记录
	deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, r.quotedDLQTable())
	result, err := tx.Exec(ctx, deleteQuery, entryID)
	if err != nil {
		return errors.Wrap(err, errors.Database, "delete dlq entry failed").
			WithContext("dlq_entry_id", entryID)
	}
	if err := sqlresult.EnsureRowsAffected(result, 1, "delete dlq entry affected unexpected rows"); err != nil {
		return err.WithContext("dlq_entry_id", entryID)
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, errors.Database, "commit retry from dlq transaction failed").
			WithContext("dlq_entry_id", entryID)
	}

	return nil
}

func (r *SQLDLQRepository[ID]) ensureOutboxRetryableByEventID(ctx context.Context, tx db.ITransaction, eventID string, lastError string) (bool, errors.IAppError) {
	query := fmt.Sprintf(`SELECT id, status FROM %s WHERE event_id = ? ORDER BY id DESC LIMIT 1`, r.quotedOutboxTable())
	var (
		entryID int64
		status  outbox.OutboxStatus
	)
	err := tx.QueryRow(ctx, query, eventID).Scan(&entryID, &status)
	switch err {
	case nil:
		switch status {
		case outbox.OutboxStatusFailed, outbox.OutboxStatusDeadLettered:
		case outbox.OutboxStatusPending, outbox.OutboxStatusProcessing:
			return false, errors.NewCode(errors.Conflict, "outbox entry is not retryable from dlq while pending or processing").
				WithContext("event_id", eventID).
				WithContext("status", string(status))
		case outbox.OutboxStatusPublished:
			r.warnDLQRetryDropsEntry(ctx, eventID, status)
			return true, nil
		default:
			return false, errors.NewCode(errors.Internal, "outbox entry has unknown status during dlq retry").
				WithContext("event_id", eventID).
				WithContext("status", string(status))
		}
		updateQuery := fmt.Sprintf(`
			UPDATE %s
			SET status = ?, claim_token = '', lease_until = NULL, next_retry_at = NULL, retry_count = 0, last_error = ?
			WHERE id = ? AND event_id = ? AND status = ?
		`, r.quotedOutboxTable())
		res, err := tx.Exec(ctx, updateQuery, outbox.OutboxStatusPending, lastError, entryID, eventID, status)
		if err != nil {
			return false, errors.Wrap(err, errors.Database, "reset existing outbox entry for dlq retry failed").
				WithContext("event_id", eventID)
		}
		if err := sqlresult.EnsureRowsAffected(res, 1, "reset existing outbox entry for dlq retry affected unexpected rows"); err != nil {
			if !errors.Is(err, errors.Conflict) {
				return false, err.WithContext("event_id", eventID)
			}
			return false, errors.NewCode(errors.Conflict, "outbox entry changed while retrying from dlq").
				WithContext("event_id", eventID).
				WithContext("entry_id", entryID).
				WithContext("status", string(status))
		}
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, errors.Wrap(err, errors.Database, "query outbox entry by event_id failed").
			WithContext("event_id", eventID)
	}
}

func (r *SQLDLQRepository[ID]) warnDLQRetryDropsEntry(ctx context.Context, eventID string, status outbox.OutboxStatus) {
	logger := r.logger
	if logger == nil {
		logger = logging.ComponentLogger("eventing.outbox.dlq")
	}
	logger.Warn(ctx, "drop dlq retry because outbox entry is already terminal",
		logging.String("event_id", eventID),
		logging.String("status", string(status)),
	)
}

// DeleteDLQEntry 删除实体并同步到存储。
//
// 说明：
// - DeleteDLQEntry 删除 DLQ 记录。
func (r *SQLDLQRepository[ID]) DeleteDLQEntry(ctx context.Context, entryID int64) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, r.quotedDLQTable())
	result, err := r.db.Exec(ctx, query, entryID)
	if err != nil {
		return errors.Wrap(err, errors.Database, "delete dlq entry failed").
			WithContext("dlq_entry_id", entryID)
	}

	if err := sqlresult.EnsureRowsAffected(result, 1, "delete dlq entry affected unexpected rows"); err != nil {
		if errors.Is(err, errors.Conflict) {
			return errors.NewCode(errors.NotFound, "dlq entry not found").WithContext("dlq_entry_id", entryID)
		}
		return err.WithContext("dlq_entry_id", entryID)
	}

	return nil
}

// GetDLQCount 从存储中查询实体。
//
// 说明：
// - GetDLQCount 获取 DLQ 记录数量。
func (r *SQLDLQRepository[ID]) GetDLQCount(ctx context.Context) (int64, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, r.quotedDLQTable())
	var count int64
	err := r.db.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, errors.Wrap(err, errors.Database, "query dlq count failed")
	}
	return count, nil
}

// ShouldMoveToDLQ 判断Move到DLQ。
func (r *SQLDLQRepository[ID]) ShouldMoveToDLQ(entry outbox.OutboxEntry[ID]) bool {
	return entry.RetryCount >= r.maxRetries
}
