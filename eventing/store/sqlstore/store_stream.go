package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gochen/errors"
	"gochen/eventing"
	"gochen/eventing/store"
)

// StreamEvents 获取基于游标的事件流（游标分页语义）。
func (s *SQLEventStore[ID]) StreamEvents(ctx context.Context, opts *store.StreamOptions) (*store.StreamResult[ID], error) {
	if opts == nil {
		opts = &store.StreamOptions{}
	}

	var cursorTimestamp time.Time
	var cursorPosition int64
	var cursorHasPosition bool
	hasGlobalPosition, err := s.supportsGlobalPositionForStream(ctx, s.db)
	if err != nil {
		return nil, errors.NewCodeWithCause(errors.Database, "detect global event position support failed", err)
	}
	if opts.After != "" {
		selectCursor := "timestamp"
		if hasGlobalPosition {
			selectCursor = "timestamp, global_position"
		}
		row := s.db.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", selectCursor, s.tableName), opts.After)
		if hasGlobalPosition {
			var position sql.NullInt64
			err = row.Scan(&cursorTimestamp, &position)
			if position.Valid {
				cursorPosition = position.Int64
				cursorHasPosition = true
			}
		} else {
			err = row.Scan(&cursorTimestamp)
		}
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, errors.NewCode(errors.NotFound, "cursor not found").WithContext("cursor", opts.After)
			}
			return nil, err
		}
	}

	var builder strings.Builder
	selectColumns := "id, type, aggregate_id, aggregate_type, version, schema_version, timestamp, payload, metadata"
	if hasGlobalPosition {
		selectColumns = "id, type, aggregate_id, aggregate_type, version, schema_version, global_position, timestamp, payload, metadata"
	}
	args := make([]any, 0, 10)
	useCursorTimestampLookup := opts.After != "" && hasGlobalPosition && !cursorHasPosition
	fmt.Fprintf(&builder, "SELECT %s FROM %s", selectColumns, s.tableName)
	if useCursorTimestampLookup {
		fmt.Fprintf(&builder, " CROSS JOIN (SELECT timestamp AS cursor_ts FROM %s WHERE id = ?) cursor", s.tableName)
		args = append(args, opts.After)
	}
	builder.WriteString(" WHERE 1=1")

	if !opts.FromTime.IsZero() {
		builder.WriteString(" AND timestamp >= ?")
		args = append(args, opts.FromTime)
	}
	if !opts.ToTime.IsZero() {
		builder.WriteString(" AND timestamp <= ?")
		args = append(args, opts.ToTime)
	}
	if len(opts.Types) > 0 {
		builder.WriteString(" AND type IN (" + placeholders(len(opts.Types)) + ")")
		for _, t := range opts.Types {
			args = append(args, t)
		}
	}
	if len(opts.AggregateTypes) > 0 {
		builder.WriteString(" AND aggregate_type IN (" + placeholders(len(opts.AggregateTypes)) + ")")
		for _, t := range opts.AggregateTypes {
			args = append(args, t)
		}
	}
	if opts.After != "" && hasGlobalPosition {
		if cursorHasPosition {
			builder.WriteString(" AND (global_position > ? OR (global_position = ? AND id > ?))")
			args = append(args, cursorPosition, cursorPosition, opts.After)
		} else {
			builder.WriteString(" AND id <> ? AND ((global_position IS NULL AND (timestamp > cursor.cursor_ts OR (timestamp = cursor.cursor_ts AND id > ?))) OR global_position IS NOT NULL)")
			args = append(args, opts.After)
			args = append(args, opts.After)
		}
	} else if opts.After != "" && !cursorTimestamp.IsZero() {
		builder.WriteString(" AND (timestamp > ? OR (timestamp = ? AND id > ?))")
		args = append(args, cursorTimestamp, cursorTimestamp, opts.After)
	}

	// high-water-mark：仅读取到首个连续区段末位置，避免游标越过仍可能被在途事务补齐的空洞。
	// NULL 历史行不受位置上界约束，始终可读。
	if hasGlobalPosition {
		hasHorizon, horizon, err := s.streamHorizon(ctx, s.db)
		if err != nil {
			return nil, errors.NewCodeWithCause(errors.Database, "compute global position horizon failed", err)
		}
		if hasHorizon {
			if horizon > 0 {
				builder.WriteString(" AND (global_position IS NULL OR global_position <= ?)")
				args = append(args, horizon)
			} else {
				builder.WriteString(" AND global_position IS NULL")
			}
		}
	}

	if hasGlobalPosition {
		builder.WriteString(" ORDER BY CASE WHEN global_position IS NULL THEN 0 ELSE 1 END ASC, CASE WHEN global_position IS NULL THEN timestamp END ASC, CASE WHEN global_position IS NULL THEN id END ASC, global_position ASC, id ASC")
	} else {
		builder.WriteString(" ORDER BY timestamp ASC, id ASC")
	}
	limit := store.NormalizeStreamLimit(opts.Limit)
	queryLimit := limit + 1
	fmt.Fprintf(&builder, " LIMIT %d", queryLimit)

	rows, err := s.db.Query(ctx, builder.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []eventing.Event[ID]
	if hasGlobalPosition {
		events, err = s.scanEventsWithGlobalPosition(rows)
	} else {
		events, err = s.scanEvents(rows)
	}
	if err != nil {
		return nil, err
	}

	result := &store.StreamResult[ID]{
		Events: events,
	}
	if len(events) == queryLimit {
		result.HasMore = true
		result.Events = events[:limit]
	}
	if len(result.Events) > 0 {
		result.EventCursors = make([]string, len(result.Events))
		for i := range result.Events {
			result.EventCursors[i] = result.Events[i].GetID()
		}
		result.NextCursor = result.EventCursors[len(result.EventCursors)-1]
	}

	return result, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	sb := strings.Builder{}
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('?')
	}
	return sb.String()
}
