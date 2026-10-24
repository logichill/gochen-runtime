package sqlstore

import (
	"context"
	"fmt"
	"time"

	"gochen/errors"
)

// countOldRecords 统计旧记录数量。
func (s *CleanupService) countOldRecords(ctx context.Context, olderThan time.Time) (int64, error) {
	cond, args := s.buildEligibleCondition(olderThan)
	query := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		WHERE %s
	`, s.outboxTable(), cond)

	var count int64
	err := s.db.QueryRow(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, errors.Wrap(err, errors.Dependency, "count old records failed")
	}
	return count, nil
}

func (s *CleanupService) Statistics(ctx context.Context) (*OutboxStatistics, error) {
	query := fmt.Sprintf(`
			SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
			COALESCE(SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END), 0) as processing,
			COALESCE(SUM(CASE WHEN status = 'published' THEN 1 ELSE 0 END), 0) as published,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(CASE WHEN status = 'dead_lettered' THEN 1 ELSE 0 END), 0) as dead_lettered,
			MIN(created_at) as oldest,
			MAX(created_at) as newest
		FROM %s
	`, s.outboxTable())

	var stats OutboxStatistics
	var oldest, newest any
	err := s.db.QueryRow(ctx, query).Scan(
		&stats.PendingCount,
		&stats.ProcessingCount,
		&stats.PublishedCount,
		&stats.FailedCount,
		&stats.DeadLetteredCount,
		&oldest,
		&newest,
	)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "get outbox statistics failed")
	}
	if parsed, ok := parseSQLTimeValue(oldest); ok {
		stats.OldestCreatedAt = parsed
	}
	if parsed, ok := parseSQLTimeValue(newest); ok {
		stats.NewestCreatedAt = parsed
	}
	return &stats, nil
}

// OutboxStatistics 定义发件箱统计信息。
type OutboxStatistics struct {
	PendingCount int64 `json:"pending_count"`
	// ProcessingCount 是被发布器预占、尚未回写终态的“在途”记录数。
	// 只统计 pending/published/failed/dead_lettered 会让在途记录在监控大盘上消失
	// （pending 已减、published 未加），形成统计黑洞。
	ProcessingCount   int64     `json:"processing_count"`
	PublishedCount    int64     `json:"published_count"`
	FailedCount       int64     `json:"failed_count"`
	DeadLetteredCount int64     `json:"dead_lettered_count"`
	OldestCreatedAt   time.Time `json:"oldest_created_at"`
	NewestCreatedAt   time.Time `json:"newest_created_at"`
}
