package module

import (
	"strings"

	"gochen/errors"
)

// NormalizeModuleID 规范化模块 ID。
func NormalizeModuleID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return "", errors.NewCode(errors.InvalidInput, "module id cannot be empty")
	}
	raw = strings.ToLower(raw)
	for i, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
			if i == 0 {
				return "", errors.NewCode(errors.InvalidInput, "module id must start with [a-z0-9]").WithContext("id", raw)
			}
		default:
			return "", errors.NewCode(errors.InvalidInput, "module id must match [a-z0-9][a-z0-9_-]*").WithContext("id", raw)
		}
	}
	return raw, nil
}
