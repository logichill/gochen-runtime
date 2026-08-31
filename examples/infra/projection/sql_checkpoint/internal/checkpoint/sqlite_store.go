package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"gochen/db"
	"gochen/errors"
	"gochen/eventing/projection"
	"time"
)

// SQLiteCheckpointStore 用 SQLite 持久化投影检查点，便于演示重启恢复。
type SQLiteCheckpointStore struct {
	db        db.IDatabase
	tableName string
}

// NewSQLiteCheckpointStore 创建一个使用指定表名的 SQLite 检查点存储。
func NewSQLiteCheckpointStore(db db.IDatabase, table string) *SQLiteCheckpointStore {
	if table == "" {
		table = "projection_checkpoints"
	}
	return &SQLiteCheckpointStore{db: db, tableName: table}
}

// EnsureTable 确保检查点表存在。
func (s *SQLiteCheckpointStore) EnsureTable(ctx context.Context) error {
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	projection_name TEXT PRIMARY KEY,
	position INTEGER NOT NULL DEFAULT 0,
	last_event_id TEXT NOT NULL DEFAULT '',
	last_event_time DATETIME NULL,
	updated_at DATETIME NOT NULL
)`, s.tableName)
	_, err := s.db.Exec(ctx, ddl)
	return err
}

// Load 读取指定投影的最近检查点。
func (s *SQLiteCheckpointStore) Load(ctx context.Context, name string) (*projection.Checkpoint, error) {
	q := fmt.Sprintf(`SELECT projection_name, position, last_event_id, last_event_time, updated_at FROM %s WHERE projection_name = ?`, s.tableName)
	row := s.db.QueryRow(ctx, q, name)
	var cp projection.Checkpoint
	var last sql.NullTime
	if err := row.Scan(&cp.ProjectionName, &cp.Position, &cp.LastEventID, &last, &cp.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.NewCode(errors.NotFound, "checkpoint not found").
				WithContext("projection_name", name)
		}
		return nil, fmt.Errorf("load checkpoint: %w", err)
	}
	if last.Valid {
		cp.LastEventTime = last.Time
	}
	return &cp, nil
}

// Save 持久化检查点，并用 UPSERT 覆盖同名投影的旧记录。
func (s *SQLiteCheckpointStore) Save(ctx context.Context, cp *projection.Checkpoint) error {
	if cp == nil {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("reason", "checkpoint is nil")
	}
	if !cp.IsValid() {
		return errors.NewCode(errors.InvalidInput, "invalid checkpoint").
			WithContext("projection_name", cp.ProjectionName).
			WithContext("position", cp.Position)
	}
	cp.UpdatedAt = time.Now()
	// SQLite UPSERT
	q := fmt.Sprintf(`
INSERT INTO %s (projection_name, position, last_event_id, last_event_time, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(projection_name) DO UPDATE SET
	position=excluded.position,
	last_event_id=excluded.last_event_id,
	last_event_time=excluded.last_event_time,
	updated_at=excluded.updated_at`, s.tableName)
	_, err := s.db.Exec(ctx, q, cp.ProjectionName, cp.Position, cp.LastEventID, cp.LastEventTime, cp.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return nil
}

// Delete 删除指定投影对应的检查点记录。
func (s *SQLiteCheckpointStore) Delete(ctx context.Context, name string) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE projection_name = ?`, s.tableName)
	_, err := s.db.Exec(ctx, q, name)
	if err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return nil
}

// 编译期断言：确保实现 projection.ICheckpointStore。
var _ projection.ICheckpointStore = (*SQLiteCheckpointStore)(nil)
