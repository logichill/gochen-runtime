package sqlstore

import (
	coredialect "gochen/db/dialect"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/internal/sqlsafe"
	"gochen/db"
	"gochen/errors"

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
}

const defaultCleanupBatchSize = 1000

// DefaultCleanupPolicy 返回一组适合日常运行的默认 Outbox 清理策略。
func DefaultCleanupPolicy() CleanupPolicy {
	return CleanupPolicy{
		RetentionDays:  7,
		BatchSize:      defaultCleanupBatchSize,
		ArchiveEnabled: false,
		OutboxTable:    DefaultOutboxTable,
		ArchiveTable:   defaultArchiveTable,
		DryRun:         false,
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

// validateTableName 校验 SQL 表名是否安全，避免拼接 SQL 时引入注入风险。
func validateTableName(name string) error {
	return sqlsafe.ValidateTableName(name)
}

func (s *CleanupService) outboxTable() string {
	return s.dialect.QuoteIdentifier(s.policy.OutboxTable)
}
