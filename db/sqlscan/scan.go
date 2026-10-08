// Package sqlscan 提供数据库包共享的轻量 SQL 词法扫描工具。
package sqlscan

import "strings"

// ScanSingleQuoted 扫描 SQL 单引号字面量，支持标准 SQL 的连续单引号转义。
func ScanSingleQuoted(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] != '\'' {
			continue
		}
		if i+1 < len(text) && text[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return len(text)
}

// ScanBackslashSingleQuoted 扫描 PostgreSQL E'...' 这类带反斜杠转义的字面量。
func ScanBackslashSingleQuoted(text string, quote int) int {
	for i := quote + 1; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
			continue
		}
		if text[i] != '\'' {
			continue
		}
		if i+1 < len(text) && text[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return len(text)
}

// ScanQuoted 扫描使用指定引号包裹的 SQL 标识符或字面量。
func ScanQuoted(text string, start int, quote byte) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] != quote {
			continue
		}
		if i+1 < len(text) && text[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(text)
}

// ScanDollarQuoted 扫描 PostgreSQL dollar-quoted block。
func ScanDollarQuoted(text string, start int) (int, bool) {
	delimiter, ok := ReadDollarQuoteDelimiter(text[start:])
	if !ok {
		return start, false
	}
	end := strings.Index(text[start+len(delimiter):], delimiter)
	if end < 0 {
		return len(text), true
	}
	return start + len(delimiter) + end + len(delimiter), true
}

// ReadDollarQuoteDelimiter 读取 PostgreSQL dollar-quote 分隔符。
func ReadDollarQuoteDelimiter(text string) (string, bool) {
	if text == "" || text[0] != '$' {
		return "", false
	}
	for i := 1; i < len(text); i++ {
		ch := text[i]
		if ch == '$' {
			return text[:i+1], true
		}
		if i == 1 && !IsIdentStart(ch) {
			return "", false
		}
		if i > 1 && !IsIdentPart(ch) {
			return "", false
		}
	}
	return "", false
}

// ScanLineComment 扫描 -- 行注释，返回注释后的下一个位置。
func ScanLineComment(text string, start int) int {
	for i := start; i < len(text); i++ {
		if text[i] == '\n' {
			return i + 1
		}
	}
	return len(text)
}

// scanPostgresLineComment 使用 PostgreSQL 的 CR/LF 行终止规则；
// 通用 ScanLineComment 保留仅 LF 终止的 SQLite/迁移语义。
func scanPostgresLineComment(text string, start int) int {
	for i := start; i < len(text); i++ {
		if text[i] == '\n' || text[i] == '\r' {
			return i + 1
		}
	}
	return len(text)
}

