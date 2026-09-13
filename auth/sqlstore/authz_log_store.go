package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gochen-runtime/internal/normalize"
	"gochen/auth/governance"
	"gochen/db"
	"gochen/errors"
	"gochen/gen"
)

// AuthzLogStore 表示基于 db.IDatabase 的 authz log 持久化实现。
type AuthzLogStore struct {
	db          db.IDatabase
	tableName   string
	idGenerator gen.IGenerator[string]
}

// NewAuthzLogStore 创建 SQL authz log store。
func NewAuthzLogStore(database db.IDatabase, tableName string, idGenerator gen.IGenerator[string]) (*AuthzLogStore, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "authz log database cannot be nil")
	}
	if idGenerator == nil || isNilStringGenerator(idGenerator) {
		return nil, errors.NewCode(errors.InvalidInput, "authz log entry id generator is required")
	}
	normalizedTableName, err := normalizeSQLTableName(database, tableName, "authz_logs")
	if err != nil {
		return nil, err
	}
	return &AuthzLogStore{
		db:          database,
		tableName:   normalizedTableName,
		idGenerator: idGenerator,
	}, nil
}

// SaveAuthzLogEntry 保存 authz 日志。
func (s *AuthzLogStore) SaveAuthzLogEntry(ctx context.Context, entry governance.AuthzLogEntry) error {
	entry = governance.SanitizeAuthzLogEntry(entry)
	entry.Resources = governance.SanitizeLoggedResources(ctx, entry.Resources)
	entry, err := normalizeAuthzLogEntry(entry, s.idGenerator)
	if err != nil {
		return err
	}
	if entry.ID == "" {
		return errors.NewCode(errors.InvalidInput, "authz log entry id is required")
	}
	resourcesJSON, err := authzLogEntryResourcesJSON(entry)
	if err != nil {
		return err
	}
	executionJSON, err := authzLogEntryExecutionJSON(entry)
	if err != nil {
		return err
	}
	metadataJSON, err := authzLogEntryMetadataJSON(entry)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s
		(id, entry_type, decision_id, principal_id, permission, operation, effect, reason_code, snapshot_version, snapshot_key, consistency, latency_ms, cache_hit, resources_json, execution_json, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tableName)
	if _, err := s.db.Exec(ctx, query,
		entry.ID,
		string(entry.Type),
		entry.DecisionID,
		entry.PrincipalID,
		entry.Action,
		entry.Operation,
		entry.Effect,
		entry.ReasonCode,
		entry.SnapshotVersion,
		entry.SnapshotKey,
		string(entry.Consistency),
		entry.LatencyMs,
		entry.CacheHit,
		resourcesJSON,
		executionJSON,
		metadataJSON,
		entry.Timestamp,
	); err != nil {
		return errors.Wrap(err, errors.Database, "insert authz log entry failed").WithContext("entry_id", entry.ID)
	}
	return nil
}

