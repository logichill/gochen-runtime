package snapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/safesql"
	"gochen-runtime/internal/sqlresult"
	"gochen/codec"
	"gochen/codec/idcodec"
	"gochen/db"
	"gochen/errors"
	snapcore "gochen/eventing/store/snapshot"
	"gochen/observe/logging"
)

// SQLStore 基于通用 db.IDatabase 的快照存储实现。
//
// 语义说明：
// - 仅作为聚合重建的性能优化层，不改变事件存储的“真相”角色；
// - 每个 (aggregate_type, aggregate_id) 只保留一条最新快照；
// - SaveSnapshot 采用“UPDATE 若无则 INSERT”的幂等写入策略，兼容 MySQL/SQLite。
type SQLStore[ID comparable] struct {
	db        db.IDatabase
	tableName string
	codec     codec.ICodec[ID, any]
	tableErr  error
	log       logging.ILogger
}

// NewSQLStore 为 `int64` 聚合 ID 创建一个 SQL 快照存储。
func NewSQLStore(db db.IDatabase, tableName string) *SQLStore[int64] {
	return NewSQLStoreWithLogger(db, tableName, nil)
}

// NewSQLStoreWithLogger 为 `int64` 聚合 ID 创建一个复用显式基础 logger 的 SQL 快照存储。
func NewSQLStoreWithLogger(db db.IDatabase, tableName string, logger logging.ILogger) *SQLStore[int64] {
	return NewSQLStoreWithCodecAndLogger[int64](db, tableName, idcodec.NewInt64[int64](), logger)
}

// NewSQLStoreWithCodec 创建可自定义聚合 ID 编解码方式的 SQL 快照存储。
func NewSQLStoreWithCodec[ID comparable](db db.IDatabase, tableName string, idCodec codec.ICodec[ID, any]) *SQLStore[ID] {
	return NewSQLStoreWithCodecAndLogger(db, tableName, idCodec, nil)
}

// NewSQLStoreWithCodecAndLogger 创建可自定义聚合 ID 编解码并复用显式基础 logger 的 SQL 快照存储。
func NewSQLStoreWithCodecAndLogger[ID comparable](db db.IDatabase, tableName string, idCodec codec.ICodec[ID, any], logger logging.ILogger) *SQLStore[ID] {
	normalizedTableName, err := normalizeSnapshotSQLTableName(db, tableName, "event_snapshots")
	return &SQLStore[ID]{db: db, tableName: normalizedTableName, codec: idCodec, tableErr: err, log: newSnapshotLogger(logger)}
}

// SaveSnapshot 保存或覆盖指定聚合的最新快照。
func (s *SQLStore[ID]) SaveSnapshot(ctx context.Context, snapshot snapcore.Snapshot[ID]) error {
	if s.db == nil {
		return errors.NewCode(errors.InvalidInput, "snapshot SQLStore database is nil")
	}
	if s.codec == nil {
		return errors.NewCode(errors.InvalidInput, "snapshot SQLStore codec is nil")
	}
	if s.tableErr != nil {
		return s.tableErr
	}

	// 确保时间有效
	ts := snapshot.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	// 元数据序列化为 JSON（可为空）
	var metaJSON *string
	if len(snapshot.Metadata) > 0 {
		b, err := json.Marshal(snapshot.Metadata)
		if err != nil {
			return errors.Wrap(err, errors.InvalidInput, "serialize snapshot metadata failed")
		}
		str := string(b)
		metaJSON = &str
	}

	// 先尝试 UPDATE，存在则更新
	updateSQL := fmt.Sprintf(`UPDATE %s
			SET version = ?, data = ?, timestamp = ?, metadata = ?
			WHERE aggregate_type = ? AND aggregate_id = ? AND version <= ?`, s.tableName)
	agg, err := s.codec.Encode(snapshot.AggregateID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}
	res, err := s.db.Exec(ctx, updateSQL,
		snapshot.Version,
		snapshot.Data,
		ts,
		metaJSON,
		snapshot.AggregateType,
		agg,
		snapshot.Version,
	)
	if err != nil {
		return errors.Wrap(err, errors.Database, "update snapshot failed").
			WithContext("aggregate_type", snapshot.AggregateType).
			WithContext("aggregate_id", snapshot.AggregateID)
	}
	rows, errRA := res.RowsAffected()
	if errRA != nil {
		return sqlresult.RowsAffectedError(errRA, "update snapshot rows affected failed")
	}
	if rows > 0 {
		snapshotStoreLogger(s.log).Debug(ctx, "snapshot updated",
			logging.Any("aggregate_id", snapshot.AggregateID),
			logging.String("aggregate_type", snapshot.AggregateType),
			logging.Any("version", snapshot.Version),
		)
		return nil
	}

	// 不存在则 INSERT
	insertSQL := fmt.Sprintf(`INSERT INTO %s
		(aggregate_type, aggregate_id, version, data, timestamp, metadata)
		VALUES (?, ?, ?, ?, ?, ?)`, s.tableName)
	if _, err := s.db.Exec(ctx, insertSQL,
		snapshot.AggregateType,
		agg,
		snapshot.Version,
		snapshot.Data,
		ts,
		metaJSON,
	); err != nil {
		if isSnapshotDuplicateKeyError(err) {
			res, retryErr := s.db.Exec(ctx, updateSQL,
				snapshot.Version,
				snapshot.Data,
				ts,
				metaJSON,
				snapshot.AggregateType,
				agg,
				snapshot.Version,
			)
			if retryErr != nil {
				return errors.Wrap(retryErr, errors.Database, "retry update snapshot failed").
					WithContext("aggregate_type", snapshot.AggregateType).
					WithContext("aggregate_id", snapshot.AggregateID)
			}
			rows, errRA := res.RowsAffected()
			if errRA != nil {
				return sqlresult.RowsAffectedError(errRA, "retry update snapshot rows affected failed")
			}
			if rows > 0 {
				snapshotStoreLogger(s.log).Debug(ctx, "snapshot updated after insert conflict",
					logging.Any("aggregate_id", snapshot.AggregateID),
					logging.String("aggregate_type", snapshot.AggregateType),
					logging.Any("version", snapshot.Version),
				)
			}
			return nil
		}
		return errors.Wrap(err, errors.Database, "insert snapshot failed").
			WithContext("aggregate_type", snapshot.AggregateType).
			WithContext("aggregate_id", snapshot.AggregateID)
	}

	snapshotStoreLogger(s.log).Debug(ctx, "snapshot created",
		logging.Any("aggregate_id", snapshot.AggregateID),
		logging.String("aggregate_type", snapshot.AggregateType),
		logging.Any("version", snapshot.Version),
	)
	return nil
}

