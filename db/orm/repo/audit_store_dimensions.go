package repo

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/safesql"
	"gochen/app"
	"gochen/audit"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/crud"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/gen"
)

// AuditResource 绑定资源的有效契约、历史维度列与隔离取值来源。
type AuditResource struct {
	Resource         string
	Execution        app.ExecutionDescriptor
	Columns          map[meta.Dimension]string
	IsolationColumn  string
	IsolationResolve IsolationResolveFunc
}

// AuditResource 返回与业务仓储共用契约的审计配置。
func (r *Repo[T, ID]) AuditResource(columns map[meta.Dimension]string) AuditResource {
	return AuditResource{Resource: r.resourceKind, Execution: r.execution, Columns: columns,
		IsolationColumn: r.access.isolation, IsolationResolve: r.isolationResolve}
}

type auditBinding struct {
	config    AuditResource
	rowType   reflect.Type
	modelMeta *orm.ModelMeta
	model     orm.IModel
	resolver  *columnResolver
}

// AuditStore 按记录自身的完整维度事实过滤历史，并复用业务事务。
type AuditStore struct {
	orm       orm.IOrm
	table     string
	idGen     gen.IGenerator[int64]
	resources map[string]*auditBinding
}

type auditStoreRow struct {
	ID            int64     `json:"id"`
	ResourceKind  string    `json:"resource_kind"`
	EntityID      string    `json:"entity_id"`
	Action        string    `json:"action"`
	Operator      string    `json:"operator"`
	Initiator     string    `json:"initiator"`
	Reason        string    `json:"reason"`
	CorrelationID string    `json:"correlation_id"`
	OperationID   string    `json:"operation_id"`
	Timestamp     time.Time `json:"timestamp"`
	Changes       string    `json:"changes"`
	Metadata      string    `json:"metadata"`
	Dimensions    string    `json:"dimensions"`
}

var (
	_ audit.IAuditStore        = (*AuditStore)(nil)
	_ audit.IAuditQueryStore   = (*AuditStore)(nil)
	_ audit.IBulkAuditStore    = (*AuditStore)(nil)
	_ crud.IDataSourceProvider = (*AuditStore)(nil)
)

// NewAuditStore 在构造期验证全部资源映射与实际审计表列。
func NewAuditStore(engine orm.IOrm, table string, ids gen.IGenerator[int64], resources ...AuditResource) (*AuditStore, error) {
	if engine == nil || ids == nil || isNilAuditIDGenerator(ids) || len(resources) == 0 {
		return nil, errors.NewCode(errors.InvalidInput, "audit store requires orm, id generator and resource mappings")
	}
	if engine.Database() == nil {
		return nil, errors.NewCode(errors.InvalidInput, "audit store requires database column inspection")
	}
	quoted, err := safesql.QuoteQualifiedIdentifier(dialect.FromDatabase(engine.Database()), table, "", "audit table", 2)
	if err != nil {
		return nil, err
	}
	rows, err := engine.Database().Query(context.Background(), "SELECT * FROM "+quoted+" WHERE 1 = 0")
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		return nil, err
	}
	available := map[string]bool{}
	for _, column := range columns {
		available[column] = true
	}
	s := &AuditStore{orm: engine, table: table, idGen: ids, resources: map[string]*auditBinding{}}
	for _, config := range resources {
		if strings.TrimSpace(config.Resource) == "" || s.resources[config.Resource] != nil || config.Execution.Metadata() == nil {
			return nil, errors.NewCode(errors.InvalidInput, "invalid or duplicate audit resource")
		}
		binding, err := s.bind(config, available)
		if err != nil {
			return nil, err
		}
		s.resources[config.Resource] = binding
	}
	return s, nil
}

