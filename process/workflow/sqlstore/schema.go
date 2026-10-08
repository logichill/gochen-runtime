package sqlstore

import (
	"context"
	"fmt"
	coredialect "gochen/db/dialect"

	"gochen/errors"
)

// CreateTables 创建流程定义表、实例表及超时巡检所需索引。
//
// 幂等：全部语句使用 IF NOT EXISTS，可在每次启动时安全调用。
func (s *SQLStore) CreateTables(ctx context.Context) error {
	for _, statement := range s.schemaStatements() {
		if _, err := s.db.Exec(ctx, statement); err != nil {
			return errors.NewCodeWithCause(errors.Database, "failed to create workflow tables", err).
				WithContext("statement", statement)
		}
	}
	return nil
}

func (s *SQLStore) schemaStatements() []string {
	definitions := s.quotedDefinitionTable()
	instances := s.quotedInstanceTable()

	switch s.dialect.Name() {
	case coredialect.NameSQLite:
		return []string{
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				definition_id TEXT NOT NULL,
				version INTEGER NOT NULL,
				name TEXT NOT NULL DEFAULT '',
				start_node_id TEXT NOT NULL,
				payload TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (definition_id, version)
			)`, definitions),
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				instance_id TEXT PRIMARY KEY,
				definition_id TEXT NOT NULL,
				definition_version INTEGER NOT NULL,
				version INTEGER NOT NULL,
				status TEXT NOT NULL,
				oldest_active_at DATETIME NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				payload TEXT NOT NULL
			)`, instances),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_scan ON %s(status, oldest_active_at)`,
				s.instanceTable, instances),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_definition ON %s(definition_id)`,
				s.instanceTable, instances),
		}

	case coredialect.NamePostgres:
		return []string{
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				definition_id VARCHAR(255) NOT NULL,
				version BIGINT NOT NULL,
				name VARCHAR(255) NOT NULL DEFAULT '',
				start_node_id VARCHAR(255) NOT NULL,
				payload TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (definition_id, version)
			)`, definitions),
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				instance_id VARCHAR(255) PRIMARY KEY,
				definition_id VARCHAR(255) NOT NULL,
				definition_version BIGINT NOT NULL,
				version BIGINT NOT NULL,
				status VARCHAR(32) NOT NULL,
				oldest_active_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				payload TEXT NOT NULL
			)`, instances),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_scan ON %s(status, oldest_active_at)`,
				s.instanceTable, instances),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_definition ON %s(definition_id)`,
				s.instanceTable, instances),
		}

	case coredialect.NameMySQL:
		return []string{
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				definition_id VARCHAR(191) NOT NULL,
				version BIGINT NOT NULL,
				name VARCHAR(255) NOT NULL DEFAULT '',
				start_node_id VARCHAR(255) NOT NULL,
				payload LONGTEXT NOT NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (definition_id, version)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, definitions),
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				instance_id VARCHAR(191) PRIMARY KEY,
				definition_id VARCHAR(191) NOT NULL,
				definition_version BIGINT NOT NULL,
				version BIGINT NOT NULL,
				status VARCHAR(32) NOT NULL,
				oldest_active_at DATETIME NULL,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				payload LONGTEXT NOT NULL,
				INDEX idx_workflow_instances_scan (status, oldest_active_at),
				INDEX idx_workflow_instances_definition (definition_id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, instances),
		}

	default:
		return nil
	}
}
