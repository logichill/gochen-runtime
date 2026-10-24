package sqlstore

import (
	"context"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	"gochen/eventing"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

// —— P2#22（fingerprint b49c10eb）修复验证：SQL 仓储参与 MaxRetries 协商，
// claim 门禁 / publisher DLQ / cleanup 三阈值同源，不再静默分叉 ——

// TestSQLOutboxRepository_MaxRetriesNegotiation 验证仓储实现
// outbox.IOutboxMaxRetriesAware 并与 core publisher 的协商语义一致：
// 未显式配置时接受下发；首次下发视为对齐，后续 publisher 采纳而不覆盖。
func TestSQLOutboxRepository_MaxRetriesNegotiation(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	// 未显式配置（构造默认值）：不报告 configured，publisher 应下发自身配置。
	maxRetries, configured := repo.ConfiguredMaxRetries()
	assert.False(t, configured)
	assert.Equal(t, outbox.DefaultMaxRetries, maxRetries)

	// 模拟 outbox.normalizeOutboxConfigForRepository 的协商分支：
	// 第一个 publisher（cfg.MaxRetries=20）发现仓储未配置 → ApplyMaxRetries(20)。
	repo.ApplyMaxRetries(20)
	maxRetries, configured = repo.ConfiguredMaxRetries()
	assert.True(t, configured)
	assert.Equal(t, 20, maxRetries)

	// 第二个 publisher（cfg.MaxRetries=7）发现已 configured → 采纳 20 而不覆盖。
	if repoMaxRetries, ok := repo.ConfiguredMaxRetries(); ok {
		assert.Equal(t, 20, repoMaxRetries)
	} else {
		t.Fatal("after ApplyMaxRetries the repository must report configured=true")
		repo.ApplyMaxRetries(7)
	}
	maxRetries, _ = repo.ConfiguredMaxRetries()
	assert.Equal(t, 20, maxRetries)
}

// TestSQLOutboxRepository_ExplicitMaxRetriesConfiguration 验证显式配置来源
// （WithConfig / WithMaxRetries）在协商中优先于 publisher 配置。
func TestSQLOutboxRepository_ExplicitMaxRetriesConfiguration(t *testing.T) {
	database := setupTestDB(t)

	// WithConfig 显式传入 cfg.MaxRetries → configured=true。
	repoCfg, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: 20})
	require.NoError(t, err)
	maxRetries, configured := repoCfg.ConfiguredMaxRetries()
	assert.True(t, configured)
	assert.Equal(t, 20, maxRetries)

	// WithMaxRetries 选项覆盖 cfg 值，仍 configured=true。
	repoOpt, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: 20}, WithMaxRetries(3))
	require.NoError(t, err)
	maxRetries, configured = repoOpt.ConfiguredMaxRetries()
	assert.True(t, configured)
	assert.Equal(t, 3, maxRetries)

	// WithMaxRetries(0) 表示“使用配置默认值”，报告生效值且 configured=true。
	repoOptZero, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{}, WithMaxRetries(0))
	require.NoError(t, err)
	maxRetries, configured = repoOptZero.ConfiguredMaxRetries()
	assert.True(t, configured)
	assert.Equal(t, outbox.DefaultMaxRetries, maxRetries)

	// NoMaxRetries 负值语义在协商面保持不变。
	repoNoMax, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: outbox.NoMaxRetries})
	require.NoError(t, err)
	maxRetries, configured = repoNoMax.ConfiguredMaxRetries()
	assert.True(t, configured)
	assert.Equal(t, outbox.NoMaxRetries, maxRetries)
}

// TestSQLOutboxRepository_ApplyMaxRetriesGatesClaim 验证 publisher 下发的阈值
// 即刻成为 claim 门禁（与 DLQ 判定同源）：retry_count 达到阈值的 failed 记录
// 不再被 claim。
func TestSQLOutboxRepository_ApplyMaxRetriesGatesClaim(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)
	repo.ApplyMaxRetries(1)

	ctx := context.Background()
	err = repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-gated", nil),
		newTestEvent(2, 1, "event-claimable", nil),
	})
	require.NoError(t, err)

	past := time.Now().Add(-time.Hour)
	_, err = database.Exec(ctx, `
		UPDATE event_outbox
		SET status = 'failed',
			retry_count = CASE event_id WHEN 'event-gated' THEN 1 ELSE 0 END,
			next_retry_at = ?
	`, past)
	require.NoError(t, err)

	entries, err := repo.ClaimPendingEntries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "event-claimable", entries[0].EventID)
}

