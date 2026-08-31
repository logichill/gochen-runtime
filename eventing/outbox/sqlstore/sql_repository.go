// Package sqlstore 提供 Outbox 的 SQL 仓储实现。
package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/db/dialect"
	"gochen/errors"

	"gochen-runtime/db/sql/sqlbuilder"

	"gochen-runtime/internal/sqlsafe"
	"gochen/db"
	"gochen/eventing"
	"gochen/eventing/outbox"
	"gochen/eventing/store"
	"gochen/messaging"
	"gochen/observe/logging"
)

const defaultClaimLease = 5 * time.Minute

// SimpleSQLOutboxRepository 提供一个简单直接的 SQL Outbox 仓储实现。
type SimpleSQLOutboxRepository[ID comparable] struct {
	db          db.IDatabase
	eventStore  store.ITransactionalEventStore[ID]
	outboxTable string
	logger      logging.ILogger
	codec       codec.ICodec[ID, any]
	claimLease  time.Duration
}

// IOutboxTableProvider 表示可暴露 SQL Outbox 主表名的仓储能力。
type IOutboxTableProvider interface {
	OutboxTableName() string
}

// IDLQTableProvider 表示可暴露 SQL Outbox DLQ 表名的仓储能力。
type IDLQTableProvider interface {
	DLQTableName() string
}

// SQLOutboxRepositoryOption 配置 SQL Outbox 仓储。
type SQLOutboxRepositoryOption func(*sqlOutboxRepositoryOptions)

type sqlOutboxRepositoryOptions struct {
	outboxTable string
}

// WithOutboxTable 设置 SQL Outbox 主表名；空字符串保留默认表名。
func WithOutboxTable(tableName string) SQLOutboxRepositoryOption {
	return func(opts *sqlOutboxRepositoryOptions) {
		opts.outboxTable = strings.TrimSpace(tableName)
	}
}

func buildSQLOutboxRepositoryOptions(opts []SQLOutboxRepositoryOption) (sqlOutboxRepositoryOptions, error) {
	resolved := sqlOutboxRepositoryOptions{outboxTable: outbox.DefaultOutboxTable}
	for _, opt := range opts {
		if opt != nil {
			opt(&resolved)
		}
	}
	if resolved.outboxTable == "" {
		resolved.outboxTable = outbox.DefaultOutboxTable
	}
	if err := validateTableName(resolved.outboxTable); err != nil {
		return sqlOutboxRepositoryOptions{}, errors.Wrap(err, errors.InvalidInput, "invalid outbox table name").
			WithContext("table", resolved.outboxTable)
	}
	return resolved, nil
}

// NewSimpleSQLOutboxRepository 为 `int64` 聚合 ID 创建一个简化 SQL Outbox 仓储。
func NewSimpleSQLOutboxRepository(db db.IDatabase, eventStore store.ITransactionalEventStore[int64], logger logging.ILogger, opts ...SQLOutboxRepositoryOption) (*SimpleSQLOutboxRepository[int64], error) {
	return NewSimpleSQLOutboxRepositoryWithCodec[int64](db, eventStore, idcodec.NewInt64[int64](), logger, opts...)
}

// NewSimpleSQLOutboxRepositoryWithConfig 创建带 outbox.OutboxConfig 的 int64 SQL Outbox 仓储。
func NewSimpleSQLOutboxRepositoryWithConfig(db db.IDatabase, eventStore store.ITransactionalEventStore[int64], logger logging.ILogger, cfg outbox.OutboxConfig, opts ...SQLOutboxRepositoryOption) (*SimpleSQLOutboxRepository[int64], error) {
	return NewSimpleSQLOutboxRepositoryWithCodecAndConfig[int64](db, eventStore, idcodec.NewInt64[int64](), logger, cfg, opts...)
}

// NewSimpleSQLOutboxRepositoryWithCodec 创建可自定义聚合 ID 编解码方式的 SQL Outbox 仓储。
func NewSimpleSQLOutboxRepositoryWithCodec[ID comparable](
	db db.IDatabase,
	eventStore store.ITransactionalEventStore[ID],
	idCodec codec.ICodec[ID, any],
	logger logging.ILogger,
	opts ...SQLOutboxRepositoryOption,
) (*SimpleSQLOutboxRepository[ID], error) {
	return NewSimpleSQLOutboxRepositoryWithCodecAndConfig(db, eventStore, idCodec, logger, outbox.DefaultOutboxConfig(), opts...)
}