func (s *AuditStore) bind(config AuditResource, available map[string]bool) (*auditBinding, error) {
	metadata := config.Execution.Metadata()
	id, ok := metadata.IDField()
	if !ok {
		return nil, errors.NewCode(errors.InvalidInput, "audit resource requires identity binding")
	}
	copyColumns := map[meta.Dimension]string{}
	for dimension, column := range config.Columns {
		copyColumns[dimension] = column
		if _, err := metadata.Resolve(meta.ByDimension(dimension)); err != nil {
			return nil, err
		}
	}
	config.Columns = copyColumns
	fields := []reflect.StructField{}
	usedNames, usedColumns := map[string]bool{}, map[string]bool{}
	reservedNames := map[string]bool{id.Name: true}
	for _, field := range metadata.Fields() {
		if field.Dimension != "" || field.Name == config.Execution.IsolationField() {
			reservedNames[field.Name] = true
		}
	}
	base := reflect.TypeOf(auditStoreRow{})
	for i := 0; i < base.NumField(); i++ {
		field := base.Field(i)
		column := field.Tag.Get("json")
		field.Name = "Audit" + field.Name
		if column != "entity_id" {
			name := field.Name
			for suffix := 1; reservedNames[field.Name] || usedNames[field.Name]; suffix++ {
				field.Name = name + strconv.Itoa(suffix)
			}
		}
		gormTag := "column:" + column
		if column == "id" {
			gormTag += ";primaryKey"
		}
		field.Tag = reflect.StructTag(`json:"` + column + `" db:"` + column + `" gorm:"` + gormTag + `"`)
		if column == "entity_id" {
			field.Name = id.Name
			field.Tag += ` entity:"id"`
		}
		if usedNames[field.Name] || usedColumns[column] {
			return nil, errors.NewCode(errors.InvalidInput, "audit base field mapping is duplicated").WithContext("field", field.Name).WithContext("column", column)
		}
		fields = append(fields, field)
		usedNames[field.Name], usedColumns[column] = true, true
		if !available[column] {
			return nil, errors.NewCode(errors.InvalidInput, "audit table column is missing").WithContext("column", column)
		}
	}
	for _, field := range metadata.Fields() {
		if field.Dimension == "" && field.Name != config.Execution.IsolationField() {
			continue
		}
		column := config.Columns[field.Dimension]
		if field.Name == config.Execution.IsolationField() {
			if field.Dimension == "" {
				column = config.IsolationColumn
			}
			if config.IsolationColumn != "" && config.IsolationColumn != column {
				return nil, errors.NewCode(errors.InvalidInput, "audit isolation mapping conflicts with dimension column")
			}
			config.IsolationColumn = column
		}
		if usedNames[field.Name] || usedColumns[column] || !available[column] {
			return nil, errors.NewCode(errors.InvalidInput, "audit dimension mapping is missing or duplicated").WithContext("field", field.Name).WithContext("column", column)
		}
		if _, err := safesql.QuoteQualifiedIdentifier(dialect.FromDatabase(s.orm.Database()), column, "", "audit column", 1); err != nil {
			return nil, err
		}
		tag := reflect.StructTag(`json:"` + column + `" db:"` + column + `" gorm:"column:` + column + `"`)
		if field.Dimension != "" {
			tag += reflect.StructTag(` dim:"` + string(field.Dimension) + `"`)
		}
		fields = append(fields, reflect.StructField{Name: field.Name, Type: field.Type, Tag: tag})
		usedNames[field.Name], usedColumns[column] = true, true
	}
	rowType := reflect.StructOf(fields)
	modelMeta := &orm.ModelMeta{Table: s.table, ModelFactory: func() any { return reflect.New(rowType).Interface() }}
	model, err := s.orm.Model(modelMeta)
	if err != nil {
		return nil, err
	}
	rowMetadata, err := meta.DescribeType(reflect.PointerTo(rowType))
	if err != nil {
		return nil, err
	}
	resolver, err := newColumnResolver(rowMetadata, model.Meta(), model.Dialect())
	if err != nil {
		return nil, err
	}
	resolver.identityType = id.Type
	return &auditBinding{config: config, rowType: rowType, modelMeta: modelMeta, model: model, resolver: resolver}, nil
}

