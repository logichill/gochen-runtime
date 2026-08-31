package config

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Get 返回原始配置值，不做类型转换。
func (p *Provider) Get(key string) any {
	return p.settings[key]
}

// GetString 读取字符串配置；缺失或类型不匹配时返回默认值并记录 warning。
func (p *Provider) GetString(key string, defaultValue string) string {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}

	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	}

	p.warnInvalid(key, "string", v, "type mismatch; using default")
	return defaultValue
}

// GetStringStrict 获取字符串配置（严格模式）。
func (p *Provider) GetStringStrict(key string) (string, error) {
	v, err := p.getRequired(key)
	if err != nil {
		return "", err
	}

	switch val := v.(type) {
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	default:
		return "", p.invalidConfig(key, "string", v)
	}
}

// GetInt 读取整数配置；缺失或类型不匹配时返回默认值并记录 warning。
func (p *Provider) GetInt(key string, defaultValue int) int {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}

	i, err := p.parseIntValue(key, v)
	if err != nil {
		p.warnInvalid(key, "int", v, "failed to parse int; using default")
		return defaultValue
	}
	return i
}

// GetIntStrict 获取整数配置（严格模式）。
func (p *Provider) GetIntStrict(key string) (int, error) {
	v, err := p.getRequired(key)
	if err != nil {
		return 0, err
	}

	return p.parseIntValue(key, v)
}

func (p *Provider) parseIntValue(key string, v any) (int, error) {
	maxInt := int64(int(^uint(0) >> 1))
	minInt := -maxInt - 1

	switch val := v.(type) {
	case int:
		return val, nil
	case int64:
		if val > maxInt || val < minInt {
			return 0, p.invalidConfig(key, "int", v)
		}
		return int(val), nil
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) || math.Trunc(val) != val {
			return 0, p.invalidConfig(key, "int", v)
		}
		parsed, err := strconv.ParseInt(strconv.FormatFloat(val, 'f', 0, 64), 10, 64)
		if err != nil || parsed > maxInt || parsed < minInt {
			return 0, p.invalidConfig(key, "int", v)
		}
		return int(parsed), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return 0, p.wrapInvalidConfig(key, "int", v, err)
		}
		return i, nil
	default:
		return 0, p.invalidConfig(key, "int", v)
	}
}

// GetInt64 读取 int64 配置；缺失或类型不匹配时返回默认值并记录 warning。
func (p *Provider) GetInt64(key string, defaultValue int64) int64 {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}

	i, err := p.parseInt64Value(key, v)
	if err != nil {
		p.warnInvalid(key, "int64", v, "failed to parse int64; using default")
		return defaultValue
	}
	return i
}

func (p *Provider) parseInt64Value(key string, v any) (int64, error) {
	switch val := v.(type) {
	case int64:
		return val, nil
	case int32:
		return int64(val), nil
	case int16:
		return int64(val), nil
	case int8:
		return int64(val), nil
	case int:
		return int64(val), nil
	case float64:
		if math.Trunc(val) != val {
			return 0, p.invalidConfig(key, "int64", v)
		}
		if val >= float64(math.MaxInt64) || val < float64(math.MinInt64) {
			return 0, p.invalidConfig(key, "int64", v)
		}
		return int64(val), nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, p.wrapInvalidConfig(key, "int64", v, err)
		}
		return i, nil
	default:
		return 0, p.invalidConfig(key, "int64", v)
	}
}

// GetFloat64 读取浮点配置；缺失或类型不匹配时返回默认值并记录 warning。
func (p *Provider) GetFloat64(key string, defaultValue float64) float64 {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}

	f, err := p.parseFloat64Value(key, v)
	if err != nil {
		p.warnInvalid(key, "float64", v, "failed to parse float64; using default")
		return defaultValue
	}
	return f
}

func (p *Provider) parseFloat64Value(key string, v any) (float64, error) {
	switch val := v.(type) {
	case float64:
		if !isFiniteFloat(val) {
			return 0, p.invalidConfig(key, "float64", v)
		}
		return val, nil
	case float32:
		f := float64(val)
		if !isFiniteFloat(f) {
			return 0, p.invalidConfig(key, "float64", v)
		}
		return f, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return 0, p.wrapInvalidConfig(key, "float64", v, err)
		}
		if !isFiniteFloat(f) {
			return 0, p.invalidConfig(key, "float64", v)
		}
		return f, nil
	default:
		return 0, p.invalidConfig(key, "float64", v)
	}
}

// GetBool 读取布尔配置；缺失或类型不匹配时返回默认值并记录 warning。
func (p *Provider) GetBool(key string, defaultValue bool) bool {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}

	switch val := v.(type) {
	case bool:
		return val
	case string:
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		default:
			p.warnInvalid(key, "bool", v, "failed to parse bool; using default")
			return defaultValue
		}
	case int:
		return val != 0
	case int64:
		return val != 0
	}

	p.warnInvalid(key, "bool", v, "type mismatch; using default")
	return defaultValue
}

