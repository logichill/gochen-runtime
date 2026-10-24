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
	"gochen/auth/governance"
	"gochen/db"
	"gochen/errors"
)

// PolicySnapshotStore 表示基于 db.IDatabase 的策略快照持久化实现。
type PolicySnapshotStore struct {
	db        db.IDatabase
	tableName string
	// dialect 在构造时解析一次，避免每次查询重复推断；
	// 同时便于测试直接注入方言校验各分支的 SQL 生成。
	dialect coredialect.IDialect
}

// NewPolicySnapshotStore 创建 SQL 策略快照存储。
func NewPolicySnapshotStore(database db.IDatabase, tableName string) (*PolicySnapshotStore, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "policy snapshot database cannot be nil")
	}
	normalizedTableName, err := normalizeSQLTableName(database, tableName, "authz_policy_snapshots")
	if err != nil {
		return nil, err
	}
	return &PolicySnapshotStore{
		db:        database,
		tableName: normalizedTableName,
		dialect:   dialect.FromDatabase(database),
	}, nil
}

// SavePolicySnapshot 保存策略快照。
func (s *PolicySnapshotStore) SavePolicySnapshot(ctx context.Context, snapshot governance.PolicySnapshot) error {
	snapshot, err := normalizePolicySnapshot(snapshot)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(snapshot.Metadata)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "marshal policy snapshot metadata failed")
	}

	if s.dialect.Name() == coredialect.NameUnknown {
		return s.savePolicySnapshotCompat(ctx, snapshot, string(metaJSON))
	}
	query, args := s.buildUpsertQuery(snapshot, string(metaJSON))
	if query == "" {
		// 非标准方言（如第三方注册的方言）不支持 upsert 语法：
		// 回退到 UPDATE+INSERT 兼容路径，而不是直接执行空 SQL。
		return s.savePolicySnapshotCompat(ctx, snapshot, string(metaJSON))
	}
	if _, err := s.db.Exec(ctx, query, args...); err != nil {
		return errors.Wrap(err, errors.Database, "save policy snapshot failed").WithContext("snapshot_key", snapshot.Key)
	}
	return nil
}

func (s *PolicySnapshotStore) buildUpsertQuery(snapshot governance.PolicySnapshot, metadataJSON string) (string, []any) {
	args := []any{snapshot.Key, snapshot.Version, snapshot.Timestamp, metadataJSON}
	switch s.dialect.Name() {
	case coredialect.NameSQLite, coredialect.NamePostgres:
		return fmt.Sprintf(`INSERT INTO %s (snapshot_key, version, timestamp, metadata)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (snapshot_key) DO UPDATE SET
				version = excluded.version,
				timestamp = excluded.timestamp,
				metadata = excluded.metadata`, s.tableName), args
	case coredialect.NameMySQL:
		return fmt.Sprintf(`INSERT INTO %s (snapshot_key, version, timestamp, metadata)
			VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				version = ?,
				timestamp = ?,
				metadata = ?`, s.tableName), append(args, snapshot.Version, snapshot.Timestamp, metadataJSON)
	default:
		return "", nil
	}
}

func (s *PolicySnapshotStore) savePolicySnapshotCompat(ctx context.Context, snapshot governance.PolicySnapshot, metadataJSON string) error {
	updateSQL := fmt.Sprintf(`UPDATE %s
		SET version = ?, timestamp = ?, metadata = ?
		WHERE snapshot_key = ?`, s.tableName)
	result, err := s.db.Exec(ctx, updateSQL, snapshot.Version, snapshot.Timestamp, metadataJSON, snapshot.Key)
	if err != nil {
		return errors.Wrap(err, errors.Database, "update policy snapshot failed").WithContext("snapshot_key", snapshot.Key)
	}
	if rows, errRows := result.RowsAffected(); errRows == nil && rows > 0 {
		return nil
	}
	// RowsAffected==0 有两种含义：记录不存在，或更新值与库中原值完全一致
	// （MySQL 默认语义）。后者并非失败，不能直接走 INSERT 触发唯一键冲突。
	// 因此先按主键探测存在性：存在即视为幂等成功，不存在才插入。
	exists, existsErr := s.policySnapshotExists(ctx, snapshot.Key)
	if existsErr != nil {
		return existsErr
	}
	if exists {
		return nil
	}

	insertSQL := fmt.Sprintf(`INSERT INTO %s (snapshot_key, version, timestamp, metadata) VALUES (?, ?, ?, ?)`, s.tableName)
	if _, err := s.db.Exec(ctx, insertSQL, snapshot.Key, snapshot.Version, snapshot.Timestamp, metadataJSON); err != nil {
		if db.IsUniqueViolation(err) {
			// 并发插入竞争：另一写者已插入，重试更新（此时记录必定存在）。
			return s.updatePolicySnapshotCompat(ctx, updateSQL, snapshot, metadataJSON)
		}
		return errors.Wrap(err, errors.Database, "insert policy snapshot failed").WithContext("snapshot_key", snapshot.Key)
	}
	return nil
}

// policySnapshotExists 按主键探测记录是否存在。
func (s *PolicySnapshotStore) policySnapshotExists(ctx context.Context, key string) (bool, error) {
	query := fmt.Sprintf(`SELECT 1 FROM %s WHERE snapshot_key = ?`, s.tableName)
	row := s.db.QueryRow(ctx, query, key)
	var one int
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, errors.Wrap(err, errors.Database, "probe policy snapshot existence failed").WithContext("snapshot_key", key)
	}
	return true, nil
}

