package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gochen-runtime/db/schema"
	"gochen-runtime/db/sql/safeident"
	"gochen-runtime/db/sql/sqlbuilder"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/errors"
	"gochen/eventing/projection"
)

// SQLCheckpointStore 使用现有数据库抽象持久化投影检查点。
type SQLCheckpointStore struct {
	db        db.IDatabase
	tableName string
	dialect   dialect.IDialect
}

// NewSQLCheckpointStore 创建一个基于 SQL 的检查点存储实现。
func NewSQLCheckpointStore(db db.IDatabase, tableName string) *SQLCheckpointStore {
	if tableName == "" {
		tableName = "projection_checkpoints"
	}

	return &SQLCheckpointStore{
		db:        db,
		tableName: tableName,
		dialect:   dialect.FromDatabase(db),
	}
}

// RequiresORMTxSession 声明 SQL checkpoint store 需要复用 ctx 中的 ORM 事务 session。
func (s *SQLCheckpointStore) RequiresORMTxSession() bool { return true }

// Load 读取指定投影最近一次保存的检查点。
func (s *SQLCheckpointStore) Load(ctx context.Context, projectionName string) (*projection.Checkpoint, error) {
	if err := s.validateTableName(); err != nil {
		return nil, err
	}

	sq, err := sqlbuilder.New(s.db)
	if err != nil {
		return nil, errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	row := sq.Select(
		"projection_name", "position", "last_event_id", "last_event_time", "updated_at",
	).From(s.tableName).
		Where("projection_name = ?", projectionName).
		QueryRow(ctx)

	var checkpoint projection.Checkpoint
	var lastEventTime sql.NullTime

	err = row.Scan(
		&checkpoint.ProjectionName,
		&checkpoint.Position,
		&checkpoint.LastEventID,
		&lastEventTime,
		&checkpoint.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, errors.NewCode(errors.NotFound, "checkpoint not found").
			WithContext("projection_name", projectionName)
	}
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("projection_name", projectionName)
	}

	if lastEventTime.Valid {
		checkpoint.LastEventTime = lastEventTime.Time
	}

	return &checkpoint, nil
}

// Save 保存一条检查点记录；已存在时只允许位置前进。
func (s *SQLCheckpointStore) Save(ctx context.Context, checkpoint *projection.Checkpoint) error {
	if err := s.validateORMTxSession(ctx); err != nil {
		return err
	}
	return s.SaveWithDB(ctx, s.databaseFor(ctx), checkpoint)
}

// SaveWithDB 使用给定数据库句柄保存检查点；可显式传入事务句柄参与同一提交边界。
func (s *SQLCheckpointStore) SaveWithDB(ctx context.Context, database db.IDatabase, checkpoint *projection.Checkpoint) error {
	if checkpoint == nil {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("reason", "checkpoint is nil")
	}
	if !checkpoint.IsValid() {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("projection_name", checkpoint.ProjectionName).
			WithContext("position", checkpoint.Position)
	}
	if err := s.validateTableName(); err != nil {
		return err
	}

	// 更新 UpdatedAt
	checkpoint.UpdatedAt = time.Now()

	if database == nil {
		database = s.db
	}
	if database == nil {
		return errors.NewCode(errors.InvalidInput, "checkpoint store database cannot be nil")
	}
	query, args, err := s.buildForwardSaveQuery(checkpoint)
	if err != nil {
		return err
	}
	if _, err := database.Exec(ctx, query, args...); err != nil {
		return errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("projection_name", checkpoint.ProjectionName)
	}
	return nil
}

// ForceSave 无条件将检查点写为给定位置（即使低于已持久化位置），用于重建对齐游标。
func (s *SQLCheckpointStore) ForceSave(ctx context.Context, checkpoint *projection.Checkpoint) error {
	if err := s.validateORMTxSession(ctx); err != nil {
		return err
	}
	return s.forceSaveWithDB(ctx, s.databaseFor(ctx), checkpoint)
}