// ListAuthzLogEntries 列出 authz 日志。
func (s *AuthzLogStore) ListAuthzLogEntries(ctx context.Context, entryType governance.AuthzLogEntryType, limit int) ([]governance.AuthzLogEntry, error) {
	query := fmt.Sprintf(`SELECT id, entry_type, decision_id, principal_id, permission, operation, effect, reason_code, snapshot_version, snapshot_key, consistency, latency_ms, cache_hit, resources_json, execution_json, metadata_json, created_at FROM %s`, s.tableName)
	args := make([]any, 0, 2)
	if entryType = normalizeAuthzLogEntryType(entryType); entryType != "" {
		query += ` WHERE entry_type = ?`
		args = append(args, string(entryType))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, errors.Database, "list authz log entries failed")
	}
	defer rows.Close()

	result := make([]governance.AuthzLogEntry, 0)
	for rows.Next() {
		var (
			entry         governance.AuthzLogEntry
			entryTypeRaw  string
			consistency   string
			resourcesJSON string
			executionJSON string
			metadataJSON  string
			cacheHit      bool
		)
		if err := rows.Scan(
			&entry.ID,
			&entryTypeRaw,
			&entry.DecisionID,
			&entry.PrincipalID,
			&entry.Action,
			&entry.Operation,
			&entry.Effect,
			&entry.ReasonCode,
			&entry.SnapshotVersion,
			&entry.SnapshotKey,
			&consistency,
			&entry.LatencyMs,
			&cacheHit,
			&resourcesJSON,
			&executionJSON,
			&metadataJSON,
			&entry.Timestamp,
		); err != nil {
			return nil, errors.Wrap(err, errors.Database, "scan authz log entry failed")
		}
		entry.Type = normalizeAuthzLogEntryType(governance.AuthzLogEntryType(entryTypeRaw))
		entry.Consistency = normalizeConsistencyMode(governance.ConsistencyMode(consistency))
		entry.CacheHit = cacheHit
		if strings.TrimSpace(resourcesJSON) != "" && resourcesJSON != "[]" {
			if err := json.Unmarshal([]byte(resourcesJSON), &entry.Resources); err != nil {
				return nil, errors.Wrap(err, errors.Internal, "decode authz log resources failed").WithContext("entry_id", entry.ID)
			}
		}
		if strings.TrimSpace(executionJSON) != "" && executionJSON != "{}" {
			if err := json.Unmarshal([]byte(executionJSON), &entry.Execution); err != nil {
				return nil, errors.Wrap(err, errors.Internal, "decode authz log execution failed").WithContext("entry_id", entry.ID)
			}
		}
		if strings.TrimSpace(metadataJSON) != "" && metadataJSON != "{}" {
			meta := make(map[string]any)
			if err := json.Unmarshal([]byte(metadataJSON), &meta); err != nil {
				return nil, errors.Wrap(err, errors.Internal, "decode authz log metadata failed").WithContext("entry_id", entry.ID)
			}
			if matchedRules, ok := meta["matched_rules"].([]any); ok && len(matchedRules) > 0 {
				entry.MatchedRules = make([]string, 0, len(matchedRules))
				for _, rule := range matchedRules {
					if text, ok := rule.(string); ok && strings.TrimSpace(text) != "" {
						entry.MatchedRules = append(entry.MatchedRules, strings.TrimSpace(text))
					}
				}
				delete(meta, "matched_rules")
			}
			if derived, ok := meta[governance.SnapshotVersionDerivedMetadataKey].(bool); ok {
				entry.SnapshotVersionDerived = derived
				delete(meta, governance.SnapshotVersionDerivedMetadataKey)
			}
			if len(meta) > 0 {
				entry.Metadata = meta
			}
		}
		normalized, err := normalizeAuthzLogEntry(entry, s.idGenerator)
		if err != nil {
			return nil, err
		}
		entry = normalized
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.Database, "iterate authz log entries failed")
	}
	return result, nil
}