// FindSnapshot 读取指定聚合当前保存的最新快照。
func (s *SQLStore[ID]) FindSnapshot(ctx context.Context, aggregateType string, aggregateID ID) (*snapcore.Snapshot[ID], error) {
	if s.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "snapshot SQLStore database is nil")
	}
	if s.codec == nil {
		return nil, errors.NewCode(errors.InvalidInput, "snapshot SQLStore codec is nil")
	}
	if s.tableErr != nil {
		return nil, s.tableErr
	}

	query := fmt.Sprintf(`SELECT aggregate_id, aggregate_type, version, data, timestamp, metadata
		FROM %s
		WHERE aggregate_type = ? AND aggregate_id = ?`, s.tableName)

	agg, err := s.codec.Encode(aggregateID)
	if err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}

	row := s.db.QueryRow(ctx, query, aggregateType, agg)
	var snap snapcore.Snapshot[ID]
	var rawAggID any
	var versionInt int64
	var ts time.Time
	var metaStr sql.NullString

	if err := row.Scan(
		&rawAggID,
		&snap.AggregateType,
		&versionInt,
		&snap.Data,
		&ts,
		&metaStr,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.NewCode(errors.NotFound, "snapshot not found").
				WithContext("aggregate_type", aggregateType).
				WithContext("aggregate_id", aggregateID)
		}
		return nil, errors.Wrap(err, errors.Database, "query snapshot failed").
			WithContext("aggregate_type", aggregateType).
			WithContext("aggregate_id", aggregateID)
	}

	typedAggID, err := s.codec.Decode(rawAggID)
	if err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
			WithContext("aggregate_type", aggregateType).
			WithContext("aggregate_id", aggregateID)
	}
	snap.AggregateID = typedAggID
	snap.Version = uint64(versionInt)
	snap.Timestamp = ts

	if metaStr.Valid && metaStr.String != "" {
		meta, err := decodeSnapshotMetadata(metaStr.String)
		if err != nil {
			return nil, err.
				WithContext("aggregate_type", snap.AggregateType).
				WithContext("aggregate_id", snap.AggregateID)
		}
		snap.Metadata = meta
	}

	return &snap, nil
}

// DeleteSnapshot 删除指定聚合对应的快照。
func (s *SQLStore[ID]) DeleteSnapshot(ctx context.Context, aggregateType string, aggregateID ID) error {
	if s.db == nil {
		return errors.NewCode(errors.InvalidInput, "snapshot SQLStore database is nil")
	}
	if s.codec == nil {
		return errors.NewCode(errors.InvalidInput, "snapshot SQLStore codec is nil")
	}
	if s.tableErr != nil {
		return s.tableErr
	}

	query := fmt.Sprintf(`DELETE FROM %s WHERE aggregate_type = ? AND aggregate_id = ?`, s.tableName)
	agg, err := s.codec.Encode(aggregateID)
	if err != nil {
		return errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}

	if _, err := s.db.Exec(ctx, query, aggregateType, agg); err != nil {
		return errors.Wrap(err, errors.Database, "delete snapshot failed").
			WithContext("aggregate_type", aggregateType).
			WithContext("aggregate_id", aggregateID)
	}

	snapshotStoreLogger(s.log).Debug(ctx, "snapshot deleted",
		logging.Any("aggregate_id", aggregateID),
		logging.String("aggregate_type", aggregateType),
	)
	return nil
}