// forceSaveWithDB 使用给定数据库句柄无条件覆盖检查点；可显式传入事务句柄参与同一提交边界。
func (s *SQLCheckpointStore) forceSaveWithDB(ctx context.Context, database db.IDatabase, checkpoint *projection.Checkpoint) error {
	if checkpoint == nil {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("reason", "checkpoint is nil")
	}
	if !checkpoint.IsValid() {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("projection_name", checkpoint.ProjectionName).
			WithContext("position", checkpoint.Position)
	}
	if err := s.validateTableName(); err != nil {
		return err
	}

	checkpoint.UpdatedAt = time.Now()

	if database == nil {
		database = s.db
	}
	if database == nil {
		return errors.NewCode(errors.InvalidInput, "checkpoint store database cannot be nil")
	}
	query, args, err := s.buildForceSaveQuery(checkpoint)
	if err != nil {
		return err
	}
	if _, err := database.Exec(ctx, query, args...); err != nil {
		return errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("projection_name", checkpoint.ProjectionName)
	}
	return nil
}

func (s *SQLCheckpointStore) buildForwardSaveQuery(checkpoint *projection.Checkpoint) (string, []any, error) {
	baseArgs := []any{
		checkpoint.ProjectionName,
		checkpoint.Position,
		checkpoint.LastEventID,
		checkpoint.LastEventTime,
		checkpoint.UpdatedAt,
	}
	quotedTable := s.dialect.QuoteIdentifier(s.tableName)
	switch s.dialect.Name() {
	case dialect.NameSQLite, dialect.NamePostgres:
		return fmt.Sprintf(`
			INSERT INTO %s (projection_name, position, last_event_id, last_event_time, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (projection_name) DO UPDATE SET
				position = excluded.position,
				last_event_id = excluded.last_event_id,
				last_event_time = excluded.last_event_time,
				updated_at = excluded.updated_at
			WHERE excluded.position > %s.position
		`, quotedTable, quotedTable), baseArgs, nil
	case dialect.NameMySQL:
		return fmt.Sprintf(`
			INSERT INTO %s (projection_name, position, last_event_id, last_event_time, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				last_event_id = IF(? > position, ?, last_event_id),
				last_event_time = IF(? > position, ?, last_event_time),
				updated_at = IF(? > position, ?, updated_at),
				position = IF(? > position, ?, position)
		`, quotedTable), append(baseArgs,
				checkpoint.Position, checkpoint.LastEventID,
				checkpoint.Position, checkpoint.LastEventTime,
				checkpoint.Position, checkpoint.UpdatedAt,
				checkpoint.Position, checkpoint.Position,
			), nil
	default:
		return "", nil, unsupportedCheckpointDialectError(s.dialect)
	}
}

func (s *SQLCheckpointStore) buildForceSaveQuery(checkpoint *projection.Checkpoint) (string, []any, error) {
	args := []any{
		checkpoint.ProjectionName,
		checkpoint.Position,
		checkpoint.LastEventID,
		checkpoint.LastEventTime,
		checkpoint.UpdatedAt,
	}
	quotedTable := s.dialect.QuoteIdentifier(s.tableName)
	switch s.dialect.Name() {
	case dialect.NameSQLite, dialect.NamePostgres:
		return fmt.Sprintf(`
			INSERT INTO %s (projection_name, position, last_event_id, last_event_time, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (projection_name) DO UPDATE SET
				position = excluded.position,
				last_event_id = excluded.last_event_id,
				last_event_time = excluded.last_event_time,
				updated_at = excluded.updated_at
		`, quotedTable), args, nil
	case dialect.NameMySQL:
		return fmt.Sprintf(`
			INSERT INTO %s (projection_name, position, last_event_id, last_event_time, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				position = ?,
				last_event_id = ?,
				last_event_time = ?,
				updated_at = ?
		`, quotedTable), append(args,
				checkpoint.Position,
				checkpoint.LastEventID,
				checkpoint.LastEventTime,
				checkpoint.UpdatedAt,
			), nil
	default:
		return "", nil, unsupportedCheckpointDialectError(s.dialect)
	}
}

