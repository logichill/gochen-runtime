package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	coredialect "gochen/db/dialect"
	"strings"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/safeident"
	"gochen-runtime/internal/sqlresult"
	"gochen/db"
	"gochen/errors"
	"gochen/process/workflow"
)

const (
	defaultDefinitionTable = "workflow_definitions"
	defaultInstanceTable   = "workflow_instances"
)

// Config 配置工作流 SQL 存储使用的表名。
type Config struct {
	// DefinitionTable 流程定义表名（默认：workflow_definitions）。
	DefinitionTable string

	// InstanceTable 流程实例表名（默认：workflow_instances）。
	InstanceTable string
}

// SQLStore 基于 db.IDatabase 持久化工作流定义与实例状态。
//
// 该实现同时满足 workflow.IOptimisticStore 与 workflow.IQueryableStore：
//   - 乐观并发：实例行携带 version 列，SaveIfVersion 用条件 UPDATE 做 compare-and-swap，
//     使多进程部署下的并发推进互斥，而不依赖引擎的进程内 keyed lock。
//   - 实例枚举：把"最早活动节点时刻"反范式成 oldest_active_at 列并建索引，
//     让超时巡检可以走索引有界扫描，而不必反序列化全表 JSON。
//
// 定义与实例状态本体以 JSON 存储：图结构与历史轨迹是整体读写的聚合，
// 拆列既无查询价值，又会让定义结构演进绑死 DDL 迁移。
type SQLStore struct {
	db              db.IDatabase
	dialect         coredialect.IDialect
	definitionTable string
	instanceTable   string
}

// NewSQLStore 创建基于 SQL 的工作流存储。
func NewSQLStore(database db.IDatabase, cfg *Config) (*SQLStore, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "workflow sql store database cannot be nil")
	}
	if cfg == nil {
		cfg = &Config{}
	}
	definitionTable := cfg.DefinitionTable
	if definitionTable == "" {
		definitionTable = defaultDefinitionTable
	}
	instanceTable := cfg.InstanceTable
	if instanceTable == "" {
		instanceTable = defaultInstanceTable
	}
	if err := validateTableName(definitionTable); err != nil {
		return nil, err
	}
	if err := validateTableName(instanceTable); err != nil {
		return nil, err
	}

	return &SQLStore{
		db:              database,
		dialect:         dialect.FromDatabase(database),
		definitionTable: definitionTable,
		instanceTable:   instanceTable,
	}, nil
}

func validateTableName(name string) error {
	if !safeident.IsSafeIdentifier(name) || strings.Contains(name, ".") {
		return errors.NewCode(errors.InvalidInput, "invalid workflow table name").
			WithContext("table_name", name)
	}
	return nil
}

func (s *SQLStore) quotedDefinitionTable() string {
	return s.dialect.QuoteIdentifier(s.definitionTable)
}

func (s *SQLStore) quotedInstanceTable() string {
	return s.dialect.QuoteIdentifier(s.instanceTable)
}

// GetDefinition 查询指定版本的流程定义；version == 0 时返回最新版本。
func (s *SQLStore) GetDefinition(ctx context.Context, id string, version uint32) (*workflow.Definition, error) {
	var (
		query string
		args  []any
	)
	if version == 0 {
		query = fmt.Sprintf(
			`SELECT payload FROM %s WHERE definition_id = ? ORDER BY version DESC LIMIT 1`,
			s.quotedDefinitionTable(),
		)
		args = []any{id}
	} else {
		query = fmt.Sprintf(
			`SELECT payload FROM %s WHERE definition_id = ? AND version = ?`,
			s.quotedDefinitionTable(),
		)
		args = []any{id, version}
	}

	var payload string
	err := s.db.QueryRow(ctx, s.dialect.Rebind(query), args...).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		// 契约要求"不存在返回 nil"，而不是 NotFound 错误。
		return nil, nil
	}
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "failed to load workflow definition", err).
			WithContext("definition_id", id).
			WithContext("definition_version", version)
	}

	var def workflow.Definition
	if err := json.Unmarshal([]byte(payload), &def); err != nil {
		return nil, errors.NewCodeWithCause(errors.Internal, "failed to decode workflow definition", err).
			WithContext("definition_id", id).
			WithContext("definition_version", version)
	}
	return &def, nil
}

