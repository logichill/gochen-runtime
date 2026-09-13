package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gochen-runtime/db/sql/safesql"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/errors"
	"gochen/gen"
)

// AuditStore 基于 gochen/db/orm 的审计记录存储实现。
//
// 说明：
// - 该实现会自动探测 ctx 中的事务会话（`orm.SessionFromContext`），确保可与 ORM Repo 在同一事务中提交；
// - 为统一不同方言与适配器行为，默认使用框架 ID 生成器写入 record.ID（避免依赖自增回填）。
type AuditStore struct {
	orm         orm.IOrm
	model       orm.IModel
	meta        *orm.ModelMeta
	quotedTable string
	idGen       gen.IGenerator[int64]
}

type auditStoreScopedRow struct {
	ID           int64     `json:"id"`
	ResourceKind string    `json:"resource_kind"`
	EntityID     string    `json:"entity_id"`
	Operation    string    `json:"operation"`
	Operator     string    `json:"operator"`
	Timestamp    time.Time `json:"timestamp"`
	Changes      string    `json:"changes"`
	Metadata     string    `json:"metadata"`
}

// auditRowColumns 是 auditStoreScopedRow 的列数，用于把占位参数上限折算成行数。
const auditRowColumns = 8

// maxRowsPerInsert 返回单条多值 INSERT 可携带的审计行数。
//
// 预算来自底层数据库（db.IBindParameterLimitProvider），与仓储、ORM 共用同一来源。
func (s *AuditStore) maxRowsPerInsert() int {
	budget := dialect.DefaultMaxBindParameters
	if s != nil && s.orm != nil {
		if provider, ok := s.orm.Database().(db.IBindParameterLimitProvider); ok {
			budget = dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
		}
	}
	if rows := budget / auditRowColumns; rows > 0 {
		return rows
	}
	return 1
}

// 批量写能力由 audited Application 按接口断言发现（audit_trail.go）：
// 签名漂移只会让它静默退回逐条写入，不会有任何报错，故用编译期断言钉死。
var _ audited.IBatchAuditStore = (*AuditStore)(nil)

// NewAuditStore 创建审计存储。
func NewAuditStore(ormEngine orm.IOrm, tableName string, idGenerator gen.IGenerator[int64]) (*AuditStore, error) {
	if ormEngine == nil {
		return nil, errors.NewCode(errors.InvalidInput, "orm cannot be nil")
	}
	if idGenerator == nil || isNilAuditIDGenerator(idGenerator) {
		return nil, errors.NewCode(errors.InvalidInput, "audit id generator is required")
	}
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return nil, errors.NewCode(errors.InvalidInput, "table name cannot be empty")
	}
	database := ormEngine.Database()
	quotedTable, err := safesql.QuoteQualifiedIdentifier(dialect.FromDatabase(database), tableName, "", "audit table name", 2)
	if err != nil {
		return nil, errors.NewCode(errors.InvalidInput, "invalid audit table name").
			WithContext("table", tableName).
			WithContext("cause", err.Error())
	}

	store := &AuditStore{
		orm:         ormEngine,
		meta:        &orm.ModelMeta{ModelFactory: orm.NewModelFactory[auditStoreScopedRow](), Table: tableName},
		quotedTable: quotedTable,
		idGen:       idGenerator,
	}
	if err := store.ensureResourceKindColumn(context.Background()); err != nil {
		return nil, err
	}
	model, err := ormEngine.Model(store.meta)
	if err != nil {
		return nil, err
	}
	store.model = model
	return store, nil
}

