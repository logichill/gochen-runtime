package lite

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"

	"gochen/codec/casecodec"
	"gochen/errors"
)

var (
	sqlScannerType   = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	driverValuerType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
)

type fieldInfo struct {
	Column        string
	Index         []int
	PrimaryKey    bool
	AutoIncrement bool
}

type structMeta struct {
	typ          reflect.Type
	fields       []fieldInfo
	columnToInfo map[string]fieldInfo
}

// insertableColumns 返回可用于 INSERT 的列及对应字段。
func (sm *structMeta) insertableColumns() ([]string, []fieldInfo) {
	var cols []string
	var fields []fieldInfo
	for _, f := range sm.fields {
		// 自增主键默认交给数据库生成
		if f.PrimaryKey && f.AutoIncrement {
			continue
		}
		cols = append(cols, f.Column)
		fields = append(fields, f)
	}
	return cols, fields
}

func (o *Orm) structMetaForValue(v any) (*structMeta, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, nil
	}

	o.mu.RLock()
	if sm, ok := o.structMap[t]; ok {
		o.mu.RUnlock()
		return sm, nil
	}
	o.mu.RUnlock()

	sm, err := buildStructMeta(t)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	// 写锁内复查：构建期间可能已有其他 goroutine 构建并缓存了同一类型。
	if existing, ok := o.structMap[t]; ok {
		o.mu.Unlock()
		return existing, nil
	}
	o.structMap[t] = sm
	o.mu.Unlock()
	return sm, nil
}

// buildStructMeta 构造StructMeta。
func buildStructMeta(t reflect.Type) (*structMeta, error) {
	sm := &structMeta{
		typ:          t,
		columnToInfo: make(map[string]fieldInfo),
	}

	var walk func(reflect.Type, []int, map[reflect.Type]bool) error
	walk = func(cur reflect.Type, prefix []int, path map[reflect.Type]bool) error {
		if path[cur] {
			return errors.NewCode(errors.InvalidInput, "recursive ORM struct type").WithContext("type", cur.String())
		}
		path[cur] = true
		defer delete(path, cur)
		for i := 0; i < cur.NumField(); i++ {
			f := cur.Field(i)
			// 跳过未导出字段
			if f.PkgPath != "" {
				continue
			}

			index := append(append([]int(nil), prefix...), i)

			col, pk, auto, skip := parseColumnTag(f)
			if skip {
				continue
			}

			// 命名指针类型可能指向自身（例如 type P *P）。在任何 helper 跟随指针链
			// 之前先校验，避免畸形元数据让反射陷入死循环。
			if _, err := derefDBFieldType(f.Type); err != nil {
				return errors.Wrap(err, errors.InvalidInput, "invalid ORM field type").WithContext("field", f.Name)
			}

			writable := isWritableDBField(f.Type)
			scannable := isScannableDBField(f.Type)

			embeddedType, _ := derefDBFieldType(f.Type)
			if f.Anonymous && embeddedType.Kind() == reflect.Struct && !writable && !scannable {
				// 内嵌结构体（例如 entity.Entity），递归展开；递归检测由 walk 自身完成
				if err := walk(embeddedType, index, path); err != nil {
					return err
				}
				continue
			}

			// 只收集数据库可表达字段，跳过切片/映射/普通结构体（time.Time 除外）
			if !writable && !scannable {
				continue
			}

			if col == "" {
				col = toSnakeCase(f.Name)
			}

			info := fieldInfo{
				Column:        col,
				Index:         index,
				PrimaryKey:    pk,
				AutoIncrement: auto,
			}
			if writable {
				sm.fields = append(sm.fields, info)
			}
			if scannable {
				// 后来的同名列覆盖之前的定义（以最内层为准）
				sm.columnToInfo[col] = info
			}
		}
		return nil
	}

	if err := walk(t, nil, make(map[reflect.Type]bool)); err != nil {
		return nil, err
	}
	return sm, nil
}

