package safeident

import (
	"fmt"
	"strings"
)

// IsSafeIdentifier 判断标识符是否为“安全的数据库标识符”。
func IsSafeIdentifier(name string) bool {
	if name == "" {
		return false
	}
	parts := strings.Split(name, ".")
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			ch := part[i]
			if i == 0 {
				if !((ch >= 'a' && ch <= 'z') ||
					(ch >= 'A' && ch <= 'Z') ||
					ch == '_') {
					return false
				}
			} else {
				if !((ch >= 'a' && ch <= 'z') ||
					(ch >= 'A' && ch <= 'Z') ||
					(ch >= '0' && ch <= '9') ||
					ch == '_') {
					return false
				}
			}
		}
	}
	return true
}

// ValidateQualifiedIdentifier 校验限定标识符，允许最多 maxParts 段。
func ValidateQualifiedIdentifier(name, source string, maxParts int) error {
	name = strings.TrimSpace(name)
	source = strings.TrimSpace(source)
	if source == "" {
		source = "identifier"
	}
	if name == "" {
		return fmt.Errorf("%s cannot be empty", source)
	}
	if maxParts <= 0 {
		maxParts = 1
	}
	parts := strings.Split(name, ".")
	if len(parts) > maxParts {
		return fmt.Errorf("%s can only contain up to %d segment(s)", source, maxParts)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("%s segment cannot be empty", source)
		}
		if !IsSafeIdentifier(part) {
			return fmt.Errorf("invalid %s segment: %s", source, part)
		}
	}
	return nil
}

// NormalizeQualifiedIdentifier 返回 trim 后的安全限定标识符。
func NormalizeQualifiedIdentifier(name, fallback, source string, maxParts int) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}
	if err := ValidateQualifiedIdentifier(name, source, maxParts); err != nil {
		return "", err
	}
	return name, nil
}