func isNilAuditIDGenerator(generator gen.IGenerator[int64]) bool {
	value := reflect.ValueOf(generator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (s *AuditStore) modelFor(ctx context.Context) (orm.IModel, error) {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		return session.Model(s.meta)
	}
	return s.model, nil
}

func (s *AuditStore) ensureResourceKindColumn(ctx context.Context) error {
	if s == nil {
		return nil
	}
	hasResourceKind, err := s.detectResourceKindColumn(ctx)
	if err != nil {
		return err
	}
	if !hasResourceKind {
		return errors.NewCode(errors.InvalidInput, "audit table requires resource_kind column").
			WithContext("table", s.meta.Table)
	}
	return nil
}

func (s *AuditStore) detectResourceKindColumn(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	database := s.orm.Database()
	if database == nil {
		return false, nil
	}
	table := strings.TrimSpace(s.quotedTable)
	if table == "" {
		table = dialect.FromDatabase(database).QuoteIdentifier(s.meta.Table)
	}
	rows, err := database.Query(ctx, fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", table))
	if err != nil {
		return false, errors.Wrap(err, errors.Database, "detect audit resource_kind column failed").
			WithContext("table", s.meta.Table)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	for _, column := range columns {
		if strings.EqualFold(column, "resource_kind") {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// SaveAuditRecord 写入一条审计记录。
func (s *AuditStore) SaveAuditRecord(ctx context.Context, record audited.AuditRecord) (int64, error) {
	if ctx == nil {
		return 0, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if s == nil {
		return 0, errors.NewCode(errors.Internal, "audit store is nil")
	}
	row, err := s.buildAuditRow(record)
	if err != nil {
		return 0, err
	}
	m, err := s.modelFor(ctx)
	if err != nil {
		return 0, err
	}
	if err := m.Create(ctx, &row); err != nil {
		return 0, err
	}
	return row.ID, nil
}

// SaveAuditRecords 实现 audited.IBatchAuditStore：整批审计记录一次性写入。
//
// audited Application 在批量写路径下会为每条实体产出一条审计记录；逐条落库
// 意味着一次批量写要多打 N 个来回。这里合并成多值 INSERT，按占位参数上限分片。
//
// 校验与单条路径共用 buildAuditRow：审计是安全证据，两条写入路径的校验口径
// 必须完全一致，任何一条记录不合法即整批失败，绝不部分留痕。
func (s *AuditStore) SaveAuditRecords(ctx context.Context, records []audited.AuditRecord) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if s == nil {
		return errors.NewCode(errors.Internal, "audit store is nil")
	}
	if len(records) == 0 {
		return nil
	}
	rows := make([]auditStoreScopedRow, 0, len(records))
	for i := range records {
		row, err := s.buildAuditRow(records[i])
		if err != nil {
			return errors.Wrap(err, errors.Code(err), "invalid audit record in batch").
				WithContext("index", i)
		}
		rows = append(rows, row)
	}
	m, err := s.modelFor(ctx)
	if err != nil {
		return err
	}
	for _, chunk := range chunkSlice(rows, s.maxRowsPerInsert()) {
		items := make([]any, 0, len(chunk))
		for i := range chunk {
			items = append(items, &chunk[i])
		}
		if err := m.Create(ctx, items...); err != nil {
			return err
		}
	}
	return nil
}

// buildAuditRow 校验并规范化一条审计记录，产出待写入的行。
func (s *AuditStore) buildAuditRow(record audited.AuditRecord) (auditStoreScopedRow, error) {
	if strings.TrimSpace(record.EntityID) == "" {
		return auditStoreScopedRow{}, errors.NewCode(errors.InvalidInput, "entityID cannot be empty")
	}
	if strings.TrimSpace(string(record.Operation)) == "" {
		return auditStoreScopedRow{}, errors.NewCode(errors.InvalidInput, "operation cannot be empty")
	}
	if strings.TrimSpace(record.Operator) == "" {
		return auditStoreScopedRow{}, errors.NewCode(errors.InvalidInput, "operator cannot be empty")
	}

	if record.ID == 0 {
		id, err := s.idGen.Next()
		if err != nil {
			return auditStoreScopedRow{}, err
		}
		record.ID = id
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	changesJSON := "{}"
	if len(record.Changes) > 0 {
		if !json.Valid(record.Changes) {
			return auditStoreScopedRow{}, errors.NewCode(errors.InvalidInput, "invalid audit record changes")
		}
		changesJSON = string(record.Changes)
	}

	metadataJSON := "{}"
	if len(record.Metadata) > 0 {
		if !json.Valid(record.Metadata) {
			return auditStoreScopedRow{}, errors.NewCode(errors.InvalidInput, "invalid audit record metadata")
		}
		metadataJSON = string(record.Metadata)
	}

	return auditStoreScopedRow{
		ID:           record.ID,
		ResourceKind: strings.TrimSpace(record.ResourceKind),
		EntityID:     record.EntityID,
		Operation:    string(record.Operation),
		Operator:     record.Operator,
		Timestamp:    record.Timestamp,
		Changes:      changesJSON,
		Metadata:     metadataJSON,
	}, nil
}

// ListAuditRecordsByEntity 从存储中查询数据。
//
// 参数：
// - offset：分页偏移量（从 0 开始）
// - limit：分页大小（最大返回条数）
func (s *AuditStore) ListAuditRecordsByEntity(ctx context.Context, entityID string, offset, limit int) ([]audited.AuditRecord, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if s == nil {
		return nil, errors.NewCode(errors.Internal, "audit store is nil")
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, errors.NewCode(errors.InvalidInput, "entityID cannot be empty")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	m, err := s.modelFor(ctx)
	if err != nil {
		return nil, err
	}
	opts := []orm.QueryOption{
		orm.WithWhere("entity_id = ?", entityID),
		orm.WithOrderBy("timestamp", true),
		orm.WithOrderBy("id", true),
		orm.WithOffset(offset),
		orm.WithLimit(limit),
	}
	if kind := strings.TrimSpace(audited.AuditResourceKind(ctx)); kind != "" {
		opts = append([]orm.QueryOption{orm.WithWhere("resource_kind = ?", kind)}, opts...)
	}

	var rows []auditStoreScopedRow
	if err := m.Find(ctx, &rows, opts...); err != nil {
		return nil, err
	}
	return auditScopedRowsToRecords(rows)
}

func auditScopedRowsToRecords(rows []auditStoreScopedRow) ([]audited.AuditRecord, error) {
	out := make([]audited.AuditRecord, 0, len(rows))
	for i := range rows {
		changes, metadata, err := decodeAuditPayloads(rows[i].ID, rows[i].Changes, rows[i].Metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, audited.AuditRecord{
			ID:           rows[i].ID,
			ResourceKind: rows[i].ResourceKind,
			EntityID:     rows[i].EntityID,
			Operation:    audited.AuditOperation(rows[i].Operation),
			Operator:     rows[i].Operator,
			Timestamp:    rows[i].Timestamp,
			Changes:      changes,
			Metadata:     metadata,
		})
	}
	return out, nil
}

func decodeAuditPayloads(id int64, changesRaw, metadataRaw string) (json.RawMessage, json.RawMessage, error) {
	changes, err := decodeAuditJSON(id, changesRaw, "changes")
	if err != nil {
		return nil, nil, err
	}
	metadata, err := decodeAuditJSON(id, metadataRaw, "metadata")
	if err != nil {
		return nil, nil, err
	}
	return changes, metadata, nil
}

func decodeAuditJSON(id int64, raw, field string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	b := []byte(raw)
	if !json.Valid(b) {
		return nil, errors.NewCode(errors.Internal, "failed to decode audit record "+field).
			WithContext("audit_id", id)
	}
	return b, nil
}