// NewSimpleSQLOutboxRepositoryWithCodecAndConfig 创建带 outbox.OutboxConfig 的 SQL Outbox 仓储。
func NewSimpleSQLOutboxRepositoryWithCodecAndConfig[ID comparable](
	db db.IDatabase,
	eventStore store.ITransactionalEventStore[ID],
	idCodec codec.ICodec[ID, any],
	logger logging.ILogger,
	cfg outbox.OutboxConfig,
	opts ...SQLOutboxRepositoryOption,
) (*SimpleSQLOutboxRepository[ID], error) {
	if logger == nil {
		logger = logging.ComponentLogger("eventing.outbox.repository")
	}
	if db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db cannot be nil")
	}
	if eventStore == nil {
		return nil, errors.NewCode(errors.InvalidInput, "eventStore cannot be nil")
	}
	if idCodec == nil {
		return nil, errors.NewCode(errors.InvalidInput, "codec cannot be nil")
	}
	cfg = outbox.NormalizeOutboxConfig(cfg)
	repoOpts, err := buildSQLOutboxRepositoryOptions(opts)
	if err != nil {
		return nil, err
	}
	return &SimpleSQLOutboxRepository[ID]{
		db:          db,
		eventStore:  eventStore,
		outboxTable: repoOpts.outboxTable,
		logger:      logger,
		codec:       idCodec,
		claimLease:  cfg.ClaimLease,
	}, nil
}

// GetClaimLease 返回仓储实际用于 claim/renew 的租约时长。
func (r *SimpleSQLOutboxRepository[ID]) GetClaimLease() time.Duration {
	if r == nil || r.claimLease <= 0 {
		return defaultClaimLease
	}
	return r.claimLease
}