// SaveDefinition 保存流程定义。定义版本一经写入即视为不可变，重复保存同一版本会被拒绝。
func (s *SQLStore) SaveDefinition(ctx context.Context, def *workflow.Definition) error {
	if def == nil {
		return nil
	}
	if def.Version == 0 {
		return errors.NewCode(errors.InvalidInput, "workflow definition version must be assigned before persisting").
			WithContext("definition_id", def.ID)
	}

	payload, err := json.Marshal(def)
	if err != nil {
		return errors.NewCodeWithCause(errors.Internal, "failed to encode workflow definition", err).
			WithContext("definition_id", def.ID)
	}

	query := fmt.Sprintf(
		`INSERT INTO %s (definition_id, version, name, start_node_id, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.quotedDefinitionTable(),
	)
	_, err = s.db.Exec(ctx, s.dialect.Rebind(query),
		def.ID, def.Version, def.Name, def.StartNodeID, string(payload), def.CreatedAt, def.UpdatedAt)
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to save workflow definition", err).
			WithContext("definition_id", def.ID).
			WithContext("definition_version", def.Version)
	}
	return nil
}

// DeleteDefinition 删除指定版本（version == 0 删除所有版本）的流程定义。
func (s *SQLStore) DeleteDefinition(ctx context.Context, id string, version uint32) error {
	var (
		query string
		args  []any
	)
	if version == 0 {
		query = fmt.Sprintf(`DELETE FROM %s WHERE definition_id = ?`, s.quotedDefinitionTable())
		args = []any{id}
	} else {
		query = fmt.Sprintf(`DELETE FROM %s WHERE definition_id = ? AND version = ?`, s.quotedDefinitionTable())
		args = []any{id, version}
	}

	if _, err := s.db.Exec(ctx, s.dialect.Rebind(query), args...); err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to delete workflow definition", err).
			WithContext("definition_id", id).
			WithContext("definition_version", version)
	}
	return nil
}

// Get 查询流程实例状态；不存在时返回 nil。
func (s *SQLStore) Get(ctx context.Context, id workflow.ID) (*workflow.State, error) {
	query := fmt.Sprintf(`SELECT payload, version FROM %s WHERE instance_id = ?`, s.quotedInstanceTable())

	var (
		payload string
		version uint64
	)
	err := s.db.QueryRow(ctx, s.dialect.Rebind(query), string(id)).Scan(&payload, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "failed to load workflow instance", err).
			WithContext("instance_id", string(id))
	}

	st, appErr := decodeState(payload)
	if appErr != nil {
		return nil, appErr.WithContext("instance_id", string(id))
	}
	// version 列是并发控制的权威来源，覆盖 JSON 里的历史值。
	st.Version = version
	return st, nil
}

// Save 无条件保存或覆盖流程实例状态。
//
// 仅适用于单进程场景；多实例部署应使用 SaveIfVersion 走乐观并发路径。
func (s *SQLStore) Save(ctx context.Context, st *workflow.State) error {
	if st == nil {
		return nil
	}
	if st.Version == 0 {
		st.Version = 1
	}

	payload, appErr := encodeState(st)
	if appErr != nil {
		return appErr
	}

	query, args, appErr := s.upsertInstanceQuery(st, payload)
	if appErr != nil {
		return appErr
	}
	if _, err := s.db.Exec(ctx, s.dialect.Rebind(query), args...); err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to save workflow instance", err).
			WithContext("instance_id", string(st.ID))
	}
	return nil
}

// SaveIfVersion 在当前版本等于 expectedVersion 时保存状态，并递增版本。
//
// expectedVersion == 0 表示"必须是首次写入"，用 INSERT 让数据库主键冲突充当去重闸门；
// 其余情况用条件 UPDATE 做 compare-and-swap，未命中时再区分 NotFound 与版本冲突。
func (s *SQLStore) SaveIfVersion(ctx context.Context, st *workflow.State, expectedVersion uint64) error {
	if st == nil {
		return nil
	}

	if expectedVersion == 0 {
		return s.insertNewInstance(ctx, st)
	}

	nextVersion := expectedVersion + 1
	payload, appErr := encodeStateWithVersion(st, nextVersion)
	if appErr != nil {
		return appErr
	}

	query := fmt.Sprintf(
		`UPDATE %s SET payload = ?, version = ?, definition_id = ?, definition_version = ?,
		 status = ?, oldest_active_at = ?, updated_at = ?
		 WHERE instance_id = ? AND version = ?`,
		s.quotedInstanceTable(),
	)
	result, err := s.db.Exec(ctx, s.dialect.Rebind(query),
		payload, nextVersion, st.DefinitionID, st.DefinitionVersion,
		string(st.Status), nullableTime(oldestActiveAt(st)), st.UpdatedAt,
		string(st.ID), expectedVersion)
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to save workflow instance", err).
			WithContext("instance_id", string(st.ID))
	}

	affected, appErr := sqlresult.ReadRowsAffected(result,
		"workflow instance save returned no result",
		"failed to read workflow instance save result")
	if appErr != nil {
		return appErr.WithContext("instance_id", string(st.ID))
	}
	if affected == 0 {
		return s.explainMissedUpdate(ctx, st.ID, expectedVersion)
	}

	st.Version = nextVersion
	return nil
}

func (s *SQLStore) insertNewInstance(ctx context.Context, st *workflow.State) error {
	payload, appErr := encodeStateWithVersion(st, 1)
	if appErr != nil {
		return appErr
	}

	query := fmt.Sprintf(
		`INSERT INTO %s (instance_id, definition_id, definition_version, version, status,
		 oldest_active_at, created_at, updated_at, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.quotedInstanceTable(),
	)
	_, err := s.db.Exec(ctx, s.dialect.Rebind(query),
		string(st.ID), st.DefinitionID, st.DefinitionVersion, 1, string(st.Status),
		nullableTime(oldestActiveAt(st)), st.CreatedAt, st.UpdatedAt, payload)
	if err != nil {
		// 主键冲突即"实例已存在"，归一化为 Conflict，与 MemoryStore 语义保持一致。
		if exists, existsErr := s.instanceExists(ctx, st.ID); existsErr == nil && exists {
			return errors.NewCode(errors.Conflict, "workflow instance already exists").
				WithContext("instance_id", string(st.ID))
		}
		return errors.NewCodeWithCause(errors.Database, "failed to create workflow instance", err).
			WithContext("instance_id", string(st.ID))
	}

	st.Version = 1
	return nil
}

// explainMissedUpdate 在条件 UPDATE 未命中时区分"实例不存在"与"版本冲突"。
func (s *SQLStore) explainMissedUpdate(ctx context.Context, id workflow.ID, expectedVersion uint64) error {
	query := fmt.Sprintf(`SELECT version FROM %s WHERE instance_id = ?`, s.quotedInstanceTable())

	var actual uint64
	err := s.db.QueryRow(ctx, s.dialect.Rebind(query), string(id)).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.NewCode(errors.NotFound, "workflow instance not found").
			WithContext("instance_id", string(id))
	}
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to inspect workflow instance version", err).
			WithContext("instance_id", string(id))
	}
	return errors.NewCode(errors.Conflict, "workflow instance version conflict").
		WithContext("instance_id", string(id)).
		WithContext("expected_version", expectedVersion).
		WithContext("actual_version", actual)
}