// ScanNestedBlockComment 扫描支持嵌套的 /* ... */ 块注释；start 必须指向起始的 '/'。
func ScanNestedBlockComment(text string, start int) int {
	depth := 0
	for i := start; i < len(text); i++ {
		if text[i] == '/' && i+1 < len(text) && text[i+1] == '*' {
			i++
			depth++
			continue
		}
		if text[i] == '*' && i+1 < len(text) && text[i+1] == '/' {
			i++
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(text)
}

// IsSpace 判断字符是否为 SQL 常见空白。
func IsSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

// IsIdentStart 判断字符是否可作为 SQL 简单标识符开头。
func IsIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

// IsIdentPart 判断字符是否可作为 SQL 简单标识符组成部分。
func IsIdentPart(ch byte) bool {
	return IsIdentStart(ch) || (ch >= '0' && ch <= '9')
}

// ScanPostgresPlaceholderIgnored scans PostgreSQL SQL spans where ? must not be
// treated as a bind placeholder: string literals, quoted identifiers, dollar
// blocks, and comments.
func ScanPostgresPlaceholderIgnored(query string, start int) (int, bool) {
	if start < 0 || start >= len(query) {
		return start, false
	}
	if next, ok := ScanPostgresPrefixedString(query, start); ok {
		return next, true
	}
	switch query[start] {
	case '\'':
		return ScanSingleQuoted(query, start), true
	case '"':
		return ScanQuoted(query, start, '"'), true
	case '$':
		return ScanDollarQuoted(query, start)
	case '-':
		if start+1 < len(query) && query[start+1] == '-' {
			return scanPostgresLineComment(query, start), true
		}
	case '/':
		if start+1 < len(query) && query[start+1] == '*' {
			return ScanNestedBlockComment(query, start), true
		}
	}
	return start, false
}

// ScanPostgresPrefixedString scans PostgreSQL E'...', U&'...', B'...', and
// X'...' literals.
func ScanPostgresPrefixedString(query string, start int) (int, bool) {
	if start > 0 && IsIdentPart(query[start-1]) {
		return start, false
	}
	if start+1 < len(query) && (query[start] == 'E' || query[start] == 'e') && query[start+1] == '\'' {
		return ScanBackslashSingleQuoted(query, start+1), true
	}
	if start+2 < len(query) && (query[start] == 'U' || query[start] == 'u') && query[start+1] == '&' && query[start+2] == '\'' {
		return ScanSingleQuoted(query, start+2), true
	}
	if start+1 < len(query) && (query[start] == 'B' || query[start] == 'b' || query[start] == 'X' || query[start] == 'x') && query[start+1] == '\'' {
		return ScanSingleQuoted(query, start+1), true
	}
	return start, false
}

type tokenBounds struct {
	start int
	end   int
}

// PrevTokenScanner incrementally finds SQL tokens before a position.
type PrevTokenScanner struct {
	query string
	next  int
	last  tokenBounds
}

// NewPrevTokenScanner creates a scanner for previous-token lookup.
func NewPrevTokenScanner(query string) PrevTokenScanner {
	return PrevTokenScanner{
		query: query,
		last:  tokenBounds{start: -1, end: -1},
	}
}

func (s *PrevTokenScanner) previousBefore(before int) (int, int, bool) {
	if s == nil || before <= 0 {
		return 0, 0, false
	}
	for s.next < before {
		start, end, next, ok := ScanSQLToken(s.query, s.next)
		if !ok || start >= before {
			break
		}
		s.last = tokenBounds{start: start, end: end}
		s.next = next
	}
	if s.last.start < 0 {
		return 0, 0, false
	}
	return s.last.start, s.last.end, true
}

// IsPostgresJSONQuestionOperator reports whether query[index] is a PostgreSQL
// JSON existence operator rather than a bind placeholder.
func IsPostgresJSONQuestionOperator(query string, prevScanner *PrevTokenScanner, index int) bool {
	if index < 0 || index >= len(query) || query[index] != '?' {
		return false
	}
	if index+1 < len(query) && (query[index+1] == '|' || query[index+1] == '&') {
		return true
	}
	prevStart, prevEnd, ok := prevScanner.previousBefore(index)
	if !ok {
		return false
	}
	if isPlaceholderContextToken(query[prevStart:prevEnd]) {
		return false
	}
	if !isPostgresJSONLeftOperand(query[prevStart:prevEnd]) {
		return false
	}
	nextStart, nextEnd, ok := NextSQLToken(query, index+1)
	if !ok {
		return false
	}
	if isPlaceholderContextToken(query[nextStart:nextEnd]) {
		return false
	}
	return isSQLValueStart(query[nextStart])
}

// NextSQLToken returns the bounds of the next SQL token after start.
func NextSQLToken(query string, start int) (int, int, bool) {
	tokenStart, tokenEnd, _, ok := ScanSQLToken(query, start)
	if !ok {
		return 0, 0, false
	}
	return tokenStart, tokenEnd, true
}

// ScanSQLToken scans the next token, skipping whitespace and comments.
func ScanSQLToken(query string, start int) (int, int, int, bool) {
	i := SkipSQLTrivia(query, start)
	if i >= len(query) {
		return 0, 0, 0, false
	}
	if next, ok := ScanPostgresPrefixedString(query, i); ok {
		return i, next, next, true
	}
	switch query[i] {
	case '\'':
		next := ScanSingleQuoted(query, i)
		return i, next, next, true
	case '"':
		next := ScanQuoted(query, i, '"')
		return i, next, next, true
	case '$':
		if next, ok := ScanDollarQuoted(query, i); ok {
			return i, next, next, true
		}
	}
	if IsIdentPart(query[i]) {
		start = i
		for i < len(query) && IsIdentPart(query[i]) {
			i++
		}
		return start, i, i, true
	}
	return i, i + 1, i + 1, true
}

// SkipSQLTrivia 跳过 PostgreSQL token 查找所需的空白和注释。
func SkipSQLTrivia(query string, start int) int {
	for i := start; i < len(query); {
		if IsSpace(query[i]) {
			i++
			continue
		}
		if query[i] == '-' && i+1 < len(query) && query[i+1] == '-' {
			i = scanPostgresLineComment(query, i)
			continue
		}
		if query[i] == '/' && i+1 < len(query) && query[i+1] == '*' {
			i = ScanNestedBlockComment(query, i)
			continue
		}
		return i
	}
	return len(query)
}

func isPlaceholderContextToken(token string) bool {
	switch strings.ToUpper(strings.TrimSpace(token)) {
	case "WHERE", "AND", "OR", "NOT", "ON", "WHEN", "THEN", "ELSE", "BY", "SELECT", "VALUES", "SET", "RETURNING", "HAVING", "CASE", "IN", "AS", "FROM", "JOIN", "USING", "LIMIT", "OFFSET", "LIKE", "ILIKE", "IS", "TO", "BETWEEN", "ESCAPE":
		return true
	default:
		return false
	}
}

func isPostgresJSONLeftOperand(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	last := token[len(token)-1]
	return IsIdentPart(last) || last == '\'' || last == '"' || last == ')' || last == ']'
}

func isSQLValueStart(ch byte) bool {
	return ch == '?' || ch == '\'' || ch == '"' || ch == '$' || ch == '(' || IsIdentStart(ch) || (ch >= '0' && ch <= '9')
}
