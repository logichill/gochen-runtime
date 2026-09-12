package config

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func validateTaggedStruct(target any) error {
	var validationErrors ValidationErrors
	validateTaggedValue(reflect.ValueOf(target), nil, &validationErrors)
	if len(validationErrors) > 0 {
		return validationErrors
	}
	return nil
}

func validateTaggedValue(value reflect.Value, path []string, validationErrors *ValidationErrors) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}

	valueType := value.Type()
	for index := 0; index < value.NumField(); index++ {
		fieldValue := value.Field(index)
		fieldType := valueType.Field(index)
		if !fieldType.IsExported() {
			continue
		}
		yamlName, ok := yamlFieldName(fieldType)
		if !ok {
			continue
		}
		fieldPath := appendPath(path, yamlName)

		if isStructLikeField(fieldValue) {
			validateTaggedValue(fieldValue, fieldPath, validationErrors)
		}

		tag := strings.TrimSpace(fieldType.Tag.Get("validate"))
		if tag == "" {
			continue
		}
		if err := validateFieldByTag(fieldValue, tag); err != nil {
			*validationErrors = append(*validationErrors, ValidationError{
				Key:     strings.Join(fieldPath, "."),
				Message: err.Error(),
			})
		}
	}
}

func validateFieldByTag(field reflect.Value, tag string) error {
	if !field.IsValid() {
		return nil
	}

	rules := strings.Split(tag, ",")
	allowEmpty := false
	for i, rule := range rules {
		rules[i] = strings.TrimSpace(rule)
		if rules[i] == "omitempty" {
			allowEmpty = true
		}
	}

	// omitempty 只跳过值检查；规则名称、类型和参数仍须有效。
	skipValue := allowEmpty && isZeroValue(field)
	actual := field
	for actual.Kind() == reflect.Pointer {
		if actual.IsNil() {
			if !skipValue {
				break
			}
			actual = reflect.Zero(actual.Type().Elem())
		} else {
			actual = actual.Elem()
		}
	}

	for _, rule := range rules {
		if rule == "" || rule == "omitempty" {
			continue
		}
		switch {
		case rule == "required":
			if !skipValue && isZeroValue(actual) {
				return fmt.Errorf("required configuration missing")
			}
		case strings.HasPrefix(rule, "oneof="):
			options := strings.Fields(strings.TrimPrefix(rule, "oneof="))
			if len(options) == 0 {
				return fmt.Errorf("oneof rule requires at least one option")
			}
			if skipValue {
				continue
			}
			value := fmt.Sprint(derefValue(actual))
			matched := false
			for _, option := range options {
				if value == option {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("value %q not in allowed values %v", value, options)
			}
		case strings.HasPrefix(rule, "min="):
			if err := validateMinMax(actual, "min", strings.TrimPrefix(rule, "min="), skipValue); err != nil {
				return err
			}
		case strings.HasPrefix(rule, "max="):
			if err := validateMinMax(actual, "max", strings.TrimPrefix(rule, "max="), skipValue); err != nil {
				return err
			}
		case strings.HasPrefix(rule, "startswith="):
			prefix := strings.TrimPrefix(rule, "startswith=")
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("startswith rule only supports string fields")
			}
			if !skipValue && !strings.HasPrefix(value, prefix) {
				return fmt.Errorf("value %q must start with %q", value, prefix)
			}
		case strings.HasPrefix(rule, "notcontains="):
			needle := strings.TrimPrefix(rule, "notcontains=")
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("notcontains rule only supports string fields")
			}
			if !skipValue && strings.Contains(value, needle) {
				return fmt.Errorf("value %q must not contain %q", value, needle)
			}
		case rule == "nospace":
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("nospace rule only supports string fields")
			}
			if !skipValue && strings.Contains(value, " ") {
				return fmt.Errorf("value %q must not contain spaces", value)
			}
		default:
			return fmt.Errorf("unknown validation rule %q", rule)
		}
	}
	return nil
}

func isZeroValue(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	return value.IsZero()
}

func derefValue(value reflect.Value) any {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	return value.Interface()
}

func stringValue(value reflect.Value) (string, bool) {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.String {
		return "", false
	}
	return value.String(), true
}

func validateMinMax(actual reflect.Value, ruleName, limitStr string, skipValue bool) error {
	if strings.EqualFold(limitStr, "nan") || strings.EqualFold(limitStr, "+nan") || strings.EqualFold(limitStr, "-nan") ||
		strings.EqualFold(limitStr, "inf") || strings.EqualFold(limitStr, "+inf") || strings.EqualFold(limitStr, "-inf") {
		return fmt.Errorf("invalid float limit %q: must be finite", limitStr)
	}

	switch actual.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if actual.Type() == reflect.TypeOf(time.Duration(0)) {
			// duration 支持带单位的时长和整数纳秒。
			limit, err := time.ParseDuration(limitStr)
			if err != nil {
				nanos, parseErr := strconv.ParseInt(limitStr, 10, 64)
				if parseErr != nil {
					return fmt.Errorf("invalid duration limit %q: %w", limitStr, err)
				}
				limit = time.Duration(nanos)
			}
			return validateBound(actual.Interface().(time.Duration), limit, ruleName, skipValue)
		}
		limit, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer limit %q: %w", limitStr, err)
		}
		return validateBound(actual.Int(), limit, ruleName, skipValue)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		limit, err := strconv.ParseUint(limitStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid unsigned integer limit %q: %w", limitStr, err)
		}
		return validateBound(actual.Uint(), limit, ruleName, skipValue)

	case reflect.Float32, reflect.Float64:
		limit, err := strconv.ParseFloat(limitStr, 64)
		if err != nil {
			return fmt.Errorf("invalid float limit %q: %w", limitStr, err)
		}
		if math.IsNaN(limit) || math.IsInf(limit, 0) {
			return fmt.Errorf("invalid float limit %q: must be finite", limitStr)
		}
		value := actual.Float()
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid float value %v", value)
		}
		return validateBound(value, limit, ruleName, skipValue)

	default:
		return fmt.Errorf("%s rule only supports numeric fields", ruleName)
	}
}

func validateBound[T ~int64 | ~uint64 | ~float64](value, limit T, ruleName string, skipValue bool) error {
	if skipValue {
		return nil
	}
	if ruleName == "min" && value < limit {
		return fmt.Errorf("value %v is less than minimum %v", value, limit)
	}
	if ruleName == "max" && value > limit {
		return fmt.Errorf("value %v is greater than maximum %v", value, limit)
	}
	return nil
}