// SaveWithEvents 在同一事务内保存事件流和对应的 Outbox 记录。
func (r *SimpleSQLOutboxRepository[ID]) SaveWithEvents(ctx context.Context, aggregateID ID, events []eventing.Event[ID]) error {
	if len(events) == 0 {
		return nil
	}
	expectedVersion, versionErr := r.calculateExpectedVersion(events)
	if versionErr != nil {
		return versionErr.WithContext("aggregate_id", aggregateID)
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		r.logger.Error(ctx, "failed to begin transaction", logging.Any("aggregate_id", aggregateID), logging.Error(err))
		return errors.Wrap(err, errors.Database, "begin transaction failed").
			WithContext("aggregate_id", aggregateID)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. 保存事件到事件存储
	storable := store.ToStorable(events)
	if err := r.eventStore.AppendEventsWithDB(ctx, tx, events[0].GetAggregateType(), aggregateID, storable, expectedVersion); err != nil {
		r.logger.Warn(ctx, "failed to save events", logging.Any("aggregate_id", aggregateID), logging.Int("event_count", len(events)), logging.Error(err))
		// 保留事件存储的原始错误码语义（例如 Concurrency），仅补充上下文信息。
		var appErr *errors.AppError
		if errors.As(err, &appErr) && appErr != nil {
			return appErr.
				WithContext("aggregate_id", aggregateID).
				WithContext("event_count", len(events))
		}
		return errors.Wrap(err, errors.Database, "append events failed").
			WithContext("aggregate_id", aggregateID).
			WithContext("event_count", len(events))
	}

	// 2. 保存 Outbox 记录
	if err := r.saveOutboxEntries(ctx, tx, aggregateID, storable); err != nil {
		r.logger.Warn(ctx, "failed to save outbox entries", logging.Any("aggregate_id", aggregateID), logging.Int("event_count", len(events)), logging.Error(err))
		var appErr *errors.AppError
		if errors.As(err, &appErr) && appErr != nil {
			return appErr.
				WithContext("aggregate_id", aggregateID).
				WithContext("event_count", len(events))
		}
		return errors.Wrap(err, errors.Database, "save outbox entries failed").
			WithContext("aggregate_id", aggregateID).
			WithContext("event_count", len(events))
	}

	// 3. 提交事务
	if err := tx.Commit(); err != nil {
		r.logger.Error(ctx, "failed to commit transaction", logging.Any("aggregate_id", aggregateID), logging.Error(err))
		return errors.Wrap(err, errors.Database, "commit transaction failed").
			WithContext("aggregate_id", aggregateID)
	}

	r.logger.Info(ctx, "successfully saved events and outbox entries", logging.Any("aggregate_id", aggregateID), logging.Int("event_count", len(events)))
	return nil
}

type outboxEntryInsert[ID comparable] struct {
	event         eventing.IStorableEvent[ID]
	eventData     string
	aggregateType string
	eventID       string
	eventType     string
	createdAt     time.Time
}

type existingOutboxEntry[ID comparable] struct {
	aggregateID   ID
	aggregateType string
	eventType     string
	eventData     string
}

// saveOutboxEntries 把事件批次写入 Outbox 表。
func (r *SimpleSQLOutboxRepository[ID]) saveOutboxEntries(ctx context.Context, tx db.ITransaction, aggregateID ID, events []eventing.IStorableEvent[ID]) error {
	if len(events) == 0 {
		return nil
	}

	agg, err := r.codec.Encode(aggregateID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}

	sq, err := sqlbuilder.New(tx)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	prepared := make([]outboxEntryInsert[ID], 0, len(events))
	createdAt := time.Now()
	for _, event := range events {
		eventData, err := r.serializeEvent(event)
		if err != nil {
			return errors.Wrap(err, errors.Internal, "serialize event failed").
				WithContext("event_id", event.GetID())
		}
		prepared = append(prepared, outboxEntryInsert[ID]{
			event:         event,
			eventData:     eventData,
			aggregateType: event.GetAggregateType(),
			eventID:       event.GetID(),
			eventType:     event.GetType(),
			createdAt:     createdAt,
		})
	}

	if len(prepared) == 1 {
		return r.insertOutboxEntry(ctx, sq, tx, aggregateID, agg, prepared[0])
	}

	maxEntries := outboxInsertChunkSize(9)
	for start := 0; start < len(prepared); start += maxEntries {
		end := start + maxEntries
		if end > len(prepared) {
			end = len(prepared)
		}
		insert := sq.InsertInto(r.outboxTable).
			Columns("aggregate_id", "aggregate_type", "event_id", "event_type", "event_data", "status", "claim_token", "created_at", "retry_count")
		for _, entry := range prepared[start:end] {
			insert = insert.Values(
				agg,
				entry.aggregateType,
				entry.eventID,
				entry.eventType,
				entry.eventData,
				outbox.OutboxStatusPending,
				"",
				entry.createdAt,
				0,
			)
		}
		if err := executeOutboxRecoverableStatement(ctx, tx, "batch_insert", func() error {
			_, err := insert.Exec(ctx)
			return err
		}); err != nil {
			if isOutboxDuplicateKeyError(err) {
				missing, err := r.filterMissingOutboxEntries(ctx, tx, aggregateID, prepared[start:end])
				if err != nil {
					return err
				}
				if err := r.saveOutboxEntriesIndividually(ctx, sq, tx, aggregateID, agg, missing); err != nil {
					return err
				}
				continue
			}
			return errors.Wrap(err, errors.Database, "batch insert outbox entries failed")
		}
	}

	return nil
}

func (r *SimpleSQLOutboxRepository[ID]) filterMissingOutboxEntries(
	ctx context.Context,
	tx db.IDatabase,
	aggregateID ID,
	entries []outboxEntryInsert[ID],
) ([]outboxEntryInsert[ID], error) {
	existing, err := r.loadExistingOutboxEntries(ctx, tx, entries)
	if err != nil {
		existing, err = r.loadExistingOutboxEntries(ctx, r.db, entries)
	}
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "load existing outbox entries failed", err)
	}
	missing := make([]outboxEntryInsert[ID], 0, len(entries))
	for _, entry := range entries {
		current, ok := existing[entry.eventID]
		if !ok {
			missing = append(missing, entry)
			continue
		}
		if current.aggregateID == aggregateID &&
			current.aggregateType == entry.aggregateType &&
			current.eventType == entry.eventType &&
			current.eventData == entry.eventData {
			continue
		}
		return nil, errors.NewCode(errors.Duplicate, "outbox event id already exists with different content").
			WithContext("event_id", entry.eventID).
			WithContext("event_type", entry.eventType)
	}
	return missing, nil
}