func (s *PolicySnapshotStore) updatePolicySnapshotCompat(ctx context.Context, updateSQL string, snapshot governance.PolicySnapshot, metadataJSON string) error {
	result, err := s.db.Exec(ctx, updateSQL, snapshot.Version, snapshot.Timestamp, metadataJSON, snapshot.Key)
	if err != nil {
		return errors.Wrap(err, errors.Database, "retry update policy snapshot failed").WithContext("snapshot_key", snapshot.Key)
	}
	if rows, errRows := result.RowsAffected(); errRows == nil && rows == 0 {
		// RowsAffected==0 也可能是“更新值与原值一致”的幂等重复保存；
		// 只有记录确实不存在时才判定为并发冲突。
		exists, existsErr := s.policySnapshotExists(ctx, snapshot.Key)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			return errors.NewCode(errors.Conflict, "policy snapshot changed while saving").WithContext("snapshot_key", snapshot.Key)
		}
	}
	return nil
}

// LoadPolicySnapshot 加载策略快照。
func (s *PolicySnapshotStore) LoadPolicySnapshot(ctx context.Context, key string) (*governance.PolicySnapshot, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.NewCode(errors.InvalidInput, "policy snapshot key is required")
	}
	query := fmt.Sprintf(`SELECT snapshot_key, version, timestamp, metadata FROM %s WHERE snapshot_key = ?`, s.tableName)
	row := s.db.QueryRow(ctx, query, key)

	var (
		snapshot governance.PolicySnapshot
		metaRaw  sql.NullString
	)
	if err := row.Scan(&snapshot.Key, &snapshot.Version, &snapshot.Timestamp, &metaRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.NewCode(errors.NotFound, "policy snapshot not found").WithContext("snapshot_key", key)
		}
		return nil, errors.Wrap(err, errors.Database, "query policy snapshot failed").WithContext("snapshot_key", key)
	}
	if metaRaw.Valid && metaRaw.String != "" {
		if err := json.Unmarshal([]byte(metaRaw.String), &snapshot.Metadata); err != nil {
			return nil, errors.Wrap(err, errors.Internal, "decode policy snapshot metadata failed").WithContext("snapshot_key", key)
		}
	}
	normalized, err := normalizePolicySnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

// DeletePolicySnapshot 删除策略快照。
func (s *PolicySnapshotStore) DeletePolicySnapshot(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.NewCode(errors.InvalidInput, "policy snapshot key is required")
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE snapshot_key = ?`, s.tableName)
	if _, err := s.db.Exec(ctx, query, key); err != nil {
		return errors.Wrap(err, errors.Database, "delete policy snapshot failed").WithContext("snapshot_key", key)
	}
	return nil
}

// ListPolicySnapshots 列出策略快照。
func (s *PolicySnapshotStore) ListPolicySnapshots(ctx context.Context, prefix string, limit int) ([]governance.PolicySnapshot, error) {
	query := fmt.Sprintf(`SELECT snapshot_key, version, timestamp, metadata FROM %s`, s.tableName)
	args := make([]any, 0, 2)
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		// ESCAPE 子句必须由方言提供：硬编码的反斜杠转义在 MySQL 默认配置下
		// 会把字符串字面量解析为未闭合，触发 1064 语法错误。
		query += ` WHERE snapshot_key LIKE ?` + s.dialect.LikeEscapeClause()
		args = append(args, escapeLike(prefix)+"%")
	}
	query += ` ORDER BY timestamp DESC, snapshot_key ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "list policy snapshots failed")
	}
	defer rows.Close()

	result := make([]governance.PolicySnapshot, 0)
	for rows.Next() {
		var (
			snapshot governance.PolicySnapshot
			metaRaw  sql.NullString
		)
		if err := rows.Scan(&snapshot.Key, &snapshot.Version, &snapshot.Timestamp, &metaRaw); err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan policy snapshot failed")
		}
		if metaRaw.Valid && metaRaw.String != "" {
			if err := json.Unmarshal([]byte(metaRaw.String), &snapshot.Metadata); err != nil {
				return nil, errors.Wrap(err, errors.Internal, "decode policy snapshot metadata failed")
			}
		}
		normalized, err := normalizePolicySnapshot(snapshot)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate policy snapshots failed")
	}
	return result, nil
}

// CleanupPolicySnapshots 清理过期策略快照。
func (s *PolicySnapshotStore) CleanupPolicySnapshots(ctx context.Context, retentionPeriod time.Duration) error {
	if retentionPeriod <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-retentionPeriod)
	query := fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, s.tableName)
	if _, err := s.db.Exec(ctx, query, cutoff); err != nil {
		return errors.Wrap(err, errors.Database, "cleanup policy snapshots failed")
	}
	return nil
}

func normalizePolicySnapshot(snapshot governance.PolicySnapshot) (governance.PolicySnapshot, error) {
	snapshot.Key = strings.TrimSpace(snapshot.Key)
	snapshot.Version = strings.TrimSpace(snapshot.Version)
	if snapshot.Key == "" {
		return governance.PolicySnapshot{}, errors.NewCode(errors.InvalidInput, "policy snapshot key is required")
	}
	if snapshot.Version == "" {
		return governance.PolicySnapshot{}, errors.NewCode(errors.InvalidInput, "policy snapshot version is required")
	}
	if snapshot.Timestamp.IsZero() {
		snapshot.Timestamp = time.Now()
	}
	if len(snapshot.Metadata) == 0 {
		snapshot.Metadata = nil
	}
	return snapshot, nil
}

func escapeLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
