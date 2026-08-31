package sqlbuilder

import "gochen-runtime/db/sql/safeident"

// isSafeIdentifier 判断标识符是否为“安全的数据库标识符”。
//
// 说明：
// - 允许形式：
// - - 单一标识符：foo, bar_1
// - - 带点的限定名：schema.table, table.column
// - 规则（按段）：
// - - 每段不能为空；
// - - 首字符必须是字母或下划线 [A-Za-z_]；
// - - 后续字符必须是字母、数字或下划线 [A-Za-z0-9_]。
// - 该函数只做简单的 ASCII 校验，足以防止常见的注入片段（空格、分号等）。
func isSafeIdentifier(name string) bool { return safeident.IsSafeIdentifier(name) }
