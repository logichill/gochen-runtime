package sqlbuilder

import (
	coredialect "gochen/db/dialect"
	"strings"

	"gochen-runtime/db/sqlscan"
	"gochen/errors"
)

// groupWhereCondition 保留单个 WHERE 条件的布尔边界，避免后续 AND 被顶层 OR/XOR 绕过。
// 复用 SQL 词法扫描跳过字符串、标识符和注释；已在括号内的条件无需再包裹。
func groupWhereCondition(expr string, d coredialect.IDialect) string {
	depth := 0
	for i := 0; i < len(expr); {
		if next, ignored := scanConditionIgnored(expr, i, d); ignored {
			if next == len(expr) && (expr[i] == '-' || expr[i] == '#') {
				// 单条件也可能以行注释结束；补换行保护后续 AND、OR 和 LIMIT。
				return expr + "\n"
			}
			i = next
			continue
		}
		switch expr[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '|':
			if depth == 0 && i+1 < len(expr) && expr[i+1] == '|' {
				return "(" + expr + "\n)"
			}
		}
		if sqlscan.IsIdentStart(expr[i]) {
			start := i
			for i < len(expr) && sqlscan.IsIdentPart(expr[i]) {
				i++
			}
			if depth == 0 && (strings.EqualFold(expr[start:i], "OR") || strings.EqualFold(expr[start:i], "XOR")) {
				// 换行使尾部 -- 注释不会吞掉闭括号。
				return "(" + expr + "\n)"
			}
			continue
		}
		i++
	}
	return expr
}

// scanConditionIgnored 为条件分组和参数展开统一跳过字面量、标识符与注释。
func scanConditionIgnored(expr string, start int, d coredialect.IDialect) (int, bool) {
	if d.PlaceholderStyle() == coredialect.PlaceholderPostgres {
		return sqlscan.ScanPostgresPlaceholderIgnored(expr, start)
	}
	switch ch := expr[start]; ch {
	case '\'', '"', '`':
		if d.Name() == coredialect.NameMySQL && ch != '`' {
			// MySQL 普通字符串允许反斜杠转义；参数内容不经过本扫描。
			for i := start + 1; i < len(expr); i++ {
				if expr[i] == '\\' {
					i++
					continue
				}
				if expr[i] == ch {
					if i+1 < len(expr) && expr[i+1] == ch {
						i++
						continue
					}
					return i + 1, true
				}
			}
			return len(expr), true
		}
		return sqlscan.ScanQuoted(expr, start, ch), true
	case '[':
		if d.Name() == coredialect.NameSQLite {
			if end := strings.IndexByte(expr[start+1:], ']'); end >= 0 {
				return start + end + 2, true
			}
			return len(expr), true
		}
	case '#':
		if d.Name() == coredialect.NameMySQL {
			return sqlscan.ScanLineComment(expr, start), true
		}
	case '-':
		if start+1 < len(expr) && expr[start+1] == '-' {
			if d.Name() != coredialect.NameMySQL || start+2 == len(expr) || expr[start+2] <= ' ' {
				return sqlscan.ScanLineComment(expr, start), true
			}
		}
	case '/':
		if start+1 < len(expr) && expr[start+1] == '*' {
			// SQLite/MySQL 在首个 */ 结束块注释；只有 PostgreSQL 支持嵌套。
			if end := strings.Index(expr[start+2:], "*/"); end >= 0 {
				return start + end + 4, true
			}
			return len(expr), true
		}
	}
	return start, false
}

// validateCondition 在分组前拒绝语句截断符、未闭合词法单元和无法配对的括号。
func validateCondition(expr string, d coredialect.IDialect) error {
	// SQLite 驱动会在 NUL 处终止 SQL，即使 NUL 位于注释内也会吞掉
	// 后续租户条件。这里只检查 SQL 文本，绑定参数中的 NUL 不受影响。
	if strings.IndexByte(expr, 0) >= 0 {
		return errors.NewCode(errors.InvalidInput, "NUL is not allowed in SQL conditions")
	}
	depth := 0
	for i := 0; i < len(expr); i++ {
		if d.Name() == coredialect.NameMySQL && (strings.HasPrefix(expr[i:], "/*!") || strings.HasPrefix(expr[i:], "/*M!")) {
			// 可执行注释内可能包含 OR 等表达式；禁止把它当作普通注释绕过条件分组。
			// 普通注释和 /*+ ... */ 优化器提示不受影响。
			return errors.NewCode(errors.InvalidInput, "executable SQL comments are not allowed in conditions")
		}
		if next, ignored := scanConditionIgnored(expr, i, d); ignored {
			if next == len(expr) && expr[i] != '-' && expr[i] != '#' {
				// 共享扫描器对“恰好闭合”和“未闭合”均返回 EOF；追加空格重扫，
				// 仅未闭合的字符串、标识符或块注释会继续吞掉该空格。
				if extendedNext, _ := scanConditionIgnored(expr+" ", i, d); extendedNext > len(expr) {
					return errors.NewCode(errors.InvalidInput, "unterminated SQL condition token")
				}
			}
			i = next - 1
			continue
		}
		switch expr[i] {
		case ';':
			// 条件只允许一条语句中的表达式；引号或注释内的分号已被跳过。
			return errors.NewCode(errors.InvalidInput, "statement separators are not allowed in SQL conditions")
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return errors.NewCode(errors.InvalidInput, "unbalanced SQL condition parentheses")
			}
		}
	}
	if depth != 0 {
		return errors.NewCode(errors.InvalidInput, "unbalanced SQL condition parentheses")
	}
	return nil
}