// TestNewCleanupServiceWithRepository_AlignsCleanupThreshold 验证 cleanup 阈值
// 从仓储值派生（第三阈值与 claim/DLQ 同源）。
func TestNewCleanupServiceWithRepository_AlignsCleanupThreshold(t *testing.T) {
	database := setupTestDB(t)

	// 仓储显式配置 20 → cleanup 采用 20。
	repo, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: 20})
	require.NoError(t, err)
	svc, err := NewCleanupServiceWithRepository(database, CleanupPolicy{}, logging.NewNoopLogger(), repo)
	require.NoError(t, err)
	assert.Equal(t, 20, svc.policy.MaxRetries)

	// NoMaxRetries 负值语义传导到 cleanup（永不清理 failed/processing）。
	repoNoMax, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: outbox.NoMaxRetries})
	require.NoError(t, err)
	svc, err = NewCleanupServiceWithRepository(database, CleanupPolicy{}, logging.NewNoopLogger(), repoNoMax)
	require.NoError(t, err)
	assert.Equal(t, outbox.NoMaxRetries, svc.policy.MaxRetries)

	// 仓储未配置（尚未与 publisher 协商）→ 保留 policy 显式值。
	repoPlain, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)
	svc, err = NewCleanupServiceWithRepository(database, CleanupPolicy{MaxRetries: 8}, logging.NewNoopLogger(), repoPlain)
	require.NoError(t, err)
	assert.Equal(t, 8, svc.policy.MaxRetries)

	// 仓储未配置且 policy 未显式配置 → 默认值兜底。
	svc, err = NewCleanupServiceWithRepository(database, CleanupPolicy{}, logging.NewNoopLogger(), repoPlain)
	require.NoError(t, err)
	assert.Equal(t, outbox.DefaultMaxRetries, svc.policy.MaxRetries)

	// nil 仓储等价于 NewCleanupService。
	svc, err = NewCleanupServiceWithRepository(database, CleanupPolicy{MaxRetries: 6}, logging.NewNoopLogger(), nil)
	require.NoError(t, err)
	assert.Equal(t, 6, svc.policy.MaxRetries)
}

// TestThreeThresholds_Aligned_NoGapBetweenClaimAndCleanup 端到端钉住三阈值
// 一致：同一阈值 T 下，retry_count < T 的记录仍可 claim 且不会被 cleanup 提前
// 删除；retry_count >= T 的记录不可 claim（交由 publisher 迁 DLQ），到期后由
// cleanup 收走——不存在“claim 不走又清不掉”或“可重试却被当垃圾删除”的夹缝。
func TestThreeThresholds_Aligned_NoGapBetweenClaimAndCleanup(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepositoryWithConfig(database, &MockEventStoreWithDB{}, logging.NewNoopLogger(), outbox.OutboxConfig{MaxRetries: 2})
	require.NoError(t, err)
	// MaxRetries 留空由仓储派生；RetentionDays=1 让 48h 前的旧记录过保留期。
	cleanup, err := NewCleanupServiceWithRepository(database, CleanupPolicy{RetentionDays: 1}, logging.NewNoopLogger(), repo)
	require.NoError(t, err)
	// 阈值同源断言：cleanup 与仓储（即 claim 门禁）共享同一上限。
	assert.Equal(t, 2, cleanup.policy.MaxRetries)
	repoMax, configured := repo.ConfiguredMaxRetries()
	require.True(t, configured)
	assert.Equal(t, cleanup.policy.MaxRetries, repoMax)

	ctx := context.Background()
	err = repo.SaveWithEvents(ctx, 1, []eventing.Event[int64]{
		newTestEvent(1, 1, "event-retryable", nil),       // retry_count=1：仍可 claim
		newTestEvent(2, 1, "event-exhausted-old", nil),   // retry_count=2：不可 claim，旧记录可清理
		newTestEvent(3, 1, "event-exhausted-fresh", nil), // retry_count=2：不可 claim，但未过保留期
	})
	require.NoError(t, err)

	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	_, err = database.Exec(ctx, `
		UPDATE event_outbox
		SET status = 'failed',
			retry_count = CASE event_id WHEN 'event-retryable' THEN 1 ELSE 2 END,
			created_at = CASE event_id WHEN 'event-exhausted-fresh' THEN ? ELSE ? END,
			next_retry_at = ?
	`, fresh, old, old)
	require.NoError(t, err)

	// claim 门禁：retry_count=2 的记录不被 claim。
	entries, err := repo.ClaimPendingEntries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "event-retryable", entries[0].EventID)

	// cleanup（RetentionDays=1 覆盖 old 记录）：只清走 retry_count>=2 且过保留期的记录。
	cleanupSvc := cleanup
	result, err := cleanupSvc.Cleanup(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.DeletedCount)

	var count int64
	require.NoError(t, database.QueryRow(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&count))
	require.Equal(t, int64(2), count)

	var remaining []string
	rows, err := database.Query(ctx, `SELECT event_id FROM event_outbox ORDER BY event_id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		remaining = append(remaining, id)
	}
	require.NoError(t, rows.Err())
	// 可重试记录与未过保留期的超限记录都还在；被清走的只有“不可 claim 且过保留期”的那条。
	require.Equal(t, []string{"event-exhausted-fresh", "event-retryable"}, remaining)
}
