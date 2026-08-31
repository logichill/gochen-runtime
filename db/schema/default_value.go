package schema

import "strings"

// NormalizeDefaultValue 归一化跨方言常见的列默认值字面量，减少伪 drift。
func NormalizeDefaultValue(columnType string, value string, _ string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	value = stripOuterParentheses(value)
	value = stripTopLevelTypeCasts(value)
	value = stripOuterParentheses(value)

	if unquoted, ok := parseSQLStringLiteral(value); ok {
		return quoteSQLString(unquoted)
	}

	if normalized, ok := normalizeSpecialSQLDefault(value); ok {
		return normalized
	}
	if shouldTreatBareValueAsString(columnType, value) {
		return quoteSQLString(value)
	}

	return value
}

func stripOuterParentheses(value string) string {
	for {
		value = strings.TrimSpace(value)
		if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
			return value
		}
		depth := 0
		enclosed := true
		inQuote := false
		for i := 0; i < len(value); i++ {
			switch value[i] {
			case '\'':
				if inQuote {
					if i+1 < len(value) && value[i+1] == '\'' {
						i++
						continue
					}
					inQuote = false
				} else {
					inQuote = true
				}
			case '(':
				if !inQuote {
					depth++
				}
			case ')':
				if !inQuote {
					depth--
					if depth == 0 && i != len(value)-1 {
						enclosed = false
						break
					}
				}
			}
		}
		if !enclosed || depth != 0 || inQuote {
			return value
		}
		value = value[1 : len(value)-1]
	}
}

func stripTopLevelTypeCasts(value string) string {
	for {
		idx := findTopLevelTypeCast(value)
		if idx < 0 || !looksLikeTypeAnnotation(value[idx+2:]) {
			return value
		}
		value = strings.TrimSpace(value[:idx])
		value = stripOuterParentheses(value)
	}
}

func findTopLevelTypeCast(value string) int {
	depth := 0
	inQuote := false
	last := -1
	for i := 0; i+1 < len(value); i++ {
		switch value[i] {
		case '\'':
			if inQuote {
				if i+1 < len(value) && value[i+1] == '\'' {
					i++
					continue
				}
				inQuote = false
			} else {
				inQuote = true
			}
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote && depth > 0 {
				depth--
			}
		case ':':
			if !inQuote && depth == 0 && value[i+1] == ':' {
				last = i
			}
		}
	}
	return last
}

func looksLikeTypeAnnotation(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '"' || r == '.' || r == '[' || r == ']' || r == ' ':
		default:
			return false
		}
	}
	return true
}

func parseSQLStringLiteral(value string) (string, bool) {
	if len(value) < 2 || value[0] != '\'' || value[len(value)-1] != '\'' {
		return "", false
	}
	var builder strings.Builder
	for i := 1; i < len(value)-1; i++ {
		if value[i] == '\'' {
			if i+1 >= len(value)-1 || value[i+1] != '\'' {
				return "", false
			}
			builder.WriteByte('\'')
			i++
			continue
		}
		builder.WriteByte(value[i])
	}
	return builder.String(), true
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func shouldTreatBareValueAsString(columnType string, value string) bool {
	if !isTextualColumnType(columnType) {
		return false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n()[]{}+-*/%<>=!|&,:\"") {
		return false
	}
	return true
}

func isTextualColumnType(columnType string) bool {
	columnType = strings.ToUpper(strings.TrimSpace(columnType))
	switch {
	case strings.HasPrefix(columnType, "CHAR"),
		strings.HasPrefix(columnType, "VARCHAR"),
		strings.HasPrefix(columnType, "TEXT"),
		strings.HasPrefix(columnType, "TINYTEXT"),
		strings.HasPrefix(columnType, "MEDIUMTEXT"),
		strings.HasPrefix(columnType, "LONGTEXT"),
		strings.HasPrefix(columnType, "NCHAR"),
		strings.HasPrefix(columnType, "NVARCHAR"),
		strings.HasPrefix(columnType, "CLOB"):
		return true
	default:
		return false
	}
}

func normalizeSpecialSQLDefault(value string) (string, bool) {
	switch strings.ToUpper(value) {
	case "NULL", "TRUE", "FALSE", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
		return strings.ToUpper(value), true
	default:
		return "", false
	}
}
