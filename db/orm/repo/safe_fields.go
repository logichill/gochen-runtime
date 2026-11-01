package repo

// isAllowedField 检查字段名是否属于实体元数据。
func (quer *queryBuilder) isAllowedField(field string) bool {
	_, ok := quer.resolveAllowedColumn(field)
	return ok
}

// resolveAllowedColumn 把字段名、列名或字段角色解析为持久化列名。
//
// 解析器同时是动态字段查询的白名单：不在实体元数据里的名字一律拒绝。
func (quer *queryBuilder) resolveAllowedColumn(field string) (string, bool) {
	if quer == nil || quer.resolver == nil {
		return "", false
	}
	return quer.resolver.columnOf(field)
}

func (quer *queryBuilder) resolveQuotedAllowedColumn(field string) (string, bool) {
	column, ok := quer.resolveAllowedColumn(field)
	if !ok {
		return "", false
	}
	return quer.resolver.quote(column), true
}
