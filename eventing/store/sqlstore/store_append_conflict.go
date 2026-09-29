package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gochen/db"
	"gochen/errors"
	"gochen/observe/logging"
)

type persistedEvent[ID comparable] struct {
	id                 string
	typ                string
	aggregateID        ID
	aggregateIDDecoded bool
	aggregateType      string
	version            uint64
	schemaVersion      int
	timestamp          time.Time
	payloadJSON        string
	metadataJSON       string
}

func (s *SQLEventStore[ID]) getCurrentVersion(ctx context.Context, db db.IDatabase, aggregateID any, aggregateType string) (uint64, error) {
	var current uint64
	row := db.QueryRow(ctx, fmt.Sprintf("SELECT COALESCE(MAX(version), 0) FROM %s WHERE aggregate_id = ? AND aggregate_type = ?", s.tableName), aggregateID, aggregateType)
	if err := row.Scan(&current); err != nil {
		return 0, err
	}
	return current, nil
}

// isDuplicateKeyError 判断DuplicateKey错误。
func isDuplicateKeyError(err error) bool {
	return db.IsUniqueViolation(err)
}

func (s *SQLEventStore[ID]) classifyUniqueInsertError(
	ctx context.Context,
	db db.IDatabase,
	aggregateID ID,
	encodedAggregateID any,
	p preparedEvent,
	insertErr error,
) (bool, error) {
	if ctx == nil {
		return false, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if s == nil {
		return false, errors.NewCode(errors.InvalidInput, "event store is nil")
	}

	// 1) 首先按 event_id 判断：若同一 event_id 已存在且完全匹配，则视为幂等成功；
	//    若 event_id 已存在但不匹配，则必然是 Duplicate（id 被占用），不应误报 Concurrency。
	found, existing, err := s.getEventByID(ctx, db, p.id)
	if err != nil {
		// 分类失败时：保留“插入失败”的核心语义，并携带冲突排查错误。
		return false, errors.NewCodeWithCause(errors.Database, "insert event failed", insertErr).
			WithContext("event_id", p.id).
			WithContext("event_type", p.typ).
			WithContext("conflict_resolution_error", err.Error())
	}
	if found {
		// Decode 失败时也应当视为“event_id 已被占用”，避免误判为 Concurrency。
		if s.matchesPersistedEvent(existing, aggregateID, p) {
			return true, nil
		}
		return false, errors.NewCode(errors.Duplicate, fmt.Sprintf("event %s already exists", p.id)).
			WithContext("event_id", p.id).
			WithContext("event_type", p.typ).
			WithContext("aggregate_id", aggregateID).
			WithContext("version", p.version)
	}

	// 2) event_id 不冲突时，再判断 (aggregate_id, aggregate_type, version) 是否已存在：
	//    这通常意味着“并发写入同一版本”，应归类为 Concurrency 而非 Duplicate。
	existingEventID, ok, err := s.getEventIDByAggregateVersion(ctx, db, encodedAggregateID, p.aggregateType, p.version)
	if err != nil {
		return false, errors.NewCodeWithCause(errors.Database, "insert event failed", insertErr).
			WithContext("event_id", p.id).
			WithContext("event_type", p.typ).
			WithContext("conflict_resolution_error", err.Error())
	}
	if ok {
		return false, errors.NewCode(errors.Concurrency,
			fmt.Sprintf("concurrency conflict: aggregate=%v, version=%d already exists", aggregateID, p.version),
		).WithContext("aggregate_id", aggregateID).
			WithContext("aggregate_type", p.aggregateType).
			WithContext("version", p.version).
			WithContext("existing_event_id", existingEventID)
	}

	// 3) 兜底：仍然认为是 Duplicate（例如：表存在其他唯一约束）。
	return false, errors.NewCode(errors.Duplicate, fmt.Sprintf("event %s already exists for aggregate %v version %d", p.id, aggregateID, p.version)).
		WithContext("event_id", p.id).
		WithContext("event_type", p.typ).
		WithContext("aggregate_id", aggregateID).
		WithContext("aggregate_type", p.aggregateType).
		WithContext("version", p.version)
}

func (s *SQLEventStore[ID]) getEventByID(ctx context.Context, db db.IDatabase, eventID string) (bool, persistedEvent[ID], error) {
	var existing persistedEvent[ID]
	if ctx == nil {
		return false, existing, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if db == nil {
		return false, existing, errors.NewCode(errors.InvalidInput, "db is nil")
	}
	if s == nil || s.codec == nil {
		return false, existing, errors.NewCode(errors.InvalidInput, "codec is nil")
	}

	var existingAggregateID any
	row := db.QueryRow(ctx, fmt.Sprintf("SELECT id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata FROM %s WHERE id = ?", s.tableName), eventID)
	if err := row.Scan(&existing.id, &existing.typ, &existingAggregateID, &existing.aggregateType, &existing.version, &existing.schemaVersion, &existing.timestamp, &existing.payloadJSON, &existing.metadataJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, existing, nil
		}
		return false, existing, err
	}

	typed, err := s.codec.Decode(existingAggregateID)
	if err != nil {
		// 事件存在但 ID 无法解码：仍视为 "found"，避免误判为并发冲突。
		s.getLogger().Warn(ctx, "event aggregate id decode failed during duplicate conflict resolution",
			logging.Error(err),
			logging.String("event_id", existing.id),
			logging.String("event_type", existing.typ),
			logging.Any("aggregate_id", existingAggregateID),
			logging.String("aggregate_type", existing.aggregateType),
			logging.Uint64("version", existing.version),
		)
		return true, existing, nil
	}
	existing.aggregateID = typed
	existing.aggregateIDDecoded = true
	return true, existing, nil
}

func (s *SQLEventStore[ID]) getEventIDByAggregateVersion(ctx context.Context, db db.IDatabase, encodedAggregateID any, aggregateType string, version uint64) (string, bool, error) {
	if ctx == nil {
		return "", false, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if db == nil {
		return "", false, errors.NewCode(errors.InvalidInput, "db is nil")
	}

	var id string
	row := db.QueryRow(ctx, fmt.Sprintf("SELECT id FROM %s WHERE aggregate_id = ? AND aggregate_type = ? AND version = ? LIMIT 1", s.tableName), encodedAggregateID, aggregateType, version)
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

func (s *SQLEventStore[ID]) isPersistedAppendBatch(
	ctx context.Context,
	db db.IDatabase,
	aggregateID ID,
	encodedAggregateID any,
	prepared []preparedEvent,
	expectedVersion uint64,
	aggregateType string,
) (bool, error) {
	if len(prepared) == 0 {
		return true, nil
	}
	preparedBatch := make(map[uint64]preparedEvent, len(prepared))
	for _, event := range prepared {
		preparedBatch[event.version] = event
	}

	existingByVersion, err := s.getEventsByAggregateVersionRange(
		ctx,
		db,
		encodedAggregateID,
		aggregateType,
		expectedVersion+1,
		expectedVersion+uint64(len(prepared)),
	)
	if err != nil {
		return false, errors.NewCodeWithCause(errors.Database, "query existing events failed", err).
			WithContext("aggregate_id", aggregateID).
			WithContext("aggregate_type", aggregateType).
			WithContext("from_version", expectedVersion+1).
			WithContext("to_version", expectedVersion+uint64(len(prepared)))
	}
	if len(existingByVersion) != len(preparedBatch) {
		return false, nil
	}
	for version, prepared := range preparedBatch {
		existing, ok := existingByVersion[version]
		if !ok || !s.matchesPersistedEvent(existing, aggregateID, prepared) {
			return false, nil
		}
	}
	return true, nil
}

func (s *SQLEventStore[ID]) getEventsByAggregateVersionRange(
	ctx context.Context,
	db db.IDatabase,
	encodedAggregateID any,
	aggregateType string,
	fromVersion uint64,
	toVersion uint64,
) (map[uint64]persistedEvent[ID], error) {
	rows, err := db.Query(ctx, fmt.Sprintf(`
		SELECT id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata
		FROM %s
		WHERE aggregate_id = ? AND aggregate_type = ? AND version BETWEEN ? AND ?
	`, s.tableName), encodedAggregateID, aggregateType, fromVersion, toVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	eventsByVersion := make(map[uint64]persistedEvent[ID])
	for rows.Next() {
		var (
			existing            persistedEvent[ID]
			existingAggregateID any
		)
		if err := rows.Scan(
			&existing.id,
			&existing.typ,
			&existingAggregateID,
			&existing.aggregateType,
			&existing.version,
			&existing.schemaVersion,
			&existing.timestamp,
			&existing.payloadJSON,
			&existing.metadataJSON,
		); err != nil {
			return nil, err
		}
		typed, err := s.codec.Decode(existingAggregateID)
		if err != nil {
			s.getLogger().Warn(ctx, "event aggregate id decode failed during aggregate version conflict resolution",
				logging.Error(err),
				logging.String("event_id", existing.id),
				logging.String("event_type", existing.typ),
				logging.Any("aggregate_id", existingAggregateID),
				logging.String("aggregate_type", existing.aggregateType),
				logging.Uint64("version", existing.version),
			)
		} else {
			existing.aggregateID = typed
			existing.aggregateIDDecoded = true
		}
		eventsByVersion[existing.version] = existing
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return eventsByVersion, nil
}

func (s *SQLEventStore[ID]) matchesPersistedEvent(existing persistedEvent[ID], aggregateID ID, p preparedEvent) bool {
	return existing.aggregateIDDecoded &&
		existing.aggregateID == aggregateID &&
		existing.id == p.id &&
		existing.typ == p.typ &&
		existing.aggregateType == p.aggregateType &&
		existing.version == p.version &&
		existing.schemaVersion == p.schemaVersion &&
		matchesPersistedTimestamp(existing.timestamp, p.timestamp) &&
		existing.payloadJSON == p.payloadJSON &&
		existing.metadataJSON == p.metadataJSON
}

func matchesPersistedTimestamp(existing, expected time.Time) bool {
	if existing.Equal(expected) {
		return true
	}
	precisions := []time.Duration{
		time.Microsecond,
		time.Millisecond,
		time.Second,
	}
	for _, precision := range precisions {
		if existing.Equal(expected.Truncate(precision)) || existing.Truncate(precision).Equal(expected.Truncate(precision)) {
			return true
		}
	}
	return false
}