// GetBoolStrict 获取布尔配置（严格模式）。
func (p *Provider) GetBoolStrict(key string) (bool, error) {
	v, err := p.getRequired(key)
	if err != nil {
		return false, err
	}

	switch val := v.(type) {
	case bool:
		return val, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off":
			return false, nil
		default:
			return false, p.invalidConfig(key, "bool", v)
		}
	case int:
		return val != 0, nil
	case int64:
		return val != 0, nil
	default:
		return false, p.invalidConfig(key, "bool", v)
	}
}

// GetDuration 读取 duration 配置；数值输入会按秒解释。
func (p *Provider) GetDuration(key string, defaultValue time.Duration) time.Duration {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return defaultValue
	}
	duration, err := p.parseDurationValue(key, v)
	if err != nil {
		p.warnInvalid(key, "duration", v, "failed to parse duration; using default")
		return defaultValue
	}
	return duration
}

// GetDurationStrict 获取时间间隔配置（严格模式）。
func (p *Provider) GetDurationStrict(key string) (time.Duration, error) {
	v, err := p.getRequired(key)
	if err != nil {
		return 0, err
	}
	return p.parseDurationValue(key, v)
}

// GetStringSlice 读取字符串切片配置，并兼容逗号分隔的字符串输入。
func (p *Provider) GetStringSlice(key string, defaultValue []string) []string {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return append([]string(nil), defaultValue...)
	}

	result, err := p.parseStringSliceValue(key, v)
	if err != nil {
		p.warnInvalid(key, "[]string", v, "type mismatch; using default")
		return append([]string(nil), defaultValue...)
	}
	return result
}

func (p *Provider) parseStringSliceValue(key string, v any) ([]string, error) {
	switch val := v.(type) {
	case []string:
		return append([]string(nil), val...), nil
	case []any:
		return stringifySlice(val), nil
	case string:
		parts := strings.Split(val, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			s := strings.TrimSpace(part)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		return nil, p.invalidConfig(key, "[]string", v)
	}
}

func stringSliceValueForType(raw []string, targetType reflect.Type) reflect.Value {
	result := reflect.MakeSlice(targetType, 0, len(raw))
	for _, item := range raw {
		elem := reflect.New(targetType.Elem()).Elem()
		elem.SetString(item)
		result = reflect.Append(result, elem)
	}
	return result
}

func (p *Provider) parseIntSliceValue(key string, v any, targetType reflect.Type) (reflect.Value, error) {
	values, err := sliceInputValues(v)
	if err != nil {
		return reflect.Value{}, p.invalidConfig(key, targetType.String(), v)
	}
	result := reflect.MakeSlice(targetType, 0, len(values))
	for _, value := range values {
		parsed, err := p.parseInt64Value(key, value)
		if err != nil {
			return reflect.Value{}, err
		}
		elem := reflect.New(targetType.Elem()).Elem()
		if elem.OverflowInt(parsed) {
			return reflect.Value{}, p.invalidConfig(key, targetType.String(), v)
		}
		elem.SetInt(parsed)
		result = reflect.Append(result, elem)
	}
	return result, nil
}

func (p *Provider) parseFloatSliceValue(key string, v any, targetType reflect.Type) (reflect.Value, error) {
	values, err := sliceInputValues(v)
	if err != nil {
		return reflect.Value{}, p.invalidConfig(key, targetType.String(), v)
	}
	result := reflect.MakeSlice(targetType, 0, len(values))
	for _, value := range values {
		parsed, err := p.parseFloat64Value(key, value)
		if err != nil {
			return reflect.Value{}, err
		}
		elem := reflect.New(targetType.Elem()).Elem()
		if elem.OverflowFloat(parsed) {
			return reflect.Value{}, p.invalidConfig(key, targetType.String(), v)
		}
		elem.SetFloat(parsed)
		result = reflect.Append(result, elem)
	}
	return result, nil
}

func sliceInputValues(v any) ([]any, error) {
	switch val := v.(type) {
	case []any:
		return append([]any(nil), val...), nil
	case string:
		parts := strings.Split(val, ",")
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			s := strings.TrimSpace(part)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		rv := reflect.ValueOf(v)
		if !rv.IsValid() || rv.Kind() != reflect.Slice {
			return nil, fmt.Errorf("not a slice")
		}
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return out, nil
	}
}

func stringifySlice(values []any) []string {
	result := make([]string, len(values))
	for i, item := range values {
		result[i] = fmt.Sprintf("%v", item)
	}
	return result
}

// GetStringMap 读取字符串映射配置，并兼容 `map[string]any` 输入。
func (p *Provider) GetStringMap(key string, defaultValue map[string]string) map[string]string {
	v, ok := p.settings[key]
	if !ok || v == nil {
		return cloneStringMap(defaultValue)
	}

	switch val := v.(type) {
	case map[string]string:
		result := make(map[string]string, len(val))
		for k, v := range val {
			result[k] = v
		}
		return result
	case map[string]any:
		result := make(map[string]string, len(val))
		for k, v := range val {
			result[k] = fmt.Sprintf("%v", v)
		}
		return result
	}

	p.warnInvalid(key, "map[string]string", v, "type mismatch; using default")
	return cloneStringMap(defaultValue)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
