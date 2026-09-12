package sqlstore

import (
	"context"
	"fmt"
	"time"

	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
	"gochen/eventing/outbox"
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
	sleepTimer := time.NewTimer(0)
	if !sleepTimer.Stop() {
		select {
		case <-sleepTimer.C:
		default:
		}
	}
	defer sleepTimer.Stop()

	for {
		affected, err := s.archiveBatch(ctx, olderThan)
		if err != nil {
			return totalArchived, err
		}

		totalArchived += affected
		if affected < int64(s.policy.BatchSize) {
			break
		}

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

// archiveBatch 将单批事务限定在一个调用内，确保错误、panic 和提交失败均会回滚。
func (s *CleanupService) archiveBatch(ctx context.Context, olderThan time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.Wrap(err, errors.Dependency, "begin archive transaction failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	entryIDs, err := s.selectArchiveBatchIDs(ctx, tx, olderThan, s.policy.BatchSize)
	if err != nil {
		return 0, err
	}
	var affected int64
	if len(entryIDs) > 0 {
		insertQuery, insertArgs := s.buildArchiveInsertByIDsQuery(entryIDs)
		if _, err := tx.Exec(ctx, insertQuery, insertArgs...); err != nil {
			return 0, errors.Wrap(err, errors.Dependency, "archive records failed")
		}

		deleteQuery, deleteArgs := s.buildDeleteByIDsQuery(entryIDs)
		deleteResult, err := tx.Exec(ctx, deleteQuery, deleteArgs...)
		if err != nil {
			return 0, errors.Wrap(err, errors.Dependency, "delete archived records failed")
		}
		affected, err = readRowsAffected(deleteResult, "get rows affected failed")
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.Wrap(err, errors.Dependency, "commit archive transaction failed")
	}
	committed = true
	return affected, nil
}

func (s *CleanupService) selectArchiveBatchIDs(ctx context.Context, tx db.ITransaction, olderThan time.Time, limit int) ([]int64, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT id
		FROM %s
		WHERE ((status = ? AND published_at < ?) OR (status = ? AND created_at < ?))
		ORDER BY created_at, id
		LIMIT ?
	`, s.outboxTable()), outbox.OutboxStatusPublished, olderThan, outbox.OutboxStatusDeadLettered, olderThan, limit)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "select archive batch ids failed")
	}
	defer rows.Close()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, errors.Wrap(err, errors.Dependency, "scan archive batch id failed")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "iterate archive batch ids failed")
	}
	return ids, nil
}

func (s *CleanupService) buildArchiveInsertByIDsQuery(entryIDs []int64) (string, []any) {
	intoClause := "INSERT INTO"
	suffix := ""
	switch s.dialect.Name() {
	case dialect.NameSQLite:
		intoClause = "INSERT OR IGNORE INTO"
	case dialect.NameMySQL:
		intoClause = "INSERT IGNORE INTO"
	case dialect.NamePostgres:
		suffix = " ON CONFLICT (id) DO NOTHING"
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
