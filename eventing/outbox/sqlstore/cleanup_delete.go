package sqlstore

import (
	"context"
	"fmt"
	coredialect "gochen/db/dialect"
	"time"

	"gochen-runtime/internal/sqlresult"

	"gochen/errors"
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

// buildDeleteBatchQuery 生成分批删除语句。
//
// 取舍说明：MySQL 走 DELETE ... LIMIT（行业常规，无 keyset 可言）；PG/SQLite
// 子查询路径每批从头执行 ORDER BY created_at, id 的子查询，没有 (created_at, id)
// 游标——DELETE 不回传本批行的键值，加游标必须先 SELECT 再 DELETE（往返翻倍）。
// 不可删除“表头”很大时该路径有 O(批数 × 表头) 放大；主要删除路径
// SimpleSQLOutboxRepository.DeletePublished 已改为 SELECT+DELETE keyset 游标，
// 本路径服务于无仓储参与的 CleanupService，维持单语句形态并在此记录取舍。
func (s *CleanupService) buildDeleteBatchQuery(olderThan time.Time, limit int64) (string, []any) {
	cond, args := s.buildEligibleCondition(olderThan)
	allArgs := append(append([]any(nil), args...), limit)
	if s.dialect.SupportsDeleteLimit() || s.dialect.Name() == coredialect.NameMySQL {
		return fmt.Sprintf(`
			DELETE FROM %s
			WHERE %s
			LIMIT ?
		`, s.outboxTable(), cond), allArgs
	}

	return fmt.Sprintf(`
		DELETE FROM %s
		WHERE id IN (
			SELECT id FROM %s
			WHERE %s
			ORDER BY created_at, id
			LIMIT ?
		)
	`, s.outboxTable(), s.outboxTable(), cond), allArgs
}