// Delete 删除指定投影对应的检查点记录。
func (s *SQLCheckpointStore) Delete(ctx context.Context, projectionName string) error {
	if err := s.validateTableName(); err != nil {
		return err
	}

	sq, err := sqlbuilder.New(s.db)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	_, err = sq.DeleteFrom(s.tableName).
		Where("projection_name = ?", projectionName).
		Exec(ctx)
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("projection_name", projectionName)
	}

	return nil
}

// CreateTable 创建检查点表及必要索引。
func (s *SQLCheckpointStore) CreateTable(ctx context.Context) error {
	if err := s.validateTableName(); err != nil {
		return err
	}

	quotedTable := s.dialect.QuoteIdentifier(s.tableName)

	switch s.dialect.Name() {
	case dialect.NameSQLite:
		indexName, indexTableName := sqliteCheckpointUpdatedAtIndexTarget(s.tableName)
		createTable := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				projection_name TEXT PRIMARY KEY,
				position INTEGER NOT NULL DEFAULT 0,
				last_event_id TEXT NOT NULL DEFAULT '',
				last_event_time DATETIME NULL,
				updated_at DATETIME NOT NULL
			)`, quotedTable)
		createIndex := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s(updated_at)`, indexName, indexTableName)

		if _, err := s.db.Exec(ctx, createTable); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create checkpoint table", err).
				WithContext("table_name", s.tableName)
		}
		if _, err := s.db.Exec(ctx, createIndex); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create checkpoint index", err).
				WithContext("table_name", s.tableName)
		}
		return nil

	case dialect.NamePostgres:
		createTable := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				projection_name VARCHAR(255) PRIMARY KEY,
				position BIGINT NOT NULL DEFAULT 0,
				last_event_id VARCHAR(255) NOT NULL DEFAULT '',
				last_event_time TIMESTAMPTZ NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`, quotedTable)
		createIndex := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_projection_checkpoints_updated_at ON %s(updated_at)`, quotedTable)

		if _, err := s.db.Exec(ctx, createTable); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create checkpoint table", err).
				WithContext("table_name", s.tableName)
		}
		if _, err := s.db.Exec(ctx, createIndex); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create checkpoint index", err).
				WithContext("table_name", s.tableName)
		}
		return nil

	case dialect.NameMySQL:
		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				projection_name VARCHAR(255) PRIMARY KEY,
				position BIGINT NOT NULL DEFAULT 0,
				last_event_id VARCHAR(255) NOT NULL DEFAULT '',
				last_event_time DATETIME NULL,
				updated_at DATETIME NOT NULL,
				INDEX idx_projection_checkpoints_updated_at (updated_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
		`, quotedTable)

		if _, err := s.db.Exec(ctx, query); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create checkpoint table", err).
				WithContext("table_name", s.tableName)
		}
		return nil

	default:
		return unsupportedCheckpointDialectError(s.dialect)
	}
}

func unsupportedCheckpointDialectError(d dialect.IDialect) error {
	return errors.NewCode(errors.Unsupported, "checkpoint store requires a known SQL dialect").
		WithContext("dialect", string(d.Name()))
}

func sqliteCheckpointUpdatedAtIndexTarget(tableName string) (indexName string, indexTableName string) {
	schemaName, tableName := schema.ParseTableName(tableName)
	indexName = "idx_projection_checkpoints_updated_at"
	if schemaName != "" {
		indexName = schemaName + "." + indexName
	}
	return indexName, tableName
}

func (s *SQLCheckpointStore) databaseFor(ctx context.Context) db.IDatabase {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		if database := session.Database(); database != nil {
			return database
		}
	}
	return s.db
}

func (s *SQLCheckpointStore) validateORMTxSession(ctx context.Context) error {
	session, ok := orm.SessionFromContext(ctx)
	if !ok || session == nil {
		return errors.NewCode(errors.Unsupported, "sql checkpoint store requires an orm transaction session in context")
	}
	if session.Database() == nil {
		return errors.NewCode(errors.Unsupported, "sql checkpoint transaction session has no database handle")
	}
	return nil
}

// List 列出当前表中的全部检查点，主要用于调试和监控。
func (s *SQLCheckpointStore) List(ctx context.Context) ([]*projection.Checkpoint, error) {
	if err := s.validateTableName(); err != nil {
		return nil, err
	}

	sq, err := sqlbuilder.New(s.db)
	if err != nil {
		return nil, errors.Wrap(err, errors.Internal, "failed to create sql builder")
	}

	builder := sq.Select(
		"projection_name", "position", "last_event_id", "last_event_time", "updated_at",
	).From(s.tableName).
		OrderBy(sqlbuilder.OrderAsc("projection_name"))

	rows, err := builder.Query(ctx)
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("table_name", s.tableName)
	}
	defer rows.Close()

	var checkpoints []*projection.Checkpoint
	for rows.Next() {
		var checkpoint projection.Checkpoint
		var lastEventTime sql.NullTime

		err := rows.Scan(
			&checkpoint.ProjectionName,
			&checkpoint.Position,
			&checkpoint.LastEventID,
			&lastEventTime,
			&checkpoint.UpdatedAt,
		)
		if err != nil {
			return nil, errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
				WithContext("table_name", s.tableName)
		}

		if lastEventTime.Valid {
			checkpoint.LastEventTime = lastEventTime.Time
		}

		checkpoints = append(checkpoints, &checkpoint)
	}

	if err = rows.Err(); err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "checkpoint store failed", err).
			WithContext("table_name", s.tableName)
	}

	return checkpoints, nil
}

// SaveBatch 在一个事务里批量保存多条检查点记录。
func (s *SQLCheckpointStore) SaveBatch(ctx context.Context, checkpoints []*projection.Checkpoint) error {
	if len(checkpoints) == 0 {
		return nil
	}

	if err := s.validateTableName(); err != nil {
		return err
	}
	for idx, cp := range checkpoints {
		if cp == nil {
			return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
				WithContext("index", idx).
				WithContext("reason", "checkpoint is nil")
		}
		if !cp.IsValid() {
			return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
				WithContext("index", idx).
				WithContext("projection_name", cp.ProjectionName).
				WithContext("position", cp.Position)
		}
	}

	// 开启事务，复用 SaveWithDB 的只前进语义。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to begin transaction", err).
			WithContext("table_name", s.tableName)
	}
	defer func() { _ = tx.Rollback() }()

	for _, cp := range checkpoints {
		if err := s.SaveWithDB(ctx, tx, cp); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to commit transaction", err).
			WithContext("table_name", s.tableName)
	}

	return nil
}

func (s *SQLCheckpointStore) validateTableName() error {
	if s == nil {
		return errors.NewCode(errors.InvalidInput, "checkpoint store cannot be nil")
	}
	if s.db == nil {
		return errors.NewCode(errors.InvalidInput, "checkpoint store database cannot be nil")
	}
	if err := validateCheckpointTableName(s.tableName); err != nil {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint table name").
			WithContext("table_name", s.tableName).
			WithContext("cause", err.Error())
	}
	return nil
}

// validateCheckpointTableName 校验检查点表名，防止通过配置注入恶意 SQL 片段。
//
// 说明：
// - 约束：只允许 table 或 schema.table，且每一段都必须是安全 SQL 标识符。
func validateCheckpointTableName(name string) error {
	if err := safeident.ValidateQualifiedIdentifier(name, "table name", 2); err != nil {
		return errors.NewCode(errors.InvalidInput, "invalid table name").WithContext("cause", err.Error())
	}
	return nil
}

// Ensure SQLCheckpointStore implements projection checkpoint contracts.
var _ projection.ICheckpointStore = (*SQLCheckpointStore)(nil)
var _ projection.ICheckpointForceSaver = (*SQLCheckpointStore)(nil)
