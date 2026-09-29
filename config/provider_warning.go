package config

import (
	"fmt"
	"strings"

	"gochen/errors"
)

// getRequired 读取一个必需配置项；缺失时返回校验错误。
func (p *Provider) getRequired(key string) (any, error) {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return nil, errors.NewCode(errors.Validation, "missing configuration").
			WithContext("key", key)
	}
	return v, nil
}

// isSensitiveKey 判断给定 key 是否疑似包含敏感信息。
func isSensitiveKey(key string) bool {
	// Conservative heuristic: dot-separated keys, but we also do substring checks
	// because callers may use various conventions (snakeCase, kebab, etc).
	k := strings.ToLower(key)
	needles := []string{
		"password",
		"passwd",
		"secret",
		"token",
		"api_key",
		"apikey",
		"access_key",
		"accesskey",
		"client_secret",
		"clientsecret",
		"private_key",
		"privatekey",
		"signing_key",
		"signingkey",
	}
	for _, n := range needles {
		if strings.Contains(k, n) {
			return true
		}
	}

	// Catch common segment names like "... .key" without matching arbitrary words.
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		if seg == "key" {
			return true
		}
	}
	return false
}

// redactIfSensitive 在敏感 key 上返回脱敏后的占位值。
func (p *Provider) redactIfSensitive(key string, value any) any {
	if isSensitiveKey(key) {
		return "[REDACTED]"
	}
	return value
}

// warnInvalid 记录一次宽松读取的类型转换告警。
func (p *Provider) warnInvalid(key string, expected string, value any, msg string) {
	w := ConfigWarning{
		Key:      key,
		Expected: expected,
		Actual:   fmt.Sprintf("%T", value),
		Value:    p.redactIfSensitive(key, value),
		Message:  msg,
	}

	p.warnMu.Lock()
	if p.maxWarns > 0 {
		if len(p.warnings) >= p.maxWarns {
			copy(p.warnings, p.warnings[1:])
			p.warnings[len(p.warnings)-1] = w
		} else {
			p.warnings = append(p.warnings, w)
		}
	}
	handler := p.onWarning
	p.warnMu.Unlock()

	if handler != nil {
		handler(w)
	}
}

// Warnings 返回宽松读取器记录的告警信息。
func (p *Provider) Warnings() []ConfigWarning {
	p.warnMu.Lock()
	defer p.warnMu.Unlock()

	out := make([]ConfigWarning, len(p.warnings))
	copy(out, p.warnings)
	return out
}

// invalidConfig 构造一条标准化的配置类型错误。
func (p *Provider) invalidConfig(key string, expected string, value any) error {
	return errors.NewCode(errors.Validation, "invalid configuration").
		WithContext("key", key).
		WithContext("expected", expected).
		WithContext("actual", fmt.Sprintf("%T", value)).
		WithContext("value", p.redactIfSensitive(key, value))
}

// wrapInvalidConfig 把底层解析错误包装成配置错误，并在敏感 key 上避免泄露原值。
func (p *Provider) wrapInvalidConfig(key string, expected string, value any, cause error) error {
	// Parsing errors often include the original value in their message (e.g.
	// strconv.ParseInt: parsing "xxx": ...). Avoid chaining the cause for secrets.
	if isSensitiveKey(key) {
		return p.invalidConfig(key, expected, value)
	}

	return errors.Wrap(cause, errors.Validation, "invalid configuration").
		WithContext("key", key).
		WithContext("expected", expected).
		WithContext("actual", fmt.Sprintf("%T", value)).
		WithContext("value", p.redactIfSensitive(key, value))
}
