package config

import (
	"reflect"
	"strconv"
	"strings"
	"time"
)

func isStructLikeField(value reflect.Value) bool {
	valueType := value.Type()
	if valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	return valueType.Kind() == reflect.Struct && valueType != reflect.TypeOf(time.Duration(0))
}

func yamlFieldName(field reflect.StructField) (string, bool) {
	tag := strings.TrimSpace(field.Tag.Get("yaml"))
	if tag == "-" {
		return "", false
	}
	if tag == "" {
		return strings.ToLower(field.Name), true
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		return strings.ToLower(field.Name), true
	}
	return name, true
}

func appendPath(path []string, segment string) []string {
	if segment == "" {
		return append([]string(nil), path...)
	}
	next := make([]string, 0, len(path)+1)
	next = append(next, path...)
	next = append(next, segment)
	return next
}

func splitEnvPrefix(prefix string) []string {
	parts := strings.Split(prefix, "_")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, strings.ToLower(part))
		}
	}
	return result
}

func pathToEnvKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	parts := make([]string, 0, len(path))
	for _, segment := range path {
		normalized := strings.ToUpper(strings.ReplaceAll(segment, "-", "_"))
		normalized = strings.ReplaceAll(normalized, ".", "_")
		if normalized != "" {
			parts = append(parts, normalized)
		}
	}
	return strings.Join(parts, "_")
}

func splitStringSlice(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func assignIntSliceValue(field reflect.Value, raw string) {
	parts := splitStringSlice(raw)
	result := reflect.MakeSlice(field.Type(), 0, len(parts))
	elemType := field.Type().Elem()
	elemBits := elemType.Bits()
	for _, part := range parts {
		parsed, err := strconv.ParseInt(part, 10, elemBits)
		if err != nil {
			return
		}
		elem := reflect.New(elemType).Elem()
		elem.SetInt(parsed)
		result = reflect.Append(result, elem)
	}
	field.Set(result)
}

func assignFloatSliceValue(field reflect.Value, raw string) {
	parts := splitStringSlice(raw)
	result := reflect.MakeSlice(field.Type(), 0, len(parts))
	elemType := field.Type().Elem()
	elemBits := elemType.Bits()
	for _, part := range parts {
		parsed, err := strconv.ParseFloat(part, elemBits)
		if err != nil || !isFiniteFloat(parsed) {
			return
		}
		elem := reflect.New(elemType).Elem()
		elem.SetFloat(parsed)
		result = reflect.Append(result, elem)
	}
	field.Set(result)
}
