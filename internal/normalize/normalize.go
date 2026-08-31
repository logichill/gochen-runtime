package normalize

import "strings"

// Strings 过滤空字符串、去除前后空白字符并按出现顺序去重。
// 若结果为空则返回 nil。
func Strings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PositiveID 在 value 为正数时返回原值，否则返回 0。
func PositiveID(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return value
}
