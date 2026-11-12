package repo

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"strings"

	"gochen/codec/casecodec"
	"gochen/contextx"
	coredialect "gochen/db/dialect"
	"gochen/db/orm"
	entitymeta "gochen/domain/meta"
	"gochen/errors"
)

var driverValuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// resolvedField 把一个领域字段与其持久化列绑定。
type resolvedField struct {
	meta       entitymeta.Field
	column     string
	serializer string
}

// columnResolver 负责领域字段名、字段角色与 SQL 列名之间的唯一映射。
//
// 列名解析顺序：模型元数据（适配器已解析）→ 结构体标签（gorm column、db、json）
// → snake_case。字段名与列名都能用于查询过滤与排序，避免下游同时记两套命名。
type columnResolver struct {
	metadata  *entitymeta.Metadata
	qualifier string
	dialect   coredialect.IDialect
	fields    []resolvedField
	byName    map[string]int
	// identityType 保留审计文本列对应的业务标识转换类型。
	identityType reflect.Type
}

func newColumnResolver(metadata *entitymeta.Metadata, modelMeta *orm.ModelMeta, d coredialect.IDialect) (*columnResolver, error) {
	if metadata == nil {
		return nil, errors.NewCode(errors.InvalidInput, "entity metadata cannot be nil")
	}
	if d == nil {
		return nil, errors.NewCode(errors.InvalidInput, "orm model dialect cannot be nil")
	}
	columnsByName := make(map[string]string)
	if modelMeta != nil {
		for _, field := range modelMeta.Fields {
			if strings.TrimSpace(field.Name) == "" || strings.TrimSpace(field.Column) == "" {
				continue
			}
			columnsByName[field.Name] = field.Column
		}
	}
	typ := metadata.Type()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	structFields := make(map[string]reflect.StructField)
	for _, field := range reflect.VisibleFields(typ) {
		if field.Anonymous || field.PkgPath != "" {
			continue
		}
		structFields[field.Name] = field
	}

	resolver := &columnResolver{
		metadata: metadata,
		dialect:  d,
		byName:   make(map[string]int),
	}
	// 适配器已解析出列映射时，它是唯一权威来源；否则按与 lite 适配器相同的
	// 标签与类型规则筛选可持久化字段，避免把切片、普通结构体等写成不存在的列。
	mapped := len(columnsByName) > 0
	for _, field := range metadata.Fields() {
		structField, hasStruct := structFields[field.Name]
		serializer := ""
		if hasStruct {
			serializer = serializerFromStructField(structField)
		}
		column := strings.TrimSpace(columnsByName[field.Name])
		if mapped {
			if column == "" {
				continue
			}
		} else {
			if !hasStruct {
				continue
			}
			var skip bool
			column, skip = columnFromStructField(structField)
			if skip {
				continue
			}
			if !isDBFieldType(field.Type) {
				continue
			}
			if column == "" {
				column = toSnakeCase(field.Name)
			}
		}
		index := len(resolver.fields)
		resolver.fields = append(resolver.fields, resolvedField{meta: field, column: column, serializer: serializer})
		resolver.byName[field.Name] = index
		resolver.byName[column] = index
		resolver.byName[toSnakeCase(field.Name)] = index
	}
	return resolver, nil
}

func (r *columnResolver) primaryKeyColumn() string {
	field, ok := r.metadata.IDField()
	if !ok {
		return ""
	}
	column, _ := r.columnOf(field.Name)
	return column
}

// resolveRef 按实体字段身份解析类型化引用，保持物理列映射局部化。
func (r *columnResolver) resolveRef(ref entitymeta.Ref) (entitymeta.Field, string, bool) {
	field, err := r.metadata.Resolve(ref)
	if err != nil {
		return entitymeta.Field{}, "", false
	}
	return r.resolveColumn(field.Name)
}

