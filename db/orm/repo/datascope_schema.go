package repo

import (
	"reflect"
	"strings"
	"sync"

	"gochen/codec/casecodec"
)

// dataScopeSchemaCache 记忆 (实体类型, 安全列) → schema 的解析结果。
//
// 为什么要缓存：解析每个安全列都要在实体类型上做一次递归字段查找，并为环检测
// 分配一张 map；而写路径每条实体会问 3~5 次 accessSchema()——一次满额批量写
// 就是上万次完全重复的反射。
//
// 为什么挂在包级而不是 Repo 字段上：Repo 在包内也会被直接以结构体字面量构造
// （见 datascope_test.go），字段缓存会随构造路径不同而失效或读到零值；
// 而这里的两个输入完全决定输出，按它们记忆化对任何构造路径都成立。
var dataScopeSchemaCache sync.Map // map[dataScopeSchemaKey]dataScopeSchema

// dataScopeSchemaKey 的两个字段都可比较（reflect.Type 是接口，accessColumns 全是 string）。
type dataScopeSchemaKey struct {
	entity  reflect.Type
	columns accessColumns
}

// resolveDataScopeSchema 仅依据**显式声明**的列构建安全列 schema。
//
// 不再按字段名（tenant_id / managed_scope_id / ...）隐式推断并激活
// 安全过滤。未经 WithIsolation / WithScope 声明的列一律不启用，
// 让"这张表受不受安全约束"成为装配期可审计的显式决定。
func resolveDataScopeSchema[T any](columns accessColumns) dataScopeSchema {
	var zero T
	typ := reflect.TypeOf(zero)
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	key := dataScopeSchemaKey{entity: typ, columns: columns}
	if cached, ok := dataScopeSchemaCache.Load(key); ok {
		return cached.(dataScopeSchema)
	}
	var schema dataScopeSchema
	overrideSchemaField(typ, &schema.managedScope, columns.managedScope)
	overrideSchemaField(typ, &schema.isolation, columns.isolation)
	overrideSchemaField(typ, &schema.ownerID, columns.ownerID)
	overrideSchemaField(typ, &schema.version, columns.version)
	// schema 中的 index 切片由 findFieldByColumn 每次新建、且全链路只读，
	// 因此多个 Repo 共享同一份缓存值是安全的。
	dataScopeSchemaCache.Store(key, schema)
	return schema
}
func overrideSchemaField(typ reflect.Type, field *dataScopeField, column string) {
	column = strings.TrimSpace(column)
	if column == "" {
		return
	}
	*field = dataScopeField{column: column}
	if matched, ok := findFieldByColumn(typ, column, nil, map[reflect.Type]int{}); ok {
		field.index = matched.index
	}
}

func findFieldByColumn(typ reflect.Type, target string, prefix []int, visiting map[reflect.Type]int) (dataScopeField, bool) {
	target = strings.TrimSpace(target)
	if target == "" || typ == nil {
		return dataScopeField{}, false
	}
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || isTimeType(typ) {
		return dataScopeField{}, false
	}
	if visiting[typ] > 0 {
		return dataScopeField{}, false
	}
	visiting[typ]++
	defer func() {
		visiting[typ]--
	}()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		if normalizeColumnName(field) == target {
			return dataScopeField{column: target, index: index}, true
		}
		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() != reflect.Struct || isTimeType(fieldType) {
			continue
		}
		if matched, ok := findFieldByColumn(fieldType, target, index, visiting); ok {
			return matched, true
		}
	}
	return dataScopeField{}, false
}

func (s dataScopeSchema) hasConstraints() bool {
	return s.managedScope.column != "" || s.isolation.column != "" || s.ownerID.column != "" || s.version.column != ""
}

func normalizeColumnName(field reflect.StructField) string {
	if column := columnFromTags(field); column != "" {
		return column
	}
	return toSnakeCase(field.Name)
}

func columnFromTags(field reflect.StructField) string {
	gormTag := strings.TrimSpace(field.Tag.Get("gorm"))
	if gormTag != "" {
		for _, part := range strings.Split(gormTag, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "column:") {
				return strings.TrimSpace(strings.TrimPrefix(part, "column:"))
			}
		}
	}
	if tag := strings.TrimSpace(field.Tag.Get("db")); tag != "" && tag != "-" {
		return tag
	}
	if tag := strings.TrimSpace(field.Tag.Get("json")); tag != "" && tag != "-" {
		return strings.TrimSpace(strings.Split(tag, ",")[0])
	}
	return ""
}
func isTimeType(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t != nil && t.PkgPath() == "time" && t.Name() == "Time"
}

// toSnakeCase 把 Go 字段名渲染为 snake_case 列名。
//
// 渲染不会失败，故忽略 error 以保持单返回值形态。
func toSnakeCase(value string) string {
	name, _ := casecodec.Convert(casecodec.Snake, value)
	return name
}