// ListSnapshots 列出快照记录，可按聚合类型和数量限制过滤。
func (s *SQLStore[ID]) ListSnapshots(ctx context.Context, aggregateType string, limit int) ([]snapcore.Snapshot[ID], error) {
	if s.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "snapshot SQLStore database is nil")
	}
	if s.codec == nil {
		return nil, errors.NewCode(errors.InvalidInput, "snapshot SQLStore codec is nil")
	}
	if s.tableErr != nil {
		return nil, s.tableErr
	}

	base := fmt.Sprintf(`SELECT aggregate_id, aggregate_type, version, data, timestamp, metadata
		FROM %s`, s.tableName)
	var (
		args  []any
		where string
		lim   string
	)
	if aggregateType != "" {
		where = " WHERE aggregate_type = ?"
		args = append(args, aggregateType)
	}
	if limit > 0 {
		lim = " LIMIT ?"
		args = append(args, limit)
	}

	query := base + where + " ORDER BY aggregate_type, aggregate_id" + lim
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "list snapshots failed").
			WithContext("aggregate_type", aggregateType)
	}
	defer rows.Close()

	var result []snapcore.Snapshot[ID]
	for rows.Next() {
		var snap snapcore.Snapshot[ID]
		var rawAggID any
		var versionInt int64
		var ts time.Time
		var metaStr sql.NullString

		if err := rows.Scan(
			&rawAggID,
			&snap.AggregateType,
			&versionInt,
			&snap.Data,
			&ts,
			&metaStr,
		); err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan snapshot row failed")
		}
		typedAggID, err := s.codec.Decode(rawAggID)
		if err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
				WithContext("aggregate_type", snap.AggregateType)
		}
		snap.AggregateID = typedAggID
		snap.Version = uint64(versionInt)
		snap.Timestamp = ts

		if metaStr.Valid && metaStr.String != "" {
			meta, err := decodeSnapshotMetadata(metaStr.String)
			if err != nil {
				return nil, err.
					WithContext("aggregate_type", snap.AggregateType).
					WithContext("aggregate_id", snap.AggregateID)
			}
			snap.Metadata = meta
		}

		result = append(result, snap)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate snapshot rows failed")
	}

	return result, nil
}

// CleanupSnapshots 删除超过保留期限的快照记录。
func (s *SQLStore[ID]) CleanupSnapshots(ctx context.Context, retentionPeriod time.Duration) error {
	if s.db == nil {
		return errors.NewCode(errors.InvalidInput, "snapshot SQLStore database is nil")
	}
	if s.tableErr != nil {
		return s.tableErr
	}
	if retentionPeriod <= 0 {
		return nil
	}

	cutoff := time.Now().Add(-retentionPeriod)
	query := fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, s.tableName)
	res, err := s.db.Exec(ctx, query, cutoff)
	if err != nil {
		return errors.Wrap(err, errors.Database, "cleanup snapshots failed")
	}
	deleted, errRA := res.RowsAffected()
	if errRA != nil {
		return sqlresult.RowsAffectedError(errRA, "cleanup snapshots rows affected failed")
	}
	snapshotStoreLogger(s.log).Info(ctx, "expired snapshots cleaned up",
		logging.Int64("deleted_count", deleted),
	)
	return nil
}

// 确保 SQLStore 实现 snapcore.ISnapshotStore[int64] 接口。
var _ snapcore.ISnapshotStore[int64] = (*SQLStore[int64])(nil)

func normalizeSnapshotSQLTableName(database db.IDatabase, tableName, fallback string) (string, error) {
	quoted, err := safesql.QuoteQualifiedIdentifier(dialect.FromDatabase(database), tableName, fallback, "SQL table name", 2)
	if err != nil {
		return "", errors.NewCode(errors.InvalidInput, "invalid SQL table name").WithContext("cause", err.Error())
	}
	return quoted, nil
}

func decodeSnapshotMetadata(raw string) (map[string]any, errors.IAppError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, errors.Wrap(err, errors.Internal, "decode snapshot metadata failed")
	}
	return meta, nil
}

func isSnapshotDuplicateKeyError(err error) bool {
	return db.IsUniqueViolation(err)
}

var defaultSnapshotLogger = logging.ComponentLogger("eventstore.snapshot")

func newSnapshotLogger(base logging.ILogger) logging.ILogger {
	if base == nil {
		return defaultSnapshotLogger
	}
	return logging.ComponentLogger("eventstore.snapshot", base)
}

func snapshotStoreLogger(logger logging.ILogger) logging.ILogger {
	if logger == nil {
		return defaultSnapshotLogger
	}
	return logger
}