func isWritableDBField(t reflect.Type) bool {
	baseType, err := derefDBFieldType(t)
	if err != nil || baseType == nil {
		return false
	}
	t = baseType
	if implementsDriverValuer(t) {
		return true
	}
	if implementsSQLScanner(t) {
		return false
	}
	if isTimeType(t) {
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

func implementsDriverValuer(t reflect.Type) bool {
	return implementsIface(t, driverValuerType)
}

func implementsSQLScanner(t reflect.Type) bool {
	return implementsIface(t, sqlScannerType)
}

func isScannableDBField(t reflect.Type) bool {
	baseType, err := derefDBFieldType(t)
	if err != nil || baseType == nil {
		return false
	}
	t = baseType
	if implementsSQLScanner(t) {
		return true
	}
	if isTimeType(t) {
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

// isTimeType 判断时间类型。
func isTimeType(t reflect.Type) bool {
	baseType, err := derefDBFieldType(t)
	if err != nil || baseType == nil {
		return false
	}
	t = baseType
	return t.PkgPath() == "time" && t.Name() == "Time"
}

// derefDBFieldType 剥离指针链，同时拒绝递归的命名指针类型。
// 对 type P *P，reflect.Type.Elem 会返回同一类型，普通循环永不终止。
func derefDBFieldType(t reflect.Type) (reflect.Type, error) {
	seen := make(map[reflect.Type]struct{})
	for t != nil && t.Kind() == reflect.Ptr {
		if _, ok := seen[t]; ok {
			return nil, errors.NewCode(errors.InvalidInput, "recursive ORM pointer type").
				WithContext("type", t.String())
		}
		seen[t] = struct{}{}
		t = t.Elem()
	}
	return t, nil
}

// parseColumnTag 解析ColumnTag。
func parseColumnTag(f reflect.StructField) (column string, primaryKey, autoIncrement bool, skip bool) {
	gormTag := f.Tag.Get("gorm")
	hasGormTag := strings.TrimSpace(gormTag) != ""
	if gormTag != "" {
		parts := strings.Split(gormTag, ";")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "-" || strings.EqualFold(part, "-:all") {
				return "", false, false, true
			}
			if strings.HasPrefix(part, "column:") {
				column = strings.TrimSpace(strings.TrimPrefix(part, "column:"))
			}
			if strings.EqualFold(part, "primaryKey") || strings.EqualFold(part, "primary_key") {
				primaryKey = true
			}
			if strings.EqualFold(part, "autoIncrement") || strings.EqualFold(part, "autoincrement") {
				autoIncrement = true
			}
		}
	}

	if column == "" {
		if dbTag := f.Tag.Get("db"); dbTag != "" {
			column = strings.Split(dbTag, ",")[0]
			if column == "-" {
				return "", false, false, true
			}
		} else {
			jsonTag := f.Tag.Get("json")
			column = strings.Split(jsonTag, ",")[0]
			if column == "-" {
				if !hasGormTag {
					return "", false, false, true
				}
				column = ""
			}
		}
	}

	return column, primaryKey, autoIncrement, false
}

// toSnakeCase 把 Go 字段名渲染为 snake_case 列名。
//
// 渲染不会失败，故忽略 error 以保持单返回值形态。
func toSnakeCase(s string) string {
	name, _ := casecodec.Convert(casecodec.Snake, s)
	return name
}

func tryGetTableName(model any) (string, bool) {
	if model == nil {
		return "", false
	}
	v := reflect.ValueOf(model)
	if !v.IsValid() {
		return "", false
	}

	if v.Kind() == reflect.Ptr && v.IsNil() {
		v = reflect.New(v.Type().Elem())
	}

	m, ok := v.Interface().(interface{ TableName() string })
	if ok {
		return m.TableName(), true
	}

	t := v.Type()
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return "", false
	}
	// 如果类型本身实现了 TableName（值接收者）
	if m2, ok := reflect.New(t).Interface().(interface{ TableName() string }); ok {
		return m2.TableName(), true
	}

	return "", false
}