// DeleteAuthzLogEntriesBefore 删除早于 cutoff 的日志。
func (s *AuthzLogStore) DeleteAuthzLogEntriesBefore(ctx context.Context, cutoff time.Time) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE created_at < ?`, s.tableName)
	if _, err := s.db.Exec(ctx, query, cutoff); err != nil {
		return errors.Wrap(err, errors.Database, "delete authz log entries failed")
	}
	return nil
}

func normalizeAuthzLogEntry(entry governance.AuthzLogEntry, idGenerator gen.IGenerator[string]) (governance.AuthzLogEntry, error) {
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Type = normalizeAuthzLogEntryType(entry.Type)
	if entry.Type == "" {
		entry.Type = governance.AuthzLogEntryTypeDecision
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if entry.ID == "" {
		id, err := nextAuthzLogID(idGenerator)
		if err != nil {
			return entry, err
		}
		entry.ID = id
	}
	entry.DecisionID = strings.TrimSpace(entry.DecisionID)
	entry.PrincipalID = strings.TrimSpace(entry.PrincipalID)
	entry.Action = strings.TrimSpace(entry.Action)
	entry.Operation = strings.TrimSpace(entry.Operation)
	entry.Effect = strings.TrimSpace(entry.Effect)
	entry.ReasonCode = strings.TrimSpace(entry.ReasonCode)
	entry.SnapshotVersion = strings.TrimSpace(entry.SnapshotVersion)
	entry.SnapshotKey = strings.TrimSpace(entry.SnapshotKey)
	entry.Consistency = normalizeConsistencyMode(entry.Consistency)
	entry.MatchedRules = normalize.Strings(entry.MatchedRules)
	entry.Execution = normalizeExecutionMetadata(entry.Execution)
	if len(entry.Resources) > 0 {
		resources := make([]governance.AuthzLoggedResource, 0, len(entry.Resources))
		for _, resource := range entry.Resources {
			resources = append(resources, normalizeLoggedResource(resource))
		}
		entry.Resources = resources
	} else {
		entry.Resources = nil
	}
	if len(entry.Metadata) == 0 {
		entry.Metadata = nil
	}
	return entry, nil
}

func nextAuthzLogID(generator gen.IGenerator[string]) (string, error) {
	if generator == nil || isNilStringGenerator(generator) {
		return "", errors.NewCode(errors.Internal, "authz log id generator is nil")
	}
	id, err := generator.Next()
	if err != nil {
		return "", errors.NewCodeWithCause(
			errors.Dependency,
			"generate authz log id failed",
			err,
		)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.NewCode(errors.Internal, "generated authz log id is empty")
	}
	return id, nil
}

func isNilStringGenerator(generator gen.IGenerator[string]) bool {
	value := reflect.ValueOf(generator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func normalizeLoggedResource(resource governance.AuthzLoggedResource) governance.AuthzLoggedResource {
	resource.Kind = strings.TrimSpace(resource.Kind)
	resource.ID = strings.TrimSpace(resource.ID)
	resource.ManagedScopeID = normalize.PositiveID(resource.ManagedScopeID)
	resource.TenantID = strings.TrimSpace(resource.TenantID)
	resource.OwnerID = strings.TrimSpace(resource.OwnerID)
	resource.Revision = strings.TrimSpace(resource.Revision)
	return resource
}

func normalizeAuthzLogEntryType(entryType governance.AuthzLogEntryType) governance.AuthzLogEntryType {
	switch governance.AuthzLogEntryType(strings.TrimSpace(string(entryType))) {
	case governance.AuthzLogEntryTypeDecision:
		return governance.AuthzLogEntryTypeDecision
	default:
		return ""
	}
}

func normalizeConsistencyMode(mode governance.ConsistencyMode) governance.ConsistencyMode {
	switch governance.ConsistencyMode(strings.TrimSpace(string(mode))) {
	case governance.ConsistencyModeStrong:
		return governance.ConsistencyModeStrong
	case governance.ConsistencyModeBoundedStaleness:
		return governance.ConsistencyModeBoundedStaleness
	default:
		return governance.ConsistencyModeUnspecified
	}
}

func normalizeExecutionMetadata(metadata governance.ExecutionMetadata) governance.ExecutionMetadata {
	metadata.InitiatorID = strings.TrimSpace(metadata.InitiatorID)
	metadata.ActorID = strings.TrimSpace(metadata.ActorID)
	metadata.RequestID = strings.TrimSpace(metadata.RequestID)
	metadata.DecisionID = strings.TrimSpace(metadata.DecisionID)
	metadata.EventID = strings.TrimSpace(metadata.EventID)
	metadata.JobID = strings.TrimSpace(metadata.JobID)
	return metadata
}

func authzLogEntryResourcesJSON(entry governance.AuthzLogEntry) (string, error) {
	if len(entry.Resources) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(entry.Resources)
	if err != nil {
		return "", errors.Wrap(err, errors.InvalidInput, "marshal authz log resources failed").
			WithContext("entry_id", entry.ID)
	}
	return string(b), nil
}

func authzLogEntryExecutionJSON(entry governance.AuthzLogEntry) (string, error) {
	b, err := json.Marshal(entry.Execution)
	if err != nil {
		return "", errors.Wrap(err, errors.InvalidInput, "marshal authz log execution failed").
			WithContext("entry_id", entry.ID)
	}
	return string(b), nil
}

func authzLogEntryMetadataJSON(entry governance.AuthzLogEntry) (string, error) {
	if len(entry.Metadata) == 0 && len(entry.MatchedRules) == 0 && !entry.SnapshotVersionDerived {
		return "{}", nil
	}
	payload := map[string]any{}
	for k, v := range entry.Metadata {
		payload[k] = v
	}
	if len(entry.MatchedRules) > 0 {
		payload["matched_rules"] = append([]string(nil), entry.MatchedRules...)
	}
	if entry.SnapshotVersionDerived {
		payload[governance.SnapshotVersionDerivedMetadataKey] = true
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, errors.InvalidInput, "marshal authz log metadata failed").
			WithContext("entry_id", entry.ID)
	}
	return string(b), nil
}
