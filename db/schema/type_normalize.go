package schema

import (
	"fmt"
	"strconv"
	"strings"
)

// NormalizeColumnType 归一化常见列类型表达，减少跨方言/元数据伪差异。
func NormalizeColumnType(columnType string) string {
	typ := strings.TrimSpace(columnType)
	if typ == "" {
		return ""
	}
	if normalized, ok := normalizeNumericColumnType(typ); ok {
		return normalized
	}
	return typ
}

func normalizeNumericColumnType(columnType string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(columnType))
	base, args, ok := splitTypeArguments(upper)
	if !ok {
		return "", false
	}
	if base != "NUMERIC" && base != "DECIMAL" {
		return "", false
	}
	if len(args) == 1 {
		precision, err := strconv.Atoi(args[0])
		if err != nil || precision <= 0 {
			return "", false
		}
		return fmt.Sprintf("%s(%d,0)", base, precision), true
	}
	if len(args) == 2 {
		precision, err1 := strconv.Atoi(args[0])
		scale, err2 := strconv.Atoi(args[1])
		if err1 != nil || err2 != nil || precision <= 0 || scale < 0 {
			return "", false
		}
		return fmt.Sprintf("%s(%d,%d)", base, precision, scale), true
	}
	return "", false
}

func splitTypeArguments(columnType string) (string, []string, bool) {
	start := strings.IndexByte(columnType, '(')
	if start < 0 {
		return strings.TrimSpace(columnType), nil, true
	}
	end := strings.LastIndexByte(columnType, ')')
	if end <= start || end != len(columnType)-1 {
		return "", nil, false
	}
	base := strings.TrimSpace(columnType[:start])
	argText := strings.TrimSpace(columnType[start+1 : end])
	if base == "" || argText == "" {
		return "", nil, false
	}
	parts := strings.Split(argText, ",")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", nil, false
		}
		args = append(args, part)
	}
	return base, args, true
}
