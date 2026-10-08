package lite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/sql/sqlbuilder"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
)

// createMaxParams 返回本次多值 INSERT 可用的占位参数预算。
//
// 向底层数据库探测（core.IBindParameterLimitProvider）；未实现即用默认值。
// 不在 Orm 构造期定格：事务会话下 m.orm.db 是事务对象，预算须随之取自它。
func (m *model) createMaxParams() int {
	if provider, ok := m.orm.db.(db.IBindParameterLimitProvider); ok {
		return dialect.ResolveMaxBindParameters(provider.MaxBindParameters())
	}
	return dialect.DefaultMaxBindParameters
}

// Create 创建对象并写入存储。
//
// 说明：
// - Create 插入记录（支持批量）。
//
// 参数：
// - entities：要处理的实体列表。
func (m *model) Create(ctx context.Context, entities ...any) error {
	if len(entities) == 0 {
		return nil
	}

	// 以第一个实体的类型构建字段映射
	first := entities[0]
	sm, metaErr := m.orm.structMetaForValue(first)
	if metaErr != nil {
		return metaErr
	}
	if sm == nil {
		return errors.NewCode(errors.InvalidInput, "basic.Model.Create: unsupported entity type").
			WithContext("entity_type", fmt.Sprintf("%T", first))
	}

	cols, insertFields := sm.insertableColumns()
	if len(cols) == 0 {
		return errors.NewCode(errors.InvalidInput, "basic.Model.Create: no insertable columns").
			WithContext("entity_type", fmt.Sprintf("%T", first))
	}

	// 记录第一个实体的基础类型，用于后续一致性校验
	firstType := sm.typ

	rows := make([][]any, 0, len(entities))
	for _, e := range entities {
		val, ok := writableStructValue(e)
		if !ok {
			return errors.NewCode(errors.InvalidInput, "basic.Model.Create: entity must be struct or *struct").
				WithContext("entity_type", fmt.Sprintf("%T", e))
		}
		// 禁止批量插入混合类型：不同类型的字段 index 不同，用第一个类型的
		// insertFields 去读其他类型的字段会写入错误数据。
		if val.Type() != firstType {
			return errors.NewCode(errors.InvalidInput, "basic.Model.Create: all entities must be the same type").
				WithContext("expected_type", firstType.String()).
				WithContext("actual_type", fmt.Sprintf("%T", e))
		}

		rowVals := make([]any, len(insertFields))
		for i, fi := range insertFields {
			fv := fieldByIndexSafe(val, fi.Index)
			rowVals[i] = dbWriteValue(fv)
		}
		rows = append(rows, rowVals)
	}

	maxRows := m.createMaxParams() / len(cols)
	if maxRows < 1 {
		maxRows = 1
	}
	if len(rows) <= maxRows || isTransactionDatabase(m.orm.db) {
		return m.createRows(ctx, cols, rows, maxRows)
	}

	session, err := m.orm.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = session.Rollback()
		}
	}()
	txModel, err := session.Model(m.meta)
	if err != nil {
		return err
	}
	typed, ok := txModel.(*model)
	if !ok {
		return errors.NewCode(errors.Internal, "unexpected lite model type")
	}
	if err := typed.createRows(ctx, cols, rows, maxRows); err != nil {
		return err
	}
	if err := session.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (m *model) createRows(ctx context.Context, cols []string, rows [][]any, maxRows int) error {
	for start := 0; start < len(rows); start += maxRows {
		end := start + maxRows
		if end > len(rows) {
			end = len(rows)
		}

		builder := m.orm.sql.InsertInto(m.table).Columns(cols...)
		for _, rowVals := range rows[start:end] {
			builder = builder.Values(rowVals...)
		}
		if _, err := builder.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func isTransactionDatabase(database db.IDatabase) bool {
	_, ok := database.(db.ITransaction)
	return ok
}

// Save 保存数据。
func (m *model) Save(ctx context.Context, entity any, opts ...orm.QueryOption) error {
	builder, err := m.buildSaveBuilder(entity, opts...)
	if err != nil {
		return err
	}
	_, err = builder.Exec(ctx)
	return err
}

// SaveWithResult 保存带结果。
func (m *model) SaveWithResult(ctx context.Context, entity any, opts ...orm.QueryOption) (sql.Result, error) {
	builder, err := m.buildSaveBuilder(entity, opts...)
	if err != nil {
		return nil, err
	}
	return builder.Exec(ctx)
}

// buildSaveBuilder 构建 Save/SaveWithResult 所需的 UPDATE builder。
func (m *model) buildSaveBuilder(entity any, opts ...orm.QueryOption) (sqlbuilder.IUpdateBuilder, error) {
	val, ok := writableStructValue(entity)
	if !ok {
		return nil, errors.NewCode(errors.InvalidInput, "basic.Model.Save: entity must be struct or *struct").
			WithContext("entity_type", fmt.Sprintf("%T", entity))
	}

	sm, metaErr := m.orm.structMetaForValue(entity)
	if metaErr != nil {
		return nil, metaErr
	}
	if sm == nil {
		return nil, errors.NewCode(errors.InvalidInput, "basic.Model.Save: unsupported entity type").
			WithContext("entity_type", fmt.Sprintf("%T", entity))
	}

	qo := orm.CollectQueryOptions(opts...)
	if !hasEffectiveWhere(qo.Where) {
		return nil, errors.NewCode(errors.InvalidInput, "basic.Orm: update without where is not allowed")
	}

	builder := m.orm.sql.Update(m.table)

	for _, fi := range sm.fields {
		// 跳过自增主键
		if fi.PrimaryKey && fi.AutoIncrement {
			continue
		}
		fv := fieldByIndexSafe(val, fi.Index)
		if !fv.IsValid() {
			continue
		}
		builder = builder.Set(fi.Column, dbWriteValue(fv))
	}

	builder = applyUpdateWhere(builder, qo.Where)

	return builder, nil
}

// UpdateValues 更新对象并写入存储。
//
// 说明：
// - UpdateValues 根据 values 与 QueryOptions 进行更新。
func (m *model) UpdateValues(ctx context.Context, values map[string]any, opts ...orm.QueryOption) error {
	if len(values) == 0 {
		return nil
	}

	qo := orm.CollectQueryOptions(opts...)
	if !hasEffectiveWhere(qo.Where) {
		return errors.NewCode(errors.InvalidInput, "basic.Orm: update without where is not allowed")
	}

	builder := m.orm.sql.Update(m.table).SetMap(values)
	builder = applyUpdateWhere(builder, qo.Where)

	_, err := builder.Exec(ctx)
	return err
}

type noopResult int64

func (r noopResult) LastInsertId() (int64, error) { return 0, nil }

func (r noopResult) RowsAffected() (int64, error) { return int64(r), nil }

// UpdateValuesWithResult 更新对象并写入存储。
func (m *model) UpdateValuesWithResult(ctx context.Context, values map[string]any, opts ...orm.QueryOption) (sql.Result, error) {
	if len(values) == 0 {
		return noopResult(0), nil
	}

	qo := orm.CollectQueryOptions(opts...)
	if !hasEffectiveWhere(qo.Where) {
		return nil, errors.NewCode(errors.InvalidInput, "basic.Orm: update without where is not allowed")
	}

	builder := m.orm.sql.Update(m.table).SetMap(values)
	builder = applyUpdateWhere(builder, qo.Where)

	return builder.Exec(ctx)
}

func hasEffectiveWhere(where []orm.Condition) bool {
	for _, w := range where {
		if strings.TrimSpace(w.Expr) != "" {
			return true
		}
	}
	return false
}

func applyUpdateWhere(builder sqlbuilder.IUpdateBuilder, where []orm.Condition) sqlbuilder.IUpdateBuilder {
	for _, w := range where {
		if strings.TrimSpace(w.Expr) == "" {
			continue
		}
		builder = builder.Where(w.Expr, w.Args...)
	}
	return builder
}

func applyDeleteWhere(builder sqlbuilder.IDeleteBuilder, where []orm.Condition) sqlbuilder.IDeleteBuilder {
	for _, w := range where {
		if strings.TrimSpace(w.Expr) == "" {
			continue
		}
		builder = builder.Where(w.Expr, w.Args...)
	}
	return builder
}

// Delete 删除对象并同步到存储。
//
// 说明：
//   - Delete 根据 QueryOptions 删除记录；
//   - WithLimit 需要方言支持 `DELETE ... LIMIT`；不支持的方言（SQLite/PostgreSQL）
//     返回 Unsupported，而不是静默丢弃 LIMIT 删除全部匹配行。
func (m *model) Delete(ctx context.Context, opts ...orm.QueryOption) error {
	qo := orm.CollectQueryOptions(opts...)
	if !hasEffectiveWhere(qo.Where) {
		return errors.NewCode(errors.InvalidInput, "basic.Orm: delete without where is not allowed")
	}

	builder := m.orm.sql.DeleteFrom(m.table)
	builder = applyDeleteWhere(builder, qo.Where)
	if qo.Limit > 0 {
		builder = builder.Limit(qo.Limit)
	}
	_, err := builder.Exec(ctx)
	return err
}

// DeleteWithResult 删除对象并同步到存储。
//
// 说明：
// - WithLimit 需要方言支持 `DELETE ... LIMIT`；不支持的方言返回 Unsupported。
func (m *model) DeleteWithResult(ctx context.Context, opts ...orm.QueryOption) (sql.Result, error) {
	qo := orm.CollectQueryOptions(opts...)
	if !hasEffectiveWhere(qo.Where) {
		return nil, errors.NewCode(errors.InvalidInput, "basic.Orm: delete without where is not allowed")
	}

	builder := m.orm.sql.DeleteFrom(m.table)
	builder = applyDeleteWhere(builder, qo.Where)
	if qo.Limit > 0 {
		builder = builder.Limit(qo.Limit)
	}
	return builder.Exec(ctx)
}

func writableStructValue(entity any) (reflect.Value, bool) {
	val := reflect.ValueOf(entity)
	if !val.IsValid() {
		return reflect.Value{}, false
	}
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return reflect.Value{}, false
		}
		val = val.Elem()
	} else {
		copyVal := reflect.New(val.Type()).Elem()
		copyVal.Set(val)
		val = copyVal
	}
	if val.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return val, true
}

func dbWriteValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() != reflect.Ptr && !v.Type().Implements(driverValuerType) && v.CanAddr() {
		addr := v.Addr()
		if addr.Type().Implements(driverValuerType) {
			return addr.Interface()
		}
	}
	return v.Interface()
}