// resolveColumn 解析字段名、列名或字段角色，返回领域字段与持久化列。
func (r *columnResolver) resolveColumn(name string) (entitymeta.Field, string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return entitymeta.Field{}, "", false
	}
	if index, ok := r.lookup(name); ok {
		field := r.fields[index]
		if field.column == "" {
			return entitymeta.Field{}, "", false
		}
		return field.meta, field.column, true
	}
	return entitymeta.Field{}, "", false
}

func (r *columnResolver) columnOf(name string) (string, bool) {
	_, column, ok := r.resolveColumn(name)
	return column, ok
}

func (r *columnResolver) lookup(name string) (int, bool) {
	index, ok := r.byName[name]
	return index, ok
}

func (r *columnResolver) quote(column string) string {
	if r.qualifier != "" {
		return r.dialect.QuoteIdentifier(r.qualifier) + "." + r.dialect.QuoteIdentifier(column)
	}
	return r.dialect.QuoteIdentifier(column)
}

// readValue 读取字段在实体上的当前值，指针为 nil 时返回 nil。
func (r *columnResolver) readValue(entity any, field resolvedField) (any, error) {
	value, ok := field.meta.Value(entity)
	if !ok {
		return nil, nil
	}
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return nil, nil
		}
		value = value.Elem()
	}
	if field.serializer == "json" {
		encoded, err := json.Marshal(value.Interface())
		if err != nil {
			return nil, errors.Wrap(err, errors.Internal, "failed to serialize field value").
				WithContext("field", field.meta.Name).
				WithContext("column", field.column)
		}
		return string(encoded), nil
	}
	return value.Interface(), nil
}

// setValue 按字段类型转换并写入实体字段。
func (r *columnResolver) setValue(entity any, field resolvedField, raw any) error {
	converted, err := field.meta.Convert(raw)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to convert field value").
			WithContext("field", field.meta.Name).
			WithContext("column", field.column)
	}
	target, ok := field.meta.Value(entity)
	if !ok || !target.CanSet() {
		return errors.NewCode(errors.Internal, "entity field is not settable").
			WithContext("field", field.meta.Name).
			WithContext("column", field.column)
	}
	target.Set(converted)
	return nil
}

// columnsWithout 返回除给定列以外的全部列（用于 UPDATE SET 映射）。
func (r *columnResolver) updateValues(entity any, excluded map[string]struct{}) (map[string]any, error) {
	values := make(map[string]any, len(r.fields))
	for _, field := range r.fields {
		if field.column == "" {
			continue
		}
		if _, skip := excluded[field.column]; skip {
			continue
		}
		value, err := r.readValue(entity, field)
		if err != nil {
			return nil, err
		}
		values[field.column] = value
	}
	return values, nil
}

// serializerFromStructField 读取 gorm serializer 标签声明的序列化格式。
func serializerFromStructField(field reflect.StructField) string {
	for _, part := range strings.Split(field.Tag.Get("gorm"), ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "serializer:") {
			return strings.ToLower(strings.TrimSpace(part[len("serializer:"):]))
		}
	}
	return ""
}

func toSnakeCase(value string) string {
	name, _ := casecodec.Convert(casecodec.Snake, value)
	return name
}

// columnFromStructField 与 lite 适配器保持同一套标签解析规则。
// skip 表示字段被 ORM/db 标签排除，或仅声明 JSON 隐藏且没有 ORM 标签。
func columnFromStructField(field reflect.StructField) (column string, skip bool) {
	gormTag := field.Tag.Get("gorm")
	hasGormTag := strings.TrimSpace(gormTag) != ""
	if gormTag != "" {
		for _, part := range strings.Split(gormTag, ";") {
			part = strings.TrimSpace(part)
			if part == "-" || strings.EqualFold(part, "-:all") {
				return "", true
			}
			if strings.HasPrefix(part, "column:") {
				return strings.TrimSpace(strings.TrimPrefix(part, "column:")), false
			}
		}
	}
	if dbTag := field.Tag.Get("db"); dbTag != "" {
		column := strings.Split(dbTag, ",")[0]
		if column == "-" {
			return "", true
		}
		return column, false
	}
	jsonTag := field.Tag.Get("json")
	column = strings.Split(jsonTag, ",")[0]
	if column == "-" {
		return "", !hasGormTag
	}
	return column, false
}

