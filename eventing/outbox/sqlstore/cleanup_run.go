package sqlstore

import (
	"context"
	"time"
)

// Cleanup 根据策略执行一次删除或归档任务，并返回结果摘要。
func (s *CleanupService) Cleanup(ctx context.Context) (*CleanupResult, error) {
	startTime := time.Now()
	result := &CleanupResult{}

	cutoffTime := time.Now().AddDate(0, 0, -s.policy.RetentionDays)

	s.log.Info(ctx, "cleanup started")

	var err error
	if s.policy.ArchiveEnabled {
		result.ArchivedCount, err = s.archiveOldRecords(ctx, cutoffTime)
	} else {
		result.DeletedCount, err = s.deleteOldRecords(ctx, cutoffTime)
	}

	result.Duration = time.Since(startTime)
	result.Error = err

	if err != nil {
		s.log.Error(ctx, "cleanup failed")
		return result, err
	}

	s.log.Info(ctx, "cleanup completed")
	return result, nil
}