func isNilAuditIDGenerator(generator gen.IGenerator[int64]) bool {
	value := reflect.ValueOf(generator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func (s *AuditStore) DataSourceID() string { id, _ := orm.DatasourceIDOf(s.orm); return id }

func (s *AuditStore) modelFor(ctx context.Context, binding *auditBinding) (orm.IModel, error) {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		if err := ensureSessionDatasourceFor(s.orm, session, s.table); err != nil {
			return nil, err
		}
		return session.Model(binding.modelMeta)
	}
	return binding.model, nil
}

func (s *AuditStore) maxRowsPerInsert(binding *auditBinding) int {
	budget := dialect.DefaultMaxBindParameters
	if provider, ok := s.orm.Database().(db.IBindParameterLimitProvider); ok {
		budget = dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
	}
	if n := budget / binding.rowType.NumField(); n > 0 {
		return n
	}
	return 1
}

func (s *AuditStore) buildAuditRow(ctx context.Context, record audit.AuditRecord) (*auditBinding, any, int64, error) {
	binding := s.resources[record.ResourceKind]
	if binding == nil {
		return nil, nil, 0, errors.NewCode(errors.InvalidInput, "audit resource is not configured")
	}
	if err := audit.ValidateRecord(record, binding.config.Execution.Metadata()); err != nil {
		return nil, nil, 0, err
	}
	snapshot, err := audit.NewDimensionSnapshot(binding.config.Execution.Metadata(), record.Dimensions.Values())
	if err != nil {
		return nil, nil, 0, err
	}
	if record.ID == 0 {
		record.ID, err = s.idGen.Next()
		if err != nil {
			return nil, nil, 0, err
		}
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	dimensions, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, 0, err
	}
	base := auditStoreRow{ID: record.ID, ResourceKind: record.ResourceKind, EntityID: record.EntityID,
		Action: record.Action, Operator: record.Operator, Initiator: record.Initiator, Reason: record.Reason,
		CorrelationID: record.CorrelationID, OperationID: record.OperationID, Timestamp: record.Timestamp,
		Changes: string(record.Changes), Metadata: string(record.Metadata), Dimensions: string(dimensions)}
	row := reflect.New(binding.rowType)
	baseValue := reflect.ValueOf(base)
	for i := 0; i < baseValue.NumField(); i++ {
		row.Elem().Field(i).Set(baseValue.Field(i))
	}
	for _, field := range binding.config.Execution.Metadata().Fields() {
		if field.Dimension == "" && field.Name != binding.config.Execution.IsolationField() {
			continue
		}
		raw, _ := snapshot.Value(field.Dimension)
		if field.Name == binding.config.Execution.IsolationField() {
			isolation, err := binding.isolation(ctx)
			if err != nil {
				return nil, nil, 0, err
			}
			if field.Dimension != "" && !reflect.DeepEqual(raw, isolation) {
				return nil, nil, 0, errors.NewCode(errors.Forbidden, "audit isolation snapshot differs from request")
			}
			raw = isolation
		}
		value, err := field.Convert(raw)
		if err != nil {
			return nil, nil, 0, err
		}
		row.Elem().FieldByName(field.Name).Set(value)
	}
	return binding, row.Interface(), record.ID, nil
}

func (b *auditBinding) isolation(ctx context.Context) (any, error) {
	name := b.config.Execution.IsolationField()
	if name == "" {
		return nil, nil
	}
	raw := contextx.TenantID(ctx)
	if b.config.IsolationResolve != nil {
		var err error
		raw, err = b.config.IsolationResolve(ctx)
		if err != nil {
			return nil, err
		}
	}
	if raw == "" {
		return nil, errors.NewCode(errors.Forbidden, "audit isolation key is required")
	}
	field, _ := b.config.Execution.Metadata().Field(name)
	value, err := field.Convert(raw)
	if err != nil {
		return nil, err
	}
	for value.Kind() == reflect.Ptr {
		value = value.Elem()
	}
	return value.Interface(), nil
}

func (s *AuditStore) SaveAuditRecord(ctx context.Context, record audit.AuditRecord) (int64, error) {
	if ctx == nil {
		return 0, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	binding, row, id, err := s.buildAuditRow(ctx, record)
	if err != nil {
		return 0, err
	}
	model, err := s.modelFor(ctx, binding)
	if err != nil {
		return 0, err
	}
	if err := model.Create(ctx, row); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *AuditStore) SaveAuditRecords(ctx context.Context, records []audit.AuditRecord) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if err := audit.ValidateTransferPairs(records); err != nil {
		return err
	}
	type prepared struct {
		binding *auditBinding
		row     any
	}
	items := make([]prepared, 0, len(records))
	for _, record := range records {
		binding, row, _, err := s.buildAuditRow(ctx, record)
		if err != nil {
			return err
		}
		items = append(items, prepared{binding, row})
	}
	if len(items) > 0 {
		multipleInserts := len(items) > s.maxRowsPerInsert(items[0].binding)
		for _, item := range items {
			multipleInserts = multipleInserts || item.binding != items[0].binding
		}
		_, _, inContext := orm.TxSessionFromContext(ctx)
		_, isSession := s.orm.(orm.IOrmSession)
		if multipleInserts && !inContext && !isSession {
			return errors.NewCode(errors.FailedPrecondition, "audit batches spanning multiple inserts require a transaction")
		}
	}
	for start := 0; start < len(items); {
		binding := items[start].binding
		end := start + 1
		for end < len(items) && items[end].binding == binding && end-start < s.maxRowsPerInsert(binding) {
			end++
		}
		rows := make([]any, 0, end-start)
		for _, item := range items[start:end] {
			rows = append(rows, item.row)
		}
		model, err := s.modelFor(ctx, binding)
		if err != nil {
			return err
		}
		if err := model.Create(ctx, rows...); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (s *AuditStore) ListAuditRecords(ctx context.Context, entityID string, offset, limit int, condition spec.Spec) ([]audit.AuditRecord, error) {
	if ctx == nil || strings.TrimSpace(entityID) == "" {
		return nil, errors.NewCode(errors.InvalidInput, "audit context and entity id are required")
	}
	binding := s.resources[audit.AuditResourceKind(ctx)]
	if binding == nil {
		return nil, errors.NewCode(errors.InvalidInput, "audit resource is not configured")
	}
	if err := validateAuditQuery(condition, binding.config.Execution); err != nil {
		return nil, err
	}
	sqlCondition, err := translateSpec(condition, binding.resolver, nil, false)
	if err != nil {
		return nil, err
	}
	opts := []orm.QueryOption{orm.WithWhere("entity_id = ?", entityID), orm.WithWhere("resource_kind = ?", binding.config.Resource)}
	if binding.config.Execution.IsolationField() != "" {
		value, err := binding.isolation(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, orm.WithWhere(binding.resolver.quote(binding.config.IsolationColumn)+" = ?", value))
	}
	if sqlCondition.Expr != "" {
		opts = append(opts, orm.WithWhere(sqlCondition.Expr, sqlCondition.Args...))
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	opts = append(opts, orm.WithOrderBy("timestamp", true), orm.WithOrderBy("id", true), orm.WithOffset(offset), orm.WithLimit(limit))
	model, err := s.modelFor(ctx, binding)
	if err != nil {
		return nil, err
	}
	rows := reflect.New(reflect.SliceOf(binding.rowType))
	if err := model.Find(ctx, rows.Interface(), opts...); err != nil {
		return nil, err
	}
	out := make([]audit.AuditRecord, 0, rows.Elem().Len())
	for i := 0; i < rows.Elem().Len(); i++ {
		row := rows.Elem().Index(i)
		var base auditStoreRow
		baseValue := reflect.ValueOf(&base).Elem()
		for j := 0; j < baseValue.NumField(); j++ {
			baseValue.Field(j).Set(row.Field(j))
		}
		var snapshot audit.DimensionSnapshot
		if err := json.Unmarshal([]byte(base.Dimensions), &snapshot); err != nil {
			return nil, errors.Wrap(err, errors.Internal, "decode audit dimensions")
		}
		snapshot, err = audit.NewDimensionSnapshot(binding.config.Execution.Metadata(), snapshot.Values())
		if err != nil {
			return nil, errors.Wrap(err, errors.Internal, "incomplete audit dimensions")
		}
		for _, field := range binding.config.Execution.Metadata().Fields() {
			if field.Dimension == "" {
				continue
			}
			stored := row.FieldByName(field.Name)
			var scalar any
			if stored.Kind() == reflect.Ptr {
				if !stored.IsNil() {
					scalar = stored.Elem().Interface()
				}
			} else {
				scalar = stored.Interface()
			}
			value, _ := snapshot.Value(field.Dimension)
			if !reflect.DeepEqual(scalar, value) {
				return nil, errors.NewCode(errors.Internal, "audit dimension column differs from snapshot")
			}
		}
		changes, err := decodeAuditJSON(base.ID, base.Changes, "changes")
		if err != nil {
			return nil, err
		}
		metadata, err := decodeAuditJSON(base.ID, base.Metadata, "metadata")
		if err != nil {
			return nil, err
		}
		out = append(out, audit.AuditRecord{ID: base.ID, ResourceKind: base.ResourceKind, EntityID: base.EntityID,
			Action: base.Action, Operator: base.Operator, Initiator: base.Initiator, Reason: base.Reason,
			CorrelationID: base.CorrelationID, OperationID: base.OperationID, Timestamp: base.Timestamp,
			Changes: changes, Metadata: metadata, Dimensions: snapshot})
	}
	return out, nil
}

func validateAuditQuery(s spec.Spec, execution app.ExecutionDescriptor) error {
	if err := execution.ValidateWriteSpec(s, false); err != nil {
		return err
	}
	for _, ref := range s.Refs() {
		field, err := execution.Metadata().Resolve(ref)
		if err != nil {
			return err
		}
		if !field.Identity && (field.Dimension == "" || !execution.IsProtected(field) || field.Name == execution.IsolationField()) {
			return errors.NewCode(errors.Internal, "audit query references an unauthorized field")
		}
	}
	return nil
}

func decodeAuditJSON(id int64, raw, field string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if !json.Valid([]byte(raw)) {
		return nil, errors.NewCode(errors.Internal, "failed to decode audit record "+field).WithContext("audit_id", id)
	}
	return json.RawMessage(raw), nil
}
