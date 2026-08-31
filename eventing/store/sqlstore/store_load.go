package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gochen/errors"
	"gochen/eventing"
	"gochen/messaging"

	"gochen/eventing/store"
)

// LoadEvents 加载指定类型的聚合事件。
func (s *SQLEventStore[ID]) LoadEvents(ctx context.Context, aggregateType string, aggregateID ID, afterVersion uint64) ([]eventing.Event[ID], error) {
	aggregateType = strings.TrimSpace(aggregateType)
	if aggregateType == "" {
		return nil, errors.NewCode(errors.InvalidInput, "aggregate type cannot be empty")
	}
	start := time.Now()
	agg, err := s.codec.Encode(aggregateID)
	if err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}
	events, err := s.loadEventsByEncodedAggregateType(ctx, agg, aggregateType, afterVersion)
	if err != nil {
		s.recordEventStoreError()
		return nil, err
	}
	s.recordEventLoaded(len(events), time.Since(start))
	return events, nil
}

func (s *SQLEventStore[ID]) loadEventsByEncodedAggregateType(ctx context.Context, encodedAggregateID any, aggregateType string, afterVersion uint64) ([]eventing.Event[ID], error) {
	query := fmt.Sprintf("SELECT id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata FROM %s WHERE aggregate_id = ? AND aggregate_type = ? AND version > ? ORDER BY version ASC", s.tableName)
	rows, err := s.db.Query(ctx, query, encodedAggregateID, aggregateType, afterVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := s.scanEvents(rows)
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (s *SQLEventStore[ID]) StreamAggregate(ctx context.Context, opts *store.AggregateStreamOptions[ID]) (*store.AggregateStreamResult[ID], error) {
	start := time.Now()
	if opts == nil {
		return nil, errors.NewCode(errors.InvalidInput, "AggregateStreamOptions cannot be nil")
	}
	aggregateType := strings.TrimSpace(opts.AggregateType)
	if aggregateType == "" {
		return nil, errors.NewCode(errors.InvalidInput, "aggregate type cannot be empty")
	}
	limit := store.NormalizeStreamLimit(opts.Limit)

	// 构建查询：按版本升序读取指定聚合的事件，限制条数
	base := fmt.Sprintf("SELECT id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata FROM %s WHERE aggregate_id = ?", s.tableName)
	agg, err := s.codec.Encode(opts.AggregateID)
	if err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}
	args := []any{agg}
	base += " AND aggregate_type = ?"
	args = append(args, aggregateType)
	base += " AND version > ? ORDER BY version ASC LIMIT ?"
	args = append(args, opts.AfterVersion, limit+1) // 多取一条判断 HasMore

	rows, err := s.db.Query(ctx, base, args...)
	if err != nil {
		s.recordEventStoreError()
		return nil, err
	}
	defer rows.Close()

	events, err := s.scanEvents(rows)
	if err != nil {
		s.recordEventStoreError()
		return nil, err
	}
	s.recordEventLoaded(len(events), time.Since(start))

	res := &store.AggregateStreamResult[ID]{Events: events}
	if len(events) == 0 {
		return res, nil
	}
	// 是否还有更多：若返回数量超过 limit，截断并标记 HasMore
	if len(events) > limit {
		res.HasMore = true
		res.Events = events[:limit]
	}
	last := res.Events[len(res.Events)-1]
	res.NextVersion = last.GetVersion()
	return res, nil
}

// IRowScanner 定义行扫描器能力接口。
type IRowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

func (s *SQLEventStore[ID]) scanEvents(rows IRowScanner) ([]eventing.Event[ID], error) {
	return s.scanEventRows(rows, false)
}

func (s *SQLEventStore[ID]) scanEventsWithGlobalPosition(rows IRowScanner) ([]eventing.Event[ID], error) {
	return s.scanEventRows(rows, true)
}

func (s *SQLEventStore[ID]) scanEventRows(rows IRowScanner, includeGlobalPosition bool) ([]eventing.Event[ID], error) {
	// 性能优化：预分配切片容量
	// 典型场景下聚合包含 10-100 个事件，预分配可避免多次扩容
	// 即使实际数量较少，也只是多分配少量内存（可被GC回收）
	events := make([]eventing.Event[ID], 0, 64)

	for rows.Next() {
		var (
			id, typ      string
			aggID        any
			aggType      string
			ver          uint64
			schema       int
			globalPos    sql.NullInt64
			ts           time.Time
			payloadJSON  string
			metadataJSON string
		)
		if includeGlobalPosition {
			if err := rows.Scan(&id, &typ, &aggID, &aggType, &ver, &schema, &globalPos, &ts, &payloadJSON, &metadataJSON); err != nil {
				return nil, err
			}
		} else {
			if err := rows.Scan(&id, &typ, &aggID, &aggType, &ver, &schema, &ts, &payloadJSON, &metadataJSON); err != nil {
				return nil, err
			}
		}

		typedAggID, err := s.codec.Decode(aggID)
		if err != nil {
			return nil, errors.Wrap(err, errors.InvalidInput, "failed to scan aggregate_id").
				WithContext("event_id", id).
				WithContext("event_type", typ)
		}

		var payload any
		if payloadJSON != "" {
			// P2 默认行为变更：读取阶段不再反序列化 payload（map/typed），
			// 改为保留 JSON bytes（json.RawMessage）以便延迟解码/升级，降低 CPU 与 GC 压力。
			payload = json.RawMessage([]byte(payloadJSON))
		}

		var metadata *messaging.Metadata
		if metadataJSON != "" {
			metadata = messaging.NewMetadata()
			decoder := json.NewDecoder(strings.NewReader(metadataJSON))
			decoder.UseNumber()
			if err := decoder.Decode(metadata); err != nil {
				return nil, errors.Wrap(err, errors.InvalidInput, "failed to unmarshal event metadata").
					WithContext("event_id", id).
					WithContext("event_type", typ)
			}
		}

		event := eventing.Event[ID]{
			Message: messaging.Message{
				ID:        id,
				Kind:      messaging.KindEvent,
				Type:      typ,
				Timestamp: ts,
				Payload:   messaging.NewPayload(payload),
				Metadata:  metadata,
			},
			AggregateID:   typedAggID,
			AggregateType: aggType,
			Version:       ver,
			SchemaVersion: schema,
		}
		if includeGlobalPosition {
			event.GlobalPosition = globalPos.Int64
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate events failed")
	}
	return events, nil
}

// HasAggregate 判断聚合。
func (s *SQLEventStore[ID]) HasAggregate(ctx context.Context, aggregateType string, aggregateID ID) (bool, error) {
	version, err := s.GetAggregateVersion(ctx, aggregateType, aggregateID)
	return version > 0, err
}

// GetAggregateVersion 从存储中查询对象。
//
// 说明：
// - GetAggregateVersion 获取聚合的当前版本。
func (s *SQLEventStore[ID]) GetAggregateVersion(ctx context.Context, aggregateType string, aggregateID ID) (uint64, error) {
	aggregateType = strings.TrimSpace(aggregateType)
	if aggregateType == "" {
		return 0, errors.NewCode(errors.InvalidInput, "aggregate type cannot be empty")
	}
	agg, err := s.codec.Encode(aggregateID)
	if err != nil {
		return 0, errors.Wrap(err, errors.InvalidInput, "invalid aggregate id")
	}
	query := fmt.Sprintf("SELECT COALESCE(MAX(version), 0) FROM %s WHERE aggregate_type = ? AND aggregate_id = ?", s.tableName)
	row := s.db.QueryRow(ctx, query, aggregateType, agg)

	var version uint64
	if err := row.Scan(&version); err != nil {
		return 0, err
	}

	return version, nil
}

// 编译期断言：SQLEventStore 满足事件存储与聚合流接口约束。
var (
	_ store.IEventStore[int64]       = (*SQLEventStore[int64])(nil)
	_ store.IEventStreamStore[int64] = (*SQLEventStore[int64])(nil)
)