func (s *SQLStore) instanceExists(ctx context.Context, id workflow.ID) (bool, error) {
	query := fmt.Sprintf(`SELECT 1 FROM %s WHERE instance_id = ?`, s.quotedInstanceTable())

	var probe int
	err := s.db.QueryRow(ctx, s.dialect.Rebind(query), string(id)).Scan(&probe)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete 删除流程实例状态。
func (s *SQLStore) Delete(ctx context.Context, id workflow.ID) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE instance_id = ?`, s.quotedInstanceTable())
	if _, err := s.db.Exec(ctx, s.dialect.Rebind(query), string(id)); err != nil {
		return errors.NewCodeWithCause(errors.Database, "failed to delete workflow instance", err).
			WithContext("instance_id", string(id))
	}
	return nil
}

// ListInstances 按查询条件枚举实例，按最早活动时刻升序返回。
func (s *SQLStore) ListInstances(ctx context.Context, query workflow.InstanceQuery) ([]*workflow.State, error) {
	if query.Limit <= 0 {
		return nil, errors.NewCode(errors.InvalidInput, "workflow instance query limit must be positive")
	}

	var (
		conditions []string
		args       []any
	)
	if query.DefinitionID != "" {
		conditions = append(conditions, "definition_id = ?")
		args = append(args, query.DefinitionID)
	}
	if len(query.Statuses) > 0 {
		placeholders := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		conditions = append(conditions, "status IN ("+strings.Join(placeholders, ", ")+")")
	}
	if !query.ActiveSince.IsZero() {
		conditions = append(conditions, "oldest_active_at IS NOT NULL AND oldest_active_at <= ?")
		args = append(args, query.ActiveSince)
	}

	statement := fmt.Sprintf(`SELECT payload, version FROM %s`, s.quotedInstanceTable())
	if len(conditions) > 0 {
		statement += " WHERE " + strings.Join(conditions, " AND ")
	}
	// NULL 排在最后：没有活动节点的实例最不可能超时，不该挤占有界扫描的额度。
	statement += " ORDER BY oldest_active_at IS NULL, oldest_active_at ASC, instance_id ASC LIMIT ?"
	args = append(args, query.Limit)

	rows, err := s.db.Query(ctx, s.dialect.Rebind(statement), args...)
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "failed to list workflow instances", err).
			WithContext("table_name", s.instanceTable)
	}
	defer func() { _ = rows.Close() }()

	var out []*workflow.State
	for rows.Next() {
		var (
			payload string
			version uint64
		)
		if err := rows.Scan(&payload, &version); err != nil {
			return nil, errors.NewCodeWithCause(errors.Database, "failed to scan workflow instance", err).
				WithContext("table_name", s.instanceTable)
		}
		st, appErr := decodeState(payload)
		if appErr != nil {
			return nil, appErr
		}
		st.Version = version
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "failed to iterate workflow instances", err).
			WithContext("table_name", s.instanceTable)
	}
	return out, nil
}

func (s *SQLStore) upsertInstanceQuery(st *workflow.State, payload string) (string, []any, errors.IAppError) {
	args := []any{
		string(st.ID), st.DefinitionID, st.DefinitionVersion, st.Version, string(st.Status),
		nullableTime(oldestActiveAt(st)), st.CreatedAt, st.UpdatedAt, payload,
	}
	table := s.quotedInstanceTable()

	switch s.dialect.Name() {
	case coredialect.NameSQLite, coredialect.NamePostgres:
		return fmt.Sprintf(
			`INSERT INTO %s (instance_id, definition_id, definition_version, version, status,
			 oldest_active_at, created_at, updated_at, payload)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (instance_id) DO UPDATE SET
			 	definition_id = excluded.definition_id,
			 	definition_version = excluded.definition_version,
			 	version = excluded.version,
			 	status = excluded.status,
			 	oldest_active_at = excluded.oldest_active_at,
			 	updated_at = excluded.updated_at,
			 	payload = excluded.payload`, table), args, nil
	case coredialect.NameMySQL:
		return fmt.Sprintf(
			`INSERT INTO %s (instance_id, definition_id, definition_version, version, status,
			 oldest_active_at, created_at, updated_at, payload)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE
			 	definition_id = VALUES(definition_id),
			 	definition_version = VALUES(definition_version),
			 	version = VALUES(version),
			 	status = VALUES(status),
			 	oldest_active_at = VALUES(oldest_active_at),
			 	updated_at = VALUES(updated_at),
			 	payload = VALUES(payload)`, table), args, nil
	default:
		return "", nil, unsupportedDialectError(s.dialect)
	}
}

func encodeState(st *workflow.State) (string, errors.IAppError) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", errors.NewCodeWithCause(errors.Internal, "failed to encode workflow instance", err).
			WithContext("instance_id", string(st.ID))
	}
	return string(payload), nil
}

// encodeStateWithVersion 以目标版本号序列化状态，避免 JSON 里的 version 与 version 列不一致。
func encodeStateWithVersion(st *workflow.State, version uint64) (string, errors.IAppError) {
	snapshot := *st
	snapshot.Version = version
	return encodeState(&snapshot)
}

func decodeState(payload string) (*workflow.State, errors.IAppError) {
	var st workflow.State
	if err := json.Unmarshal([]byte(payload), &st); err != nil {
		return nil, errors.NewCodeWithCause(errors.Internal, "failed to decode workflow instance", err)
	}
	return &st, nil
}

// oldestActiveAt 返回实例中最早的活动节点激活时刻，用于反范式出可索引的超时扫描列。
func oldestActiveAt(st *workflow.State) time.Time {
	var oldest time.Time
	for _, nodeID := range st.ActiveNodeIDs {
		startedAt, ok := st.ActiveNodeStartedAt[nodeID]
		if !ok || startedAt.IsZero() {
			continue
		}
		if oldest.IsZero() || startedAt.Before(oldest) {
			oldest = startedAt
		}
	}
	return oldest
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func unsupportedDialectError(d coredialect.IDialect) errors.IAppError {
	return errors.NewCode(errors.Unsupported, "workflow sql store requires a known SQL dialect").
		WithContext("dialect", string(d.Name()))
}

var _ workflow.IOptimisticStore = (*SQLStore)(nil)
var _ workflow.IQueryableStore = (*SQLStore)(nil)
