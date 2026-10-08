package sqlstore

import (
	"context"
	"fmt"
	coredialect "gochen/db/dialect"
	"time"

	"gochen-runtime/internal/sqlresult"
	"gochen/db"

	"gochen/errors"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

// archiveOldRecords 分批把旧记录迁移到归档表，并删除原始记录。
func (s *CleanupService) archiveOldRecords(ctx context.Context, olderThan time.Time) (int64, error) {
	if s.policy.DryRun {
		return s.countOldRecords(ctx, olderThan)
	}
	if err := s.ensureArchiveTable(ctx); err != nil {
		return 0, err
	}

	var totalArchived int64
	var cursor *archiveCursor
	sleepTimer := time.NewTimer(0)
	if !sleepTimer.Stop() {
		select {
		case <-sleepTimer.C:
		default:
		}
	}
	defer sleepTimer.Stop()

	for {
		affected, next, err := s.archiveBatch(ctx, olderThan, cursor)
		if err != nil {
			return totalArchived, err
		}

		totalArchived += affected
		if next == nil {
			break
		}
		cursor = next

		if !sleepTimer.Stop() {
			select {
			case <-sleepTimer.C:
			default:
			}
		}
		sleepTimer.Reset(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			return totalArchived, ctx.Err()
		case <-sleepTimer.C:
		}
	}

	return totalArchived, nil
}

// archiveCursor 按选中行推进扫描，归档冲突留下的行不会阻塞后续批次。
type archiveCursor struct {
	createdAt any
	id        int64
}

// archiveBatch 将单批事务限定在一个调用内，确保错误、panic 和提交失败均会回滚。
func (s *CleanupService) archiveBatch(ctx context.Context, olderThan time.Time, cursor *archiveCursor) (int64, *archiveCursor, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, errors.Wrap(err, errors.Dependency, "begin archive transaction failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	entryIDs, next, err := s.selectArchiveBatchIDs(ctx, tx, olderThan, s.policy.BatchSize, cursor)
	if err != nil {
		return 0, nil, err
	}
	var affected int64
	if len(entryIDs) > 0 {
		insertQuery, insertArgs := s.buildArchiveInsertByIDsQuery(entryIDs)
		if _, err := tx.Exec(ctx, insertQuery, insertArgs...); err != nil {
			return 0, nil, errors.Wrap(err, errors.Dependency, "archive records failed")
		}

		// 归档插入对 id 冲突静默忽略（INSERT OR IGNORE / ON CONFLICT DO NOTHING）。
		// 当 outbox id 被复用且该 id 已属于其它历史事件时，本批对应行并未真正归档，
		// 必须保留在 outbox，避免事件在 outbox 与归档中同时消失。
		archivedIDs, err := s.selectArchivedIDsForBatch(ctx, tx, entryIDs)
		if err != nil {
			return 0, nil, err
		}
		if skipped := len(entryIDs) - len(archivedIDs); skipped > 0 {
			s.log.Warn(ctx, "archive skipped outbox rows whose id is occupied by another archived event; rows kept in outbox",
				logging.Int64("skipped_count", int64(skipped)),
				logging.String("outbox_table", s.policy.OutboxTable),
			)
		}
		if len(archivedIDs) > 0 {
			deleteQuery, deleteArgs := s.buildDeleteByIDsQuery(archivedIDs)
			deleteResult, err := tx.Exec(ctx, deleteQuery, deleteArgs...)
			if err != nil {
				return 0, nil, errors.Wrap(err, errors.Dependency, "delete archived records failed")
			}
			affected, err = sqlresult.ReadRowsAffected(deleteResult, "get rows affected failed", "get rows affected failed")
			if err != nil {
				return 0, nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, errors.Wrap(err, errors.Dependency, "commit archive transaction failed")
	}
	committed = true
	return affected, next, nil
}

func (s *CleanupService) selectArchiveBatchIDs(ctx context.Context, tx db.ITransaction, olderThan time.Time, limit int, cursor *archiveCursor) ([]int64, *archiveCursor, error) {
	after := ""
	args := []any{outbox.OutboxStatusPublished, olderThan, outbox.OutboxStatusDeadLettered, olderThan}
	if cursor != nil {
		after = " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, cursor.createdAt, cursor.createdAt, cursor.id)
	}
	args = append(args, limit)
	createdAtColumn := "created_at"
	if s.dialect.Name() == coredialect.NameSQLite {
		// SQLite 按存储文本排序；转成 time.Time 再绑定会改变格式，跳过同一时间的后续 ID。
		createdAtColumn = "CAST(created_at AS TEXT)"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT id, %s
		FROM %s
		WHERE ((status = ? AND published_at < ?) OR (status = ? AND created_at < ?))%s
		ORDER BY created_at, id
		LIMIT ?
	`, createdAtColumn, s.outboxTable(), after), args...)
	if err != nil {
		return nil, nil, errors.Wrap(err, errors.Dependency, "select archive batch ids failed")
	}
	defer rows.Close()

	ids := make([]int64, 0, limit)
	var last archiveCursor
	for rows.Next() {
		if err := rows.Scan(&last.id, &last.createdAt); err != nil {
			return nil, nil, errors.Wrap(err, errors.Dependency, "scan archive batch id failed")
		}
		ids = append(ids, last.id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, errors.Wrap(err, errors.Dependency, "iterate archive batch ids failed")
	}
	if len(ids) == limit {
		return ids, &last, nil
	}
	return ids, nil, nil
}

// selectArchivedIDsForBatch 返回本批 outbox id 中已确认在归档表存在对应行（id 与 event_id 均一致）的子集。
// 只有确认复制成功的行才能安全删除：归档插入可能因 id 被历史归档行占用而被静默忽略（R2d-F03）。
func (s *CleanupService) selectArchivedIDsForBatch(ctx context.Context, tx db.ITransaction, entryIDs []int64) ([]int64, error) {
	if len(entryIDs) == 0 {
		return nil, nil
	}

	query := fmt.Sprintf(`
		SELECT t.id
		FROM %s t
		JOIN %s a ON a.id = t.id AND a.event_id = t.event_id
		WHERE %s
	`, s.outboxTable(), s.dialect.QuoteIdentifier(s.policy.ArchiveTable), inIDsClause("t.id", len(entryIDs)))
	rows, err := tx.Query(ctx, query, int64SliceToArgs(entryIDs)...)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "select archived batch ids failed")
	}
	defer rows.Close()

	ids := make([]int64, 0, len(entryIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "scan archived batch id failed")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "iterate archived batch ids failed")
	}
	return ids, nil
}

func (s *CleanupService) buildArchiveInsertByIDsQuery(entryIDs []int64) (string, []any) {
	intoClause := "INSERT INTO"
	suffix := ""
	switch s.dialect.Name() {
	case coredialect.NameSQLite:
		intoClause = "INSERT OR IGNORE INTO"
	case coredialect.NameMySQL:
		intoClause = "INSERT IGNORE INTO"
	case coredialect.NamePostgres:
		suffix = " ON CONFLICT DO NOTHING"
	}

	return fmt.Sprintf(`
		%s %s (
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		)
		SELECT
			id, aggregate_id, aggregate_type, event_id, event_type, event_data,
			status, claim_token, created_at, published_at, retry_count, last_error, lease_until, next_retry_at
		FROM %s
		WHERE %s%s
	`, intoClause, s.dialect.QuoteIdentifier(s.policy.ArchiveTable), s.outboxTable(), inIDsClause("id", len(entryIDs)), suffix), int64SliceToArgs(entryIDs)
}

func (s *CleanupService) buildDeleteByIDsQuery(entryIDs []int64) (string, []any) {
	return fmt.Sprintf(`
		DELETE FROM %s
		WHERE %s
	`, s.outboxTable(), inIDsClause("id", len(entryIDs))), int64SliceToArgs(entryIDs)
}
