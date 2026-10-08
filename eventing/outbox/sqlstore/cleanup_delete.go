package sqlstore

import (
	"context"
	"fmt"
	coredialect "gochen/db/dialect"
	"time"

	"gochen-runtime/internal/sqlresult"

	"gochen/errors"
	"gochen/eventing/outbox"
)

// deleteOldRecords 分批删除早于阈值的已发布记录。
func (s *CleanupService) deleteOldRecords(ctx context.Context, olderThan time.Time) (int64, error) {
	if s.policy.DryRun {
		return s.countOldRecords(ctx, olderThan)
	}

	var totalDeleted int64
	sleepTimer := time.NewTimer(0)
	if !sleepTimer.Stop() {
		select {
		case <-sleepTimer.C:
		default:
		}
	}
	defer sleepTimer.Stop()

	for {
		sqlQuery, args := s.buildDeleteBatchQuery(olderThan, int64(s.policy.BatchSize))

		rawResult, err := s.db.Exec(ctx, sqlQuery, args...)
		if err != nil {
			return totalDeleted, errors.Wrap(err, errors.Dependency, "delete old records failed")
		}

		affected, appErr := sqlresult.ReadRowsAffected(rawResult, "get rows affected failed", "get rows affected failed")
		if appErr != nil {
			return totalDeleted, appErr
		}

		totalDeleted += affected
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
			return totalDeleted, ctx.Err()
		case <-sleepTimer.C:
		}
	}

	return totalDeleted, nil
}

func (s *CleanupService) buildDeleteBatchQuery(olderThan time.Time, limit int64) (string, []any) {
	if s.dialect.Name() == coredialect.NameMySQL {
		return fmt.Sprintf(`
			DELETE FROM %s
			WHERE ((status = ? AND published_at < ?) OR (status = ? AND created_at < ?))
			LIMIT ?
		`, s.outboxTable()), []any{outbox.OutboxStatusPublished, olderThan, outbox.OutboxStatusDeadLettered, olderThan, limit}
	}

	return fmt.Sprintf(`
		DELETE FROM %s
		WHERE id IN (
			SELECT id FROM %s
			WHERE ((status = ? AND published_at < ?) OR (status = ? AND created_at < ?))
			ORDER BY created_at, id
			LIMIT ?
		)
	`, s.outboxTable(), s.outboxTable()), []any{outbox.OutboxStatusPublished, olderThan, outbox.OutboxStatusDeadLettered, olderThan, limit}
}
