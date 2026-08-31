package migrate

import (
	"strings"

	"gochen/db/sqlscan"
)

// SplitStatements 把常见 SQL migration 文件拆成按顺序执行的语句。
//
// 说明：该函数覆盖普通 DDL/DML、单/双引号、反引号、PostgreSQL dollar-quoted block、
// 行注释与块注释；复杂脚本可由后续方言 splitter 扩展。
func SplitStatements(sqlText string) []string {
	var (
		out []string
		sb  strings.Builder
	)

	for i := 0; i < len(sqlText); {
		if end, ok := scanQuotedSQLSpan(sqlText, i); ok {
			sb.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if end, ok := scanSQLComment(sqlText, i); ok {
			sb.WriteString(sqlText[i:end])
			i = end
			continue
		}
		if sqlText[i] == ';' {
			appendStatement(&out, sb.String())
			sb.Reset()
			i++
			continue
		}
		sb.WriteByte(sqlText[i])
		i++
	}
	appendStatement(&out, sb.String())
	return out
}

func scanQuotedSQLSpan(text string, start int) (int, bool) {
	if start < 0 || start >= len(text) {
		return start, false
	}
	if end, ok := sqlscan.ScanPostgresPrefixedString(text, start); ok {
		return end, true
	}
	switch text[start] {
	case 0x27:
		return sqlscan.ScanSingleQuoted(text, start), true
	case '"':
		return sqlscan.ScanQuoted(text, start, '"'), true
	case 0x60:
		return sqlscan.ScanQuoted(text, start, 0x60), true
	case '$':
		return sqlscan.ScanDollarQuoted(text, start)
	default:
		return start, false
	}
}

func scanSQLComment(text string, start int) (int, bool) {
	if start < 0 || start >= len(text) {
		return start, false
	}
	switch text[start] {
	case '#':
		return sqlscan.ScanLineComment(text, start), true
	case '-':
		if start+1 < len(text) && text[start+1] == '-' {
			return sqlscan.ScanLineComment(text, start), true
		}
	case '/':
		if start+1 < len(text) && text[start+1] == '*' {
			return sqlscan.ScanNestedBlockComment(text, start), true
		}
	}
	return start, false
}

func appendStatement(out *[]string, statement string) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return
	}
	*out = append(*out, statement)
}
