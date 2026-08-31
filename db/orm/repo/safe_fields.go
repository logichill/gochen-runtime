package repo

import "gochen-runtime/db/sql/safeident"

// isAllowedField 检查字段名是否同时满足“安全标识符”和模型元数据中的字段集合。
//
// 说明：
// - 若无法从模型中获取字段元数据（例如适配器未填充），动态字段查询按 fail-closed 处理。
func (quer *queryBuilder) isAllowedField(field string) bool {
	_, ok := quer.resolveAllowedColumn(field)
	return ok
}

func (quer *queryBuilder) resolveAllowedColumn(field string) (string, bool) {
	if !safeident.IsSafeIdentifier(field) {
		return "", false
	}

	if quer.model == nil {
		return "", false
	}
	meta := quer.model.Meta()
	if meta == nil || len(meta.Fields) == 0 {
		return "", false
	}
	for _, f := range meta.Fields {
		if f.Column == field || f.Name == field {
			return f.Column, true
		}
	}
	return "", false
}

func (quer *queryBuilder) resolveQuotedAllowedColumn(field string) (string, bool) {
	column, ok := quer.resolveAllowedColumn(field)
	if !ok {
		return "", false
	}
	d := quer.model.Dialect()
	if d == nil {
		return "", false
	}
	return d.QuoteIdentifier(column), true
}