func (r *SimpleSQLOutboxRepository[ID]) loadExistingOutboxEntries(ctx context.Context, database db.IDatabase, entries []outboxEntryInsert[ID]) (map[string]existingOutboxEntry[ID], error) {
	result := make(map[string]existingOutboxEntry[ID])
	if len(entries) == 0 {
		return result, nil
	}
	tableName, err := r.validatedOutboxTableName()
	if err != nil {
		return nil, err
	}
	eventIDs := make([]any, 0, len(entries))
	for _, entry := range entries {
		eventIDs = append(eventIDs, entry.eventID)
	}
	tableName = dialect.FromDatabase(database).QuoteIdentifier(tableName)
	rows, err := database.Query(ctx,
		fmt.Sprintf("SELECT aggregate_id, aggregate_type, event_id, event_type, event_data FROM %s WHERE event_id IN (%s)", tableName, outboxPlaceholders(len(eventIDs))),
		eventIDs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rawAggID any
		var aggregateType, eventID, eventType, eventData string
		if err := rows.Scan(&rawAggID, &aggregateType, &eventID, &eventType, &eventData); err != nil {
			return nil, err
		}
		typedAggID, err := r.codec.Decode(rawAggID)
		if err != nil {
			return nil, err
		}
		result[eventID] = existingOutboxEntry[ID]{
			aggregateID:   typedAggID,
			aggregateType: aggregateType,
			eventType:     eventType,
			eventData:     eventData,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func outboxPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func outboxInsertChunkSize(argsPerEntry int) int {
	if argsPerEntry <= 0 || sqlsafe.DefaultInsertMaxParams <= argsPerEntry {
		return 1
	}
	return sqlsafe.DefaultInsertMaxParams / argsPerEntry
}

func (r *SimpleSQLOutboxRepository[ID]) saveOutboxEntriesIndividually(
	ctx context.Context,
	sq sqlbuilder.ISQL,
	tx db.ITransaction,
	aggregateID ID,
	agg any,
	entries []outboxEntryInsert[ID],
) error {
	for _, entry := range entries {
		if err := r.insertOutboxEntry(ctx, sq, tx, aggregateID, agg, entry); err != nil {
			return err
		}
	}
	return nil
}

func (r *SimpleSQLOutboxRepository[ID]) insertOutboxEntry(
	ctx context.Context,
	sq sqlbuilder.ISQL,
	tx db.ITransaction,
	aggregateID ID,
	agg any,
	entry outboxEntryInsert[ID],
) error {
	err := executeOutboxRecoverableStatement(ctx, tx, "insert", func() error {
		_, err := sq.InsertInto(r.outboxTable).
			Columns("aggregate_id", "aggregate_type", "event_id", "event_type", "event_data", "status", "claim_token", "created_at", "retry_count").
			Values(
				agg,
				entry.aggregateType,
				entry.eventID,
				entry.eventType,
				entry.eventData,
				outbox.OutboxStatusPending,
				"",
				entry.createdAt,
				0,
			).Exec(ctx)
		return err
	})
	if err == nil {
		return nil
	}
	if isOutboxDuplicateKeyError(err) {
		// Prefer tx-local lookup so READ COMMITTED cannot treat an
		// uncommitted duplicate as committed state; fall back to r.db
		// for engines/snapshots where the duplicate row is already
		// committed but not visible inside the current transaction.
		same, checkErr := r.sameOutboxEntry(ctx, tx, aggregateID, entry.event, entry.eventData)
		if checkErr != nil {
			same, checkErr = r.sameOutboxEntry(ctx, r.db, aggregateID, entry.event, entry.eventData)
		}
		if checkErr != nil {
			return errors.NewCodeWithCause(errors.Database, "insert outbox entry failed", err).
				WithContext("event_id", entry.eventID).
				WithContext("conflict_resolution_error", checkErr.Error())
		}
		if same {
			return nil
		}
		return errors.NewCode(errors.Duplicate, "outbox event id already exists with different content").
			WithContext("event_id", entry.eventID).
			WithContext("event_type", entry.eventType)
	}
	return errors.Wrap(err, errors.Database, "insert outbox entry failed")
}

func (r *SimpleSQLOutboxRepository[ID]) sameOutboxEntry(ctx context.Context, database db.IDatabase, aggregateID ID, event eventing.IStorableEvent[ID], eventData string) (bool, error) {
	tableName, err := r.validatedOutboxTableName()
	if err != nil {
		return false, err
	}
	tableName = dialect.FromDatabase(database).QuoteIdentifier(tableName)
	var rawAggID any
	var aggregateType, eventType, existingData string
	row := database.QueryRow(ctx,
		fmt.Sprintf("SELECT aggregate_id, aggregate_type, event_type, event_data FROM %s WHERE event_id = ?", tableName),
		event.GetID(),
	)
	if err := row.Scan(&rawAggID, &aggregateType, &eventType, &existingData); err != nil {
		return false, err
	}
	typedAggID, err := r.codec.Decode(rawAggID)
	if err != nil {
		return false, err
	}
	return typedAggID == aggregateID &&
		aggregateType == event.GetAggregateType() &&
		eventType == event.GetType() &&
		existingData == eventData, nil
}

func (r *SimpleSQLOutboxRepository[ID]) validatedOutboxTableName() (string, error) {
	if r == nil {
		return "", errors.NewCode(errors.InvalidInput, "outbox repository is nil")
	}
	tableName := strings.TrimSpace(r.outboxTable)
	if err := validateTableName(tableName); err != nil {
		return "", err
	}
	return tableName, nil
}

// OutboxTableName 返回 Outbox 记录使用的 SQL 表名。
func (r *SimpleSQLOutboxRepository[ID]) OutboxTableName() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.outboxTable)
}

// ClaimPendingEntries 原子 claim 当前可发布或到期可重试的 Outbox 记录。
func (r *SimpleSQLOutboxRepository[ID]) ClaimPendingEntries(ctx context.Context, limit int) ([]outbox.OutboxEntry[ID], error) {
	if limit <= 0 {
		return nil, nil
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "begin transaction failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	sq, err := sqlbuilder.New(tx)
	if err != nil {
		return nil, errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	now := time.Now()
	claimToken, err := newClaimToken()
	if err != nil {
		return nil, err
	}
	leaseUntil := now.Add(r.claimLease)

	builder := sq.Select(
		"id", "aggregate_id", "aggregate_type", "event_id", "event_type", "event_data",
		"status", "claim_token", "created_at", "published_at", "retry_count", "last_error", "lease_until", "next_retry_at",
	).From(r.outboxTable).
		Where(claimableOutboxEntriesWhere(), claimableOutboxEntriesArgs(now)...).
		OrderBy(sqlbuilder.OrderAsc("created_at"), sqlbuilder.OrderAsc("id")).
		Limit(limit).
		ForUpdate().
		SkipLocked()

	rows, err := builder.Query(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "query pending entries failed")
	}
	// defer 兜底关闭，保证 scan/decode 错误提前 return 时不泄漏 rows；
	// 成功路径下方仍显式 Close 以捕获关闭错误（sql.Rows.Close 可重复调用）。
	defer func() { _ = rows.Close() }()
	var entries []outbox.OutboxEntry[ID]
	for rows.Next() {
		var entry outbox.OutboxEntry[ID]
		var rawAggID any
		var publishedAt, leaseUntil, nextRetryAt sql.NullTime
		var lastError sql.NullString

		err := rows.Scan(
			&entry.ID, &rawAggID, &entry.AggregateType,
			&entry.EventID, &entry.EventType, &entry.EventData,
			&entry.Status, &entry.ClaimToken, &entry.CreatedAt, &publishedAt,
			&entry.RetryCount, &lastError, &leaseUntil, &nextRetryAt,
		)
		if err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan entry failed")
		}
		typedAggID, err := r.codec.Decode(rawAggID)
		if err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
				WithContext("entry_id", entry.ID).
				WithContext("event_id", entry.EventID)
		}
		entry.AggregateID = typedAggID

		if publishedAt.Valid {
			entry.PublishedAt = &publishedAt.Time
		}
		if lastError.Valid {
			entry.LastError = lastError.String
		}
		if leaseUntil.Valid {
			entry.LeaseUntil = &leaseUntil.Time
		}
		if nextRetryAt.Valid {
			entry.NextRetryAt = &nextRetryAt.Time
		}

		entries = append(entries, entry)
	}

	rowsErr := rows.Err()
	if rowsErr != nil {
		_ = rows.Close()
		return nil, rowsErr
	}
	if err := rows.Close(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "close pending entries rows failed")
	}

	if len(entries) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, errors.Wrap(err, errors.Database, "commit transaction failed")
		}
		committed = true
		return entries, nil
	}

	result, err := sq.Update(r.outboxTable).
		Set("status", outbox.OutboxStatusProcessing).
		Set("claim_token", claimToken).
		Set("lease_until", leaseUntil).
		Set("next_retry_at", nil).
		Where(inIDsClause("id", len(entries)), entryIDsToArgs(entries)...).
		Where(claimableOutboxEntriesWhere(), claimableOutboxEntriesArgs(now)...).
		Exec(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "claim pending entries failed")
	}

	if err := ensureRowsAffected(result, int64(len(entries)), "claim pending entries affected unexpected rows"); err != nil {
		return nil, err
	}

	for i := range entries {
		entries[i].Status = outbox.OutboxStatusProcessing
		entries[i].ClaimToken = claimToken
		entries[i].LeaseUntil = &leaseUntil
		entries[i].NextRetryAt = nil
	}

	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "commit transaction failed")
	}
	committed = true
	return entries, nil
}