// implementsIface 检查类型 t 或 *t（仅当 t 非指针时）是否实现了接口 iface。
func implementsIface(t reflect.Type, iface reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Implements(iface) {
		return true
	}
	if t.Kind() != reflect.Ptr {
		return reflect.PointerTo(t).Implements(iface)
	}
	return false
}

// isDBFieldType 判断字段类型能否直接写入数据库。
//
// 与 lite 适配器的可写字段判定保持一致：实现 driver.Valuer、time.Time，
// 以及基础标量。切片、映射与普通结构体不是列。
func isDBFieldType(t reflect.Type) bool {
	seen := make(map[reflect.Type]struct{})
	for t != nil && t.Kind() == reflect.Ptr {
		if _, ok := seen[t]; ok {
			return false
		}
		seen[t] = struct{}{}
		t = t.Elem()
	}
	if t == nil {
		return false
	}
	if implementsIface(t, driverValuerType) {
		return true
	}
	if t.PkgPath() == "time" && t.Name() == "Time" {
		return true
	}
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

// isolationPolicy 表达一次查询构造对隔离条件的态度。
type isolationPolicy int

const (
	// isolationStrict 隔离条件必须落地：写路径与写前探针都走这里。
	isolationStrict isolationPolicy = iota
	// isolationRelaxable 只读查询档：两道闸都合上时才跳过隔离条件。
	isolationRelaxable
)

// applyIsolation 强制执行 L2 隔离边界。
func (r *Repo[T, ID]) applyIsolation(quer *queryBuilder, policy isolationPolicy) (*queryBuilder, error) {
	column := strings.TrimSpace(r.access.isolation)
	if column == "" {
		return quer, nil
	}
	if policy == isolationRelaxable {
		relaxed, err := r.crossIsolationRelaxed(quer.ctx)
		if err != nil {
			return nil, err
		}
		if relaxed {
			return quer, nil
		}
	}
	key, err := r.resolveIsolationKey(quer.ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, r.isolationKeyMissingError(column)
	}
	value, err := r.isolationKeyValue(key)
	if err != nil {
		return nil, err
	}
	return quer.Where(r.resolver.quote(column)+" = ?", value), nil
}

// stampIsolation 在写入前把实体标记为当前隔离空间。
func (r *Repo[T, ID]) stampIsolation(ctx context.Context, entity T) error {
	column := strings.TrimSpace(r.access.isolation)
	if column == "" {
		return nil
	}
	key, err := r.resolveIsolationKey(ctx)
	if err != nil {
		return err
	}
	if key == "" {
		return r.isolationKeyMissingError(column)
	}
	if err := r.resolver.setValue(entity, resolvedField{meta: r.access.isolationField, column: column}, key); err != nil {
		return err
	}
	return nil
}

func (r *Repo[T, ID]) isolationKeyValue(key string) (any, error) {
	converted, err := r.access.isolationField.Convert(key)
	if err != nil {
		return nil, errors.Wrap(err, errors.Internal, "failed to convert isolation key")
	}
	for converted.Kind() == reflect.Ptr {
		if converted.IsNil() {
			return nil, errors.NewCode(errors.Internal, "isolation key is nil after conversion")
		}
		converted = converted.Elem()
	}
	return converted.Interface(), nil
}

func (r *Repo[T, ID]) resolveIsolationKey(ctx context.Context) (string, error) {
	if r.isolationResolve != nil {
		key, err := r.isolationResolve(ctx)
		if err != nil {
			// 解析器自身故障绝不能被当成"无隔离"而放行，原样上抛。
			return "", err
		}
		return strings.TrimSpace(key), nil
	}
	return strings.TrimSpace(contextx.TenantID(ctx)), nil
}

func (r *Repo[T, ID]) isolationKeyMissingError(column string) error {
	source := "contextx.TenantID (default)"
	if r.isolationResolve != nil {
		source = "the configured IsolationCols.Resolve"
	}
	return errors.NewCode(errors.Forbidden,
		"isolated repository requires an isolation key for this request").
		WithContext("resource_kind", r.resourceKind).
		WithContext("isolation_column", column).
		WithContext("isolation_source", source)
}

// CrossIsolationRead 描述一次已放行的跨隔离空间读取，用于强制留痕。
type CrossIsolationRead struct {
	ResourceKind    string
	Table           string
	IsolationColumn string
	Reason          string
	Operator        string
}

// ICrossIsolationAudit 接收跨隔离只读放行的留痕。
type ICrossIsolationAudit interface {
	RecordCrossIsolationRead(ctx context.Context, read CrossIsolationRead) error
}

// CrossIsolationAuditFunc 允许用函数直接实现 ICrossIsolationAudit。
type CrossIsolationAuditFunc func(ctx context.Context, read CrossIsolationRead) error

// RecordCrossIsolationRead 记录一次跨隔离只读放行。
func (f CrossIsolationAuditFunc) RecordCrossIsolationRead(ctx context.Context, read CrossIsolationRead) error {
	if f == nil {
		return errors.NewCode(errors.InvalidInput,
			"cross-isolation audit function cannot be nil; this indicates a wiring bug")
	}
	return f(ctx, read)
}

// validateCrossIsolationDeclaration 拦下让开闸声明落空或无意义的配置。
func (r *Repo[T, ID]) validateCrossIsolationDeclaration() error {
	if !r.crossIsolationReads {
		return nil
	}
	if r.crossIsolationAudit == nil {
		return errors.NewCode(errors.InvalidInput,
			"repository allows cross-isolation reads but supplies no audit sink; "+
				"an unauditable valve is indistinguishable from silently omitting WithIsolation").
			WithContext("resource_kind", r.resourceKind)
	}
	if strings.TrimSpace(r.access.isolation) == "" {
		return errors.NewCode(errors.InvalidInput,
			"repository allows cross-isolation reads but declares no isolation to relax "+
				"(add WithIsolation, or drop WithCrossIsolationReads)").
			WithContext("resource_kind", r.resourceKind)
	}
	return nil
}

func (r *Repo[T, ID]) crossIsolationRelaxed(ctx context.Context) (bool, error) {
	column := strings.TrimSpace(r.access.isolation)
	if !r.crossIsolationReads || column == "" {
		return false, nil
	}
	credential, ok := contextx.CrossIsolationFrom(ctx)
	if !ok {
		return false, nil
	}
	if r.crossIsolationAudit == nil {
		return false, errors.NewCode(errors.Forbidden,
			"cross-isolation read requires an audit sink").
			WithContext("resource_kind", r.resourceKind)
	}
	if err := r.crossIsolationAudit.RecordCrossIsolationRead(ctx, CrossIsolationRead{
		ResourceKind:    r.resourceKind,
		Table:           r.table,
		IsolationColumn: column,
		Reason:          credential.Reason,
		Operator:        credential.Operator,
	}); err != nil {
		return false, errors.Wrap(err, errors.Forbidden, "failed to record cross-isolation read")
	}
	return true, nil
}

// guardCrossIsolationWrite 在开闸期间拒绝本仓储上的一切写入。
func (r *Repo[T, ID]) guardCrossIsolationWrite(ctx context.Context) error {
	if !r.crossIsolationReads {
		return nil
	}
	column := strings.TrimSpace(r.access.isolation)
	if column == "" {
		return nil
	}
	credential, ok := contextx.CrossIsolationFrom(ctx)
	if !ok {
		return nil
	}
	return errors.NewCode(errors.Forbidden,
		"cross-isolation credential permits reads only; derive a per-isolation-space context before writing").
		WithContext("resource_kind", r.resourceKind).
		WithContext("isolation_column", column).
		WithContext("reason", credential.Reason).
		WithContext("operator", credential.Operator)
}
