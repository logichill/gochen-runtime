package sqlstore

import (
	coredialect "gochen/db/dialect"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/internal/sqlsafe"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"

	"gochen/observe/logging"
)

const defaultArchiveTable = "event_outbox_archive"

// CleanupPolicy 清理策略配置。
//
// 定义如何清理已发布的 Outbox 记录。
type CleanupPolicy struct {
	// RetentionDays 保留已发布记录的天数
	//
	// 超过此天数的已发布记录将被删除或归档。
	// 默认：7 天
	RetentionDays int `json:"retention_days"`

	// BatchSize 每次清理的批次大小
	//
	// 避免一次性删除大量数据导致数据库压力。
	// 默认：1000
	BatchSize int `json:"batch_size"`

	// ArchiveEnabled 是否启用归档
	//
	// 如果启用，记录将被移动到归档表而非删除。
	// 默认：false
	ArchiveEnabled bool `json:"archive_enabled"`

	// OutboxTable Outbox 主表名。
	//
	// 默认："event_outbox"
	OutboxTable string `json:"outbox_table"`

	// ArchiveTable 归档表名
	//
	// 仅在 ArchiveEnabled 为 true 时有效。
	// 默认："event_outbox_archive"
	ArchiveTable string `json:"archive_table"`

	// DryRun 试运行模式
	//
	// 如果为 true，只统计不实际删除。
	// 默认：false
	DryRun bool `json:"dry_run"`

	// MaxRetries 判定重试超限的阈值。
	//
	// 0 表示未显式配置，构造时回退为 DefaultMaxRetries；
	// > 0 时，重试次数达到或超过该阈值且早于保留期的 failed/processing 记录，
	// 也会被纳入归档或清理范围，防止无 DLQ 时主表永久堆积死信；
	// < 0（outbox.NoMaxRetries）表示永不清理 failed/processing 记录。
	//
	// 该阈值必须与仓储 claim 门禁、publisher DLQ 判定同源（同一 OutboxConfig
	// 或经 outbox.IOutboxMaxRetriesAware 协商后的仓储值），否则分叉时记录会在
	// 仓储侧停止 claim、publisher 侧不迁 DLQ，最后被 cleanup 当作普通垃圾
	// 静默删除且无 DLQ 痕迹。推荐用 NewCleanupServiceWithRepository 从仓储
	// 值派生。
	MaxRetries int `json:"max_retries"`
}

const defaultCleanupBatchSize = 1000

// maxArchiveBatchSize 是归档单批选行数的硬上限。
//
// archiveBatch 会对同一批 id 连续执行多条 `IN (?, ?, ...)` 查询（归档插入、
// 回查已归档 id、按 id 删除），每条查询的绑定参数个数都等于本批行数。
// 若 BatchSize 超过底层驱动的参数上限（sqlsafe.DefaultInsertMaxParams），
// 这些查询会直接报错或退化为全表扫描。此处把有效批次收敛到安全上限，
// 使默认值 1000 与用户自定义的大批次都不会踩线。
const maxArchiveBatchSize = sqlsafe.DefaultInsertMaxParams

// DefaultCleanupPolicy 返回一组适合日常运行的默认 Outbox 清理策略。
func DefaultCleanupPolicy() CleanupPolicy {
	return CleanupPolicy{
		RetentionDays:  7,
		BatchSize:      defaultCleanupBatchSize,
		ArchiveEnabled: false,
		OutboxTable:    DefaultOutboxTable,
		ArchiveTable:   defaultArchiveTable,
		DryRun:         false,
		MaxRetries:     outbox.DefaultMaxRetries,
	}
}

// CleanupService 负责批量删除或归档已发布的 Outbox 记录。
type CleanupService struct {
	db      db.IDatabase
	dialect coredialect.IDialect
	policy  CleanupPolicy
	log     logging.ILogger
}

// CleanupResult 表示一次清理任务的执行结果。
type CleanupResult struct {
	// DeletedCount 删除的记录数
	DeletedCount int64

	// ArchivedCount 归档的记录数
	ArchivedCount int64

	// Duration 清理耗时
	Duration time.Duration

	// Error 错误信息（如果有）
	Error error
}