// MarkAsPublished 把指定 Outbox 记录更新为已发布状态。
func (r *SimpleSQLOutboxRepository[ID]) MarkAsPublished(ctx context.Context, entryID int64, claimToken string) error {
	sq, err := sqlbuilder.New(r.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	result, err := sq.Update(r.outboxTable).
		Set("status", outbox.OutboxStatusPublished).
		Set("claim_token", "").
		Set("lease_until", nil).
		Set("published_at", time.Now()).
		Set("next_retry_at", nil).
		Where("id = ?", entryID).
		Where("claim_token = ?", claimToken).
		Where("status = ?", outbox.OutboxStatusProcessing).
		Exec(ctx)
	if err != nil {
		r.logger.Warn(ctx, "failed to mark as published", logging.Int64("entry_id", entryID), logging.Error(err))
		return errors.Wrap(err, errors.Database, "mark as published failed").
			WithContext("entry_id", entryID)
	}

	if err := ensureRowsAffected(result, 1, "outbox entry claim is no longer owned"); err != nil {
		return err.WithContext("entry_id", entryID)
	}
	return nil
}

// MarkAsFailed 记录失败原因并安排下次重试时间。
func (r *SimpleSQLOutboxRepository[ID]) MarkAsFailed(ctx context.Context, entryID int64, claimToken string, errorMsg string, nextRetryAt time.Time) error {
	sq, err := sqlbuilder.New(r.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	result, err := sq.Update(r.outboxTable).
		Set("status", outbox.OutboxStatusFailed).
		Set("claim_token", "").
		Set("lease_until", nil).
		Set("last_error", errorMsg).
		SetIncrement("retry_count", 1).
		Set("next_retry_at", nextRetryAt).
		Where("id = ?", entryID).
		Where("claim_token = ?", claimToken).
		Where("status = ?", outbox.OutboxStatusProcessing).
		Exec(ctx)
	if err != nil {
		r.logger.Warn(ctx, "failed to mark as failed", logging.Int64("entry_id", entryID), logging.Error(err))
		return errors.Wrap(err, errors.Database, "mark as failed failed").
			WithContext("entry_id", entryID)
	}

	if err := ensureRowsAffected(result, 1, "outbox entry claim is no longer owned"); err != nil {
		return err.WithContext("entry_id", entryID)
	}
	return nil
}

// RenewClaim 延长指定 claim 的 lease，避免长时间处理过程中被其他 worker 重新 claim。
func (r *SimpleSQLOutboxRepository[ID]) RenewClaim(ctx context.Context, entryID int64, claimToken string) error {
	sq, err := sqlbuilder.New(r.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	leaseUntil := time.Now().Add(r.claimLease)
	result, err := sq.Update(r.outboxTable).
		Set("lease_until", leaseUntil).
		Where("id = ?", entryID).
		Where("claim_token = ?", claimToken).
		Where("status = ?", outbox.OutboxStatusProcessing).
		Exec(ctx)
	if err != nil {
		r.logger.Warn(ctx, "failed to renew outbox claim", logging.Int64("entry_id", entryID), logging.Error(err))
		return errors.Wrap(err, errors.Database, "renew outbox claim failed").
			WithContext("entry_id", entryID)
	}

	if err := ensureRowsAffected(result, 1, "outbox entry claim is no longer owned"); err != nil {
		return err.WithContext("entry_id", entryID)
	}
	return nil
}

// DeletePublished 删除发布时间早于阈值的已发布记录。
func (r *SimpleSQLOutboxRepository[ID]) DeletePublished(ctx context.Context, olderThan time.Time) error {
	sq, err := sqlbuilder.New(r.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	builder := sq.DeleteFrom(r.outboxTable).
		Where("status = ?", outbox.OutboxStatusPublished).
		Where("published_at < ?", olderThan)

	result, err := builder.Exec(ctx)
	if err != nil {
		return errors.Wrap(err, errors.Database, "delete published failed").
			WithContext("older_than", olderThan)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		if rowsErr := rowsAffectedError(err, "delete published rows affected failed"); rowsErr != nil {
			return rowsErr.WithContext("older_than", olderThan)
		}
	}
	if rowsAffected > 0 {
		r.logger.Info(ctx, "cleaned up published outbox entries", logging.Int64("deleted", rowsAffected))
	}
	return nil
}

// calculateExpectedVersion 根据事件批次首条事件的版本推导 expectedVersion。
func (r *SimpleSQLOutboxRepository[ID]) calculateExpectedVersion(events []eventing.Event[ID]) (uint64, errors.IAppError) {
	if len(events) == 0 {
		return 0, nil
	}
	for i, event := range events {
		if event.GetVersion() == 0 {
			return 0, errors.NewCode(errors.InvalidInput, "event version must be positive").
				WithContext("event_id", event.GetID()).
				WithContext("index", i)
		}
	}
	return events[0].GetVersion() - 1, nil
}

// serializeEvent 把事件编码为存入 Outbox 的 JSON 文本。
func (r *SimpleSQLOutboxRepository[ID]) serializeEvent(event eventing.IStorableEvent[ID]) (string, error) {
	data := map[string]any{
		"id":             event.GetID(),
		"type":           event.GetType(),
		"aggregate_id":   event.GetAggregateID(),
		"aggregate_type": event.GetAggregateType(),
		"version":        event.GetVersion(),
		"schema_version": event.EventSchemaVersion(),
		"timestamp":      event.GetTimestamp(),
		"payload":        messaging.PayloadValue(event.GetPayload()),
		"metadata":       event.GetMetadata(),
	}

	bytes, err := json.Marshal(data)
	if err != nil {
		return "", errors.Wrap(err, errors.Internal, "marshal event failed")
	}

	return string(bytes), nil
}

func claimableOutboxEntriesWhere() string {
	return "((status = ?) OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND lease_until <= ?))"
}

func claimableOutboxEntriesArgs(now time.Time) []any {
	return []any{outbox.OutboxStatusPending, outbox.OutboxStatusFailed, now, outbox.OutboxStatusProcessing, now}
}

func entryIDsToArgs[ID comparable](entries []outbox.OutboxEntry[ID]) []any {
	args := make([]any, 0, len(entries))
	for _, entry := range entries {
		args = append(args, entry.ID)
	}
	return args
}

func int64SliceToArgs(ids []int64) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func inIDsClause(column string, count int) string {
	placeholders := make([]string, count)
	for i := 0; i < count; i++ {
		placeholders[i] = "?"
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ","))
}

func newClaimToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.Wrap(err, errors.Internal, "generate outbox claim token failed")
	}
	return fmt.Sprintf("%x", raw[:]), nil
}

func isOutboxDuplicateKeyError(err error) bool {
	return db.IsUniqueViolation(err)
}
