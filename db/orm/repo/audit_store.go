package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/safesql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/domain/crud"
	entitymeta "gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/gen"
)

// AuditStore 基于 gochen/db/orm 的审计记录存储实现（D11）。
//
// 说明：
// - 该实现会自动探测 ctx 中的事务会话（`orm.SessionFromContext`），确保可与 ORM Repo 在同一事务中提交；
// - 为统一不同方言与适配器行为，默认使用框架 ID 生成器写入 record.ID（避免依赖自增回填）；
// - 边界字段（tenant/scope/owner）随记录写入，读取时不依赖业务实体是否仍然存在。
type AuditStore struct {
	orm         orm.IOrm
	model       orm.IModel
	meta        *orm.ModelMeta
	resolver    *columnResolver
	quotedTable string
	idGen       gen.IGenerator[int64]
}

type auditStoreRow struct {
	ID            int64     `json:"id"`
	ResourceKind  string    `json:"resource_kind,omitempty"`
	EntityID      string    `json:"entity_id" role:"id"`
	Action        string    `json:"action"`
	Operator      string    `json:"operator"`
	Initiator     string    `json:"initiator,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	TenantID      string    `json:"tenant_id,omitempty" role:"tenant"`
	ScopeID       int64     `json:"scope_id,omitempty" role:"scope"`
	OwnerID       string    `json:"owner_id,omitempty" role:"owner"`
	Timestamp     time.Time `json:"timestamp"`
	Changes       string    `json:"changes,omitempty"`
	Metadata      string    `json:"metadata,omitempty"`
}

// auditRowColumns 是 auditStoreRow 的列数，用于把占位参数上限折算成行数。
const auditRowColumns = 14

// 批量写能力由 audited Application 按接口断言发现：
// 签名漂移只会让它静默退回逐条写入，不会有任何报错，故用编译期断言钉死。
var (
	_ audited.IAuditStore      = (*AuditStore)(nil)
	_ audited.IAuditQueryStore = (*AuditStore)(nil)
	_ audited.IBulkAuditStore  = (*AuditStore)(nil)
	_ crud.IDataSourceProvider = (*AuditStore)(nil)
)

// maxRowsPerInsert 返回单条多值 INSERT 可携带的审计行数。
//
// 预算来自底层数据库（db.IBindParameterLimitProvider），与仓储、ORM 共用同一来源。
func (s *AuditStore) maxRowsPerInsert() int {
	budget := dialect.DefaultMaxBindParameters
	if s != nil && s.orm != nil && s.orm.Database() != nil {
		if provider, ok := s.orm.Database().(db.IBindParameterLimitProvider); ok {
			budget = dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
		}
	}
	if rows := budget / auditRowColumns; rows > 0 {
		return rows
	}
	return 1
}

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
		meta:        &orm.ModelMeta{ModelFactory: orm.NewModelFactory[auditStoreRow](), Table: tableName},
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
	metadata, err := entitymeta.DescribeType(reflect.TypeOf(&auditStoreRow{}))
	if err != nil {
		return nil, err
	}
	resolver, err := newColumnResolver(metadata, model.Meta(), model.Dialect())
	if err != nil {
		return nil, err
	}
	store.resolver = resolver
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

// DataSourceID 实现 data source provider（D17）。
func (s *AuditStore) DataSourceID() string {
	if s == nil || s.orm == nil {
		return ""
	}
	id, _ := orm.DatasourceIDOf(s.orm)
	return id
}

func (s *AuditStore) modelFor(ctx context.Context) (orm.IModel, error) {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		// 审计记录是安全证据，归属校验与仓储同一口径：异源或无法验证的事务
		// 会话直接拒绝，防止审计流水被写进别的数据源。
		if err := ensureSessionDatasourceFor(s.orm, session, s.meta.Table); err != nil {
			return nil, err
		}
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

// SaveAuditRecords 实现 audited.IBulkAuditStore：整批审计记录一次性写入。
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
	rows := make([]auditStoreRow, 0, len(records))
	for i := range records {
		row, err := s.buildAuditRow(records[i])
		if err != nil {
			return errors.Wrap(err, errors.Code(err), "invalid audit record in bulk").
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
func (s *AuditStore) buildAuditRow(record audited.AuditRecord) (auditStoreRow, error) {
	if strings.TrimSpace(record.EntityID) == "" {
		return auditStoreRow{}, errors.NewCode(errors.InvalidInput, "entityID cannot be empty")
	}
	if strings.TrimSpace(record.Action) == "" {
		return auditStoreRow{}, errors.NewCode(errors.InvalidInput, "action cannot be empty")
	}
	if strings.TrimSpace(record.Operator) == "" {
		return auditStoreRow{}, errors.NewCode(errors.InvalidInput, "operator cannot be empty")
	}

	if record.ID == 0 {
		id, err := s.idGen.Next()
		if err != nil {
			return auditStoreRow{}, err
		}
		record.ID = id
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	changesJSON := "{}"
	if len(record.Changes) > 0 {
		if !json.Valid(record.Changes) {
			return auditStoreRow{}, errors.NewCode(errors.InvalidInput, "invalid audit record changes")
		}
		changesJSON = string(record.Changes)
	}

	metadataJSON := "{}"
	if len(record.Metadata) > 0 {
		if !json.Valid(record.Metadata) {
			return auditStoreRow{}, errors.NewCode(errors.InvalidInput, "invalid audit record metadata")
		}
		metadataJSON = string(record.Metadata)
	}

	return auditStoreRow{
		ID:            record.ID,
		ResourceKind:  strings.TrimSpace(record.ResourceKind),
		EntityID:      record.EntityID,
		Action:        record.Action,
		Operator:      record.Operator,
		Initiator:     record.Initiator,
		Reason:        record.Reason,
		CorrelationID: record.CorrelationID,
		TenantID:      record.TenantID,
		ScopeID:       record.ScopeID,
		OwnerID:       record.OwnerID,
		Timestamp:     record.Timestamp,
		Changes:       changesJSON,
		Metadata:      metadataJSON,
	}, nil
}

// ListAuditRecords 按实体维度读取审计轨迹（D11）。
//
// 规约只能引用审计记录自身的边界角色（@id / @scope / @owner），
// 不接受 Named 或其他字段；entityID 为空返回 InvalidInput。
func (s *AuditStore) ListAuditRecords(ctx context.Context, entityID string, offset, limit int, spec spec.Spec) ([]audited.AuditRecord, error) {
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
	condition, err := auditSpecCondition(spec, s.resolver)
	if err != nil {
		return nil, err
	}
	m, err := s.modelFor(ctx)
	if err != nil {
		return nil, err
	}
	opts := []orm.QueryOption{
		orm.WithWhere("entity_id = ?", entityID),
		orm.WithWhere("tenant_id = ?", contextx.TenantID(ctx)),
	}
	if kind := strings.TrimSpace(audited.AuditResourceKind(ctx)); kind != "" {
		opts = append(opts, orm.WithWhere("resource_kind = ?", kind))
	}
	if condition.Expr != "" {
		opts = append(opts, orm.WithWhere(condition.Expr, condition.Args...))
	}
	opts = append(opts,
		orm.WithOrderBy("timestamp", true),
		orm.WithOrderBy("id", true),
		orm.WithOffset(offset),
		orm.WithLimit(limit),
	)

	var rows []auditStoreRow
	if err := m.Find(ctx, &rows, opts...); err != nil {
		return nil, err
	}
	return auditRowsToRecords(rows)
}

// auditSpecCondition 把 audit_read 规约翻译成 SQL 条件。
func auditSpecCondition(s spec.Spec, resolver *columnResolver) (orm.Condition, error) {
	if err := s.Err(); err != nil {
		return orm.Condition{}, errors.Wrap(err, errors.Internal, "invalid spec")
	}
	for _, field := range s.Fields() {
		switch field {
		case string(spec.RoleID), string(spec.RoleScope), string(spec.RoleOwner):
		default:
			return orm.Condition{}, errors.NewCode(errors.Internal,
				"audit spec may only reference the id, scope or owner roles").
				WithContext("field", field)
		}
	}
	return translateSpec(s, resolver, nil, false)
}

func auditRowsToRecords(rows []auditStoreRow) ([]audited.AuditRecord, error) {
	out := make([]audited.AuditRecord, 0, len(rows))
	for i := range rows {
		changes, metadata, err := decodeAuditPayloads(rows[i].ID, rows[i].Changes, rows[i].Metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, audited.AuditRecord{
			ID:            rows[i].ID,
			ResourceKind:  rows[i].ResourceKind,
			EntityID:      rows[i].EntityID,
			Action:        rows[i].Action,
			Operator:      rows[i].Operator,
			Initiator:     rows[i].Initiator,
			Reason:        rows[i].Reason,
			CorrelationID: rows[i].CorrelationID,
			TenantID:      rows[i].TenantID,
			ScopeID:       rows[i].ScopeID,
			OwnerID:       rows[i].OwnerID,
			Timestamp:     rows[i].Timestamp,
			Changes:       changes,
			Metadata:      metadata,
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