// NewCleanupService 创建一个 Outbox 清理服务，并完成策略归一化与校验。
func NewCleanupService(
	db db.IDatabase,
	policy CleanupPolicy,
	logger logging.ILogger,
) (*CleanupService, error) {
	if logger == nil {
		logger = logging.ComponentLogger("eventing.outbox.cleanup")
	}
	if db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "cleanup database cannot be nil")
	}

	// 设置默认值
	if policy.RetentionDays <= 0 {
		policy.RetentionDays = 7
	}
	if policy.BatchSize <= 0 {
		policy.BatchSize = defaultCleanupBatchSize
	}
	// MaxRetries：0 视为未配置回退默认值（与 outbox 配置约定一致）；
	// 显式禁用需传负值（outbox.NoMaxRetries）。
	if policy.MaxRetries == 0 {
		policy.MaxRetries = outbox.DefaultMaxRetries
	}
	if policy.OutboxTable == "" {
		policy.OutboxTable = DefaultOutboxTable
	}
	if policy.ArchiveTable == "" {
		policy.ArchiveTable = defaultArchiveTable
	}
	if err := validateTableName(policy.OutboxTable); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid outbox table name").
			WithContext("table", policy.OutboxTable)
	}

	// 验证归档表名，防止 SQL 注入
	if policy.ArchiveEnabled {
		if err := validateTableName(policy.ArchiveTable); err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "invalid archive table name").
				WithContext("table", policy.ArchiveTable)
		}
	}

	return &CleanupService{
		db:      db,
		dialect: dialect.FromDatabase(db),
		policy:  policy,
		log:     logger,
	}, nil
}

// NewCleanupServiceWithRepository 创建与指定 Outbox 仓储共享 MaxRetries 阈值的清理服务。
//
// 仓储的 claim 门禁与 publisher 的 DLQ 判定通过 outbox.IOutboxMaxRetriesAware
// 协商共享同一上限；cleanup 若使用独立配置，三阈值分叉时“达到上限的记录”
// 会卡死在主表并被本服务当作普通垃圾删除、无 DLQ 痕迹。这里从仓储已配置/
// 已对齐的值派生 policy.MaxRetries（含 outbox.NoMaxRetries 负值语义），保证
// 清理阈值与 claim/DLQ 同源。仓储未显式配置且尚未与 publisher 协商时保持
// policy 原值——接线方应保证仓储、publisher、cleanup 使用同一 OutboxConfig，
// 且在 publisher 构造（协商发生点）之后创建本服务。
func NewCleanupServiceWithRepository(
	db db.IDatabase,
	policy CleanupPolicy,
	logger logging.ILogger,
	repo outbox.IOutboxMaxRetriesAware,
) (*CleanupService, error) {
	if repo != nil {
		if maxRetries, configured := repo.ConfiguredMaxRetries(); configured {
			policy.MaxRetries = maxRetries
		}
	}
	return NewCleanupService(db, policy, logger)
}

// effectiveArchiveBatchSize 返回受参数上限约束后的归档批次大小。
func (s *CleanupService) effectiveArchiveBatchSize() int {
	size := s.policy.BatchSize
	if size <= 0 {
		size = defaultCleanupBatchSize
	}
	if size > maxArchiveBatchSize {
		return maxArchiveBatchSize
	}
	return size
}
func validateTableName(name string) error {
	return sqlsafe.ValidateTableName(name)
}

func (s *CleanupService) outboxTable() string {
	return s.dialect.QuoteIdentifier(s.policy.OutboxTable)
}

// buildEligibleCondition 生成清理候选行的 4 路 OR 条件。
//
// 索引契约：published 分支依赖部署方主表上的 (status, published_at) 覆盖索引；
// 参考示例 schema（examples/infra/outbox/sql/internal/schema.go）仅有
// (status, created_at, id) 等三个索引，published 存量大时清理批会退化为
// status 前缀扫描。部署指南/DDL 应补充：
//
//	CREATE INDEX idx_event_outbox_published ON event_outbox (status, published_at)
func (s *CleanupService) buildEligibleCondition(olderThan time.Time) (string, []any) {
	if s.policy.MaxRetries > 0 {
		return "((status = ? AND published_at < ?) OR (status = ? AND created_at < ?) OR (status = ? AND retry_count >= ? AND created_at < ?) OR (status = ? AND (lease_until IS NULL OR lease_until < ?) AND retry_count >= ? AND created_at < ?))",
			[]any{
				outbox.OutboxStatusPublished, olderThan,
				outbox.OutboxStatusDeadLettered, olderThan,
				outbox.OutboxStatusFailed, s.policy.MaxRetries, olderThan,
				outbox.OutboxStatusProcessing, olderThan, s.policy.MaxRetries, olderThan,
			}
	}
	return "((status = ? AND published_at < ?) OR (status = ? AND created_at < ?))",
		[]any{outbox.OutboxStatusPublished, olderThan, outbox.OutboxStatusDeadLettered, olderThan}
}
