package migrate

import "strings"

// ManualReviewGuardFunction 是保护生成类破坏性 down SQL 的哨兵函数名。
const ManualReviewGuardFunction = "gochen_migration_down_requires_manual_review"

// ManualReviewGuardStatement 是需要人工 review 后手动删除的保护 SQL 语句。
const ManualReviewGuardStatement = "SELECT " + ManualReviewGuardFunction + "()"

func containsManualReviewGuard(sqlText string) bool {
	target := compactGuardStatement(ManualReviewGuardStatement)
	for _, statement := range SplitStatements(sqlText) {
		if compactGuardStatement(statement) == target {
			return true
		}
	}
	return false
}

func compactGuardStatement(statement string) string {
	var sb strings.Builder
	for i := 0; i < len(statement); {
		if end, ok := scanSQLComment(statement, i); ok {
			i = end
			continue
		}
		if end, ok := scanQuotedSQLSpan(statement, i); ok {
			appendCompactedGuardText(&sb, statement[i:end])
			i = end
			continue
		}
		appendCompactedGuardByte(&sb, statement[i])
		i++
	}
	return sb.String()
}

func appendCompactedGuardText(sb *strings.Builder, text string) {
	for i := 0; i < len(text); i++ {
		appendCompactedGuardByte(sb, text[i])
	}
}

func appendCompactedGuardByte(sb *strings.Builder, ch byte) {
	if isGuardSpace(ch) || ch == ';' {
		return
	}
	sb.WriteByte(toLowerASCII(ch))
}

func isGuardSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func toLowerASCII(ch byte) byte {
	if ch >= 'A' && ch <= 'Z' {
		return ch + ('a' - 'A')
	}
	return ch
}
