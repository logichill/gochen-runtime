package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gochen-runtime/internal/sqlsafe"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing"
	"gochen/observe/logging"
)

// preparedEvent 预处理的事件数据（用于批量插入优化）
type preparedEvent struct {
	id             string
	typ            string
	aggregateType  string
	version        uint64
	schemaVersion  int
	globalPosition int64
	timestamp      time.Time
	payloadJSON    string
	metadataJSON   string
}

// AppendEvents 向事件存储追加事件。
func (s *SQLEventStore[ID]) AppendEvents(ctx context.Context, aggregateType string, aggregateID ID, events []eventing.IStorableEvent[ID], expectedVersion uint64) error {
	aggregateType, agg, prepared, empty, err := s.prepareAppendInput(aggregateType, aggregateID, events, expectedVersion)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.recordEventStoreError()
		return errors.NewCodeWithCause(errors.Database, "begin transaction failed", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.appendEventsWithDBValidated(ctx, tx, aggregateType, aggregateID, agg, prepared, expectedVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		s.recordEventStoreError()
		return errors.NewCodeWithCause(errors.Database, "commit transaction failed", err)
	}
	s.getLogger().Info(ctx, "events appended", logging.Any("aggregate_id", aggregateID), logging.Int("event_count", len(prepared)))
	return nil
}

func (s *SQLEventStore[ID]) AppendEventsWithDB(ctx context.Context, database db.IDatabase, aggregateType string, aggregateID ID, events []eventing.IStorableEvent[ID], expectedVersion uint64) error {
	aggregateType, agg, prepared, empty, err := s.prepareAppendInput(aggregateType, aggregateID, events, expectedVersion)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	return s.appendEventsWithDBValidated(ctx, database, aggregateType, aggregateID, agg, prepared, expectedVersion)
}

func (s *SQLEventStore[ID]) appendEventsWithDBValidated(ctx context.Context, database db.IDatabase, aggregateType string, aggregateID ID, agg any, prepared []preparedEvent, expectedVersion uint64) error {
	if _, ok := database.(db.ITransaction); !ok {
		// 在开启事务前完成 schema 能力探测并填充缓存，既保留数据库 wrapper 的
		// 探测语义，也确保随后版本预检与 INSERT 始终处于同一事务。
		if _, err := s.supportsGlobalPositionWithNegativeCache(ctx, database); err != nil {
			s.recordEventStoreError()
			return errors.NewCodeWithCause(errors.Database, "detect global event position support failed", err)
		}
		tx, err := database.Begin(ctx)
		if err != nil {
			s.recordEventStoreError()
			return errors.NewCodeWithCause(errors.Database, "begin transaction failed", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := s.appendEventsWithDBValidated(ctx, tx, aggregateType, aggregateID, agg, prepared, expectedVersion); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			s.recordEventStoreError()
			return errors.NewCodeWithCause(errors.Database, "commit transaction failed", err)
		}
		return nil
	}

	// 第二步：版本检查（必须在事务内）
	currentVersion, err := s.getCurrentVersion(ctx, database, agg, aggregateType)
	if err != nil {
		s.recordEventStoreError()
		return errors.NewCodeWithCause(errors.Database, "query current version failed", err)
	}
	if currentVersion != expectedVersion {
		if currentVersion == expectedVersion+uint64(len(prepared)) {
			idempotent, err := s.isPersistedAppendBatch(ctx, database, aggregateID, agg, prepared, expectedVersion, aggregateType)
			if err != nil {
				return err
			}
			if idempotent {
				return nil
			}
		}
		return errors.NewCode(errors.Concurrency,
			fmt.Sprintf("concurrency conflict: aggregate=%v, expected=%d, actual=%d", aggregateID, expectedVersion, currentVersion),
		).WithContext("aggregate_id", aggregateID).
			WithContext("expected_version", expectedVersion).
			WithContext("actual_version", currentVersion)
	}

	start := time.Now()
	hasGlobalPosition, err := s.supportsGlobalPositionWithNegativeCache(ctx, database)
	if err != nil {
		s.recordEventStoreError()
		return errors.NewCodeWithCause(errors.Database, "detect global event position support failed", err)
	}
	// 第三步：性能优化 - 批量INSERT
	// 构建批量插入SQL: INSERT INTO table VALUES (?, ...), (?, ...), ...
	// 这将N次数据库往返降低到1次；带 global_position 时在位置冲突后刷新位置并重试。
	if err := s.appendPreparedBatch(ctx, database, aggregateID, agg, prepared, start, hasGlobalPosition); err != nil {
		return err
	}
	if hasGlobalPosition {
		s.invalidateStreamHorizonCache()
	}

	duration := time.Since(start)
	s.recordEventSaved(len(prepared), duration)
	s.getLogger().Debug(ctx, "append batch done", logging.Any("aggregate_id", aggregateID), logging.Int("written", len(prepared)), logging.Int64("ms", duration.Milliseconds()))
	return nil
}

func (s *SQLEventStore[ID]) appendEventsIndividually(ctx context.Context, database db.IDatabase, aggregateID ID, agg any, prepared []preparedEvent, start time.Time, hasGlobalPosition bool) error {
	insertSQL := fmt.Sprintf(`INSERT INTO %s (id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tableName)
	if hasGlobalPosition {
		insertSQL = fmt.Sprintf(`INSERT INTO %s (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tableName)
	}

	for i := range prepared {
		p := prepared[i]
		for attempt := 0; ; attempt++ {
			if hasGlobalPosition && (p.globalPosition <= 0 || attempt > 0) {
				if err := s.assignGlobalPositions(ctx, database, prepared[i:]); err != nil {
					return err
				}
				p = prepared[i]
			}
			err := executeRecoverableStatement(ctx, database, "append_single", func() error {
				args := []any{p.id, p.typ, agg, p.aggregateType, p.version, p.schemaVersion, p.timestamp, p.payloadJSON, p.metadataJSON}
				if hasGlobalPosition {
					args = []any{p.id, p.typ, agg, p.aggregateType, p.version, p.schemaVersion, p.globalPosition, p.timestamp, p.payloadJSON, p.metadataJSON}
				}
				_, err := database.Exec(ctx, insertSQL, args...)
				return err
			})
			if err == nil {
				if hasGlobalPosition {
					s.invalidateStreamHorizonCache()
				}
				break
			}
			if hasGlobalPosition && isGlobalPositionUniqueError(err) && attempt < maxGlobalPositionRetries {
				if err := waitGlobalPositionRetry(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			if isDuplicateKeyError(err) {
				idempotent, classified := s.classifyUniqueInsertError(ctx, database, aggregateID, agg, p, err)
				if idempotent {
					break // 跳过重复事件
				}
				return classified
			}
			s.recordEventStoreError()
			return errors.NewCodeWithCause(errors.Database, "insert event failed", err).WithContext("event_id", p.id).WithContext("event_type", p.typ)
		}
	}

	duration := time.Since(start)
	s.recordEventSaved(len(prepared), duration)
	return nil
}

func (s *SQLEventStore[ID]) prepareAppendEvents(
	events []eventing.IStorableEvent[ID],
	expectedVersion uint64,
	aggregateType string,
) ([]preparedEvent, error) {
	prepared := make([]preparedEvent, 0, len(events))
	for idx, evt := range events {
		item, err := prepareAppendEvent(evt, expectedVersion+uint64(idx)+1, aggregateType)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func prepareAppendEvent[ID comparable](event eventing.IStorableEvent[ID], expectedVersion uint64, aggregateType string) (preparedEvent, error) {
	candidate, isolated := isolateAppendEvent(event)
	originalAggregateType := candidate.GetAggregateType()
	eventAggregateType := strings.TrimSpace(originalAggregateType)
	if eventAggregateType == "" {
		eventAggregateType = aggregateType
	} else if eventAggregateType != aggregateType {
		return preparedEvent{}, errors.NewCode(errors.InvalidInput, "mixed aggregate types in append batch").WithContext("event_id", candidate.GetID()).WithContext("event_type", candidate.GetType())
	}
	if originalAggregateType != aggregateType {
		candidate.SetAggregateType(aggregateType)
		if !isolated {
			defer candidate.SetAggregateType(originalAggregateType)
		}
	}
	if candidate.GetVersion() != expectedVersion {
		return preparedEvent{}, errors.NewCode(errors.InvalidInput, fmt.Sprintf("event version mismatch: expected %d, got %d", expectedVersion, candidate.GetVersion())).WithContext("event_id", candidate.GetID()).WithContext("event_type", candidate.GetType())
	}
	if err := candidate.Validate(); err != nil {
		return preparedEvent{}, errors.NewCode(errors.InvalidInput, fmt.Sprintf("event validation failed: %v", err)).WithContext("event_id", candidate.GetID()).WithContext("event_type", candidate.GetType())
	}
	payloadJSON, err := json.Marshal(candidate.GetPayload())
	if err != nil {
		return preparedEvent{}, errors.NewCodeWithCause(errors.Internal, "serialize payload failed", err).WithContext("event_id", candidate.GetID()).WithContext("event_type", candidate.GetType())
	}
	metadataJSON, err := json.Marshal(candidate.GetMetadata())
	if err != nil {
		return preparedEvent{}, errors.NewCodeWithCause(errors.Internal, "serialize metadata failed", err).WithContext("event_id", candidate.GetID()).WithContext("event_type", candidate.GetType())
	}
	return preparedEvent{
		id:            candidate.GetID(),
		typ:           candidate.GetType(),
		aggregateType: eventAggregateType,
		version:       candidate.GetVersion(),
		schemaVersion: candidate.EventSchemaVersion(),
		timestamp:     candidate.GetTimestamp(),
		payloadJSON:   string(payloadJSON),
		metadataJSON:  string(metadataJSON),
	}, nil
}

func isolateAppendEvent[ID comparable](event eventing.IStorableEvent[ID]) (eventing.IStorableEvent[ID], bool) {
	if standard, ok := event.(*eventing.Event[ID]); ok {
		clone := *standard
		return &clone, true
	}
	return event, false
}

func (s *SQLEventStore[ID]) appendPreparedBatch(
	ctx context.Context,
	database db.IDatabase,
	aggregateID ID,
	agg any,
	prepared []preparedEvent,
	start time.Time,
	hasGlobalPosition bool,
) error {
	if hasGlobalPosition {
		if err := s.assignGlobalPositions(ctx, database, prepared); err != nil {
			return err
		}
	}
	if len(prepared) == 1 {
		p := prepared[0]
		insertSQL := fmt.Sprintf(`INSERT INTO %s (id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tableName)
		args := []any{p.id, p.typ, agg, p.aggregateType, p.version, p.schemaVersion, p.timestamp, p.payloadJSON, p.metadataJSON}
		if hasGlobalPosition {
			insertSQL = fmt.Sprintf(`INSERT INTO %s (id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tableName)
		}
		for attempt := 0; ; attempt++ {
			if hasGlobalPosition && attempt > 0 {
				if err := s.assignGlobalPositions(ctx, database, prepared); err != nil {
					return err
				}
				p = prepared[0]
			}
			if hasGlobalPosition {
				args = []any{p.id, p.typ, agg, p.aggregateType, p.version, p.schemaVersion, p.globalPosition, p.timestamp, p.payloadJSON, p.metadataJSON}
			}
			err := executeRecoverableStatement(ctx, database, "append_single", func() error {
				_, err := database.Exec(ctx, insertSQL, args...)
				return err
			})
			if err == nil {
				return nil
			}
			if hasGlobalPosition && isGlobalPositionUniqueError(err) && attempt < maxGlobalPositionRetries {
				if err := waitGlobalPositionRetry(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			if isDuplicateKeyError(err) {
				idempotent, classified := s.classifyUniqueInsertError(ctx, database, aggregateID, agg, p, err)
				if idempotent {
					return nil
				}
				return classified
			}
			s.recordEventStoreError()
			return errors.NewCodeWithCause(errors.Database, "insert event failed", err).WithContext("event_id", p.id).WithContext("event_type", p.typ)
		}
	}
	columns := "id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata"
	argsPerEvent := 9
	placeholder := "(?, ?, ?, ?, ?, ?, ?, ?, ?)"
	if hasGlobalPosition {
		columns = "id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata"
		argsPerEvent = 10
		placeholder = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	}
	maxEvents := appendBatchChunkSize(argsPerEvent)
	for attempt := 0; ; attempt++ {
		if hasGlobalPosition && attempt > 0 {
			if err := s.assignGlobalPositions(ctx, database, prepared); err != nil {
				return err
			}
		}
		err := s.appendPreparedBatchChunks(ctx, database, agg, prepared, columns, placeholder, argsPerEvent, maxEvents, hasGlobalPosition)
		if err == nil {
			return nil
		}
		if hasGlobalPosition && isGlobalPositionUniqueError(err) && attempt < maxGlobalPositionRetries {
			if err := waitGlobalPositionRetry(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		if isDuplicateKeyError(err) {
			s.getLogger().Debug(ctx, "batch insert failed with duplicate key, falling back to individual inserts", logging.Int("event_count", len(prepared)))
			return s.appendEventsIndividually(ctx, database, aggregateID, agg, prepared, start, hasGlobalPosition)
		}
		s.recordEventStoreError()
		return errors.NewCodeWithCause(errors.Database, "batch insert events failed", err)
	}
}

func (s *SQLEventStore[ID]) appendPreparedBatchChunks(
	ctx context.Context,
	database db.IDatabase,
	agg any,
	prepared []preparedEvent,
	columns string,
	placeholder string,
	argsPerEvent int,
	maxEvents int,
	hasGlobalPosition bool,
) error {
	for start := 0; start < len(prepared); start += maxEvents {
		end := start + maxEvents
		if end > len(prepared) {
			end = len(prepared)
		}
		chunk := prepared[start:end]
		placeholders := make([]string, len(chunk))
		for i := range placeholders {
			placeholders[i] = placeholder
		}
		batchSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", s.tableName, columns, strings.Join(placeholders, ","))
		args := make([]any, 0, len(chunk)*argsPerEvent)
		for _, p := range chunk {
			args = append(args, p.id, p.typ, agg, p.aggregateType, p.version, p.schemaVersion)
			if hasGlobalPosition {
				args = append(args, p.globalPosition)
			}
			args = append(args, p.timestamp, p.payloadJSON, p.metadataJSON)
		}
		if err := executeRecoverableStatement(ctx, database, "append_batch", func() error {
			_, err := database.Exec(ctx, batchSQL, args...)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func appendBatchChunkSize(argsPerEvent int) int {
	if argsPerEvent <= 0 || sqlsafe.DefaultInsertMaxParams <= argsPerEvent {
		return 1
	}
	return sqlsafe.DefaultInsertMaxParams / argsPerEvent
}

func (s *SQLEventStore[ID]) assignGlobalPositions(ctx context.Context, database db.IDatabase, prepared []preparedEvent) error {
	nextPosition, err := s.reserveGlobalPositions(ctx, database, len(prepared))
	if err != nil {
		return errors.NewCodeWithCause(errors.Database, "reserve global event positions failed", err)
	}
	for i := range prepared {
		prepared[i].globalPosition = nextPosition + int64(i)
	}
	return nil
}

func (s *SQLEventStore[ID]) prepareAppendInput(aggregateType string, aggregateID ID, events []eventing.IStorableEvent[ID], expectedVersion uint64) (string, any, []preparedEvent, bool, error) {
	aggregateType = strings.TrimSpace(aggregateType)
	if aggregateType == "" {
		return "", nil, nil, false, errors.NewCode(errors.InvalidInput, "aggregate type cannot be empty")
	}
	if len(events) == 0 {
		return aggregateType, nil, nil, true, nil
	}

	for index, event := range events {
		if isNilAppendEvent(event) {
			return "", nil, nil, false, errors.NewCode(errors.InvalidInput, "event cannot be nil").WithContext("event_index", index)
		}
		if event.GetAggregateID() != aggregateID {
			return "", nil, nil, false, errors.NewCode(errors.InvalidInput, "event aggregate id does not match append aggregate id").
				WithContext("aggregate_id", aggregateID).
				WithContext("event_aggregate_id", event.GetAggregateID()).
				WithContext("event_id", event.GetID()).
				WithContext("event_type", event.GetType())
		}
		eventAggregateType := strings.TrimSpace(event.GetAggregateType())
		if eventAggregateType != "" && eventAggregateType != aggregateType {
			return "", nil, nil, false, errors.NewCode(errors.InvalidInput, "mixed aggregate types in append batch").
				WithContext("event_index", index).
				WithContext("event_id", event.GetID()).
				WithContext("event_type", event.GetType())
		}
	}
	agg, err := s.codec.Encode(aggregateID)
	if err != nil {
		return "", nil, nil, false, errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}
	prepared, err := s.prepareAppendEvents(events, expectedVersion, aggregateType)
	if err != nil {
		return "", nil, nil, false, err
	}
	return aggregateType, agg, prepared, false, nil
}

func isNilAppendEvent[ID comparable](event eventing.IStorableEvent[ID]) bool {
	if event == nil {
		return true
	}
	value := reflect.ValueOf(event)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
