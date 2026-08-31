package schema

import "strings"

// ParseTableName 把 table、schema.table 或更长限定名拆成限定前缀与表名。
//
// 当 name 包含点号时按最后一个点拆分，例如 catalog.schema.table 会得到
// schemaName=catalog.schema、tableName=table。本函数只做字符串拆分，不表示
// 所有 SQL 渲染或安全校验入口都支持多段限定名。
func ParseTableName(name string) (schemaName string, tableName string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return strings.TrimSpace(name[:idx]), strings.TrimSpace(name[idx+1:])
	}
	return "", name
}
