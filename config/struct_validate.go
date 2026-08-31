package config

import (
	"fmt"
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
	for _, rule := range rules {
		if strings.TrimSpace(rule) == "omitempty" {
			allowEmpty = true
			break
		}
	}

	actual := field
	for actual.Kind() == reflect.Pointer {
		if actual.IsNil() {
			if allowEmpty {
				return nil
			}
			break
		}
		actual = actual.Elem()
	}

	if allowEmpty && isZeroValue(actual) {
		return nil
	}

	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" || rule == "omitempty" {
			continue
		}
		switch {
		case rule == "required":
			if isZeroValue(actual) {
				return fmt.Errorf("required configuration missing")
			}
		case strings.HasPrefix(rule, "oneof="):
			options := strings.Fields(strings.TrimPrefix(rule, "oneof="))
			if len(options) == 0 {
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
			limit, err := strconv.ParseFloat(strings.TrimPrefix(rule, "min="), 64)
			if err != nil {
				return err
			}
			value, ok := numericValue(actual)
			if !ok {
				return fmt.Errorf("min rule only supports numeric fields")
			}
			if value < limit {
				return fmt.Errorf("value %v is less than minimum %v", derefValue(actual), trimFloat(limit))
			}
		case strings.HasPrefix(rule, "max="):
			limit, err := strconv.ParseFloat(strings.TrimPrefix(rule, "max="), 64)
			if err != nil {
				return err
			}
			value, ok := numericValue(actual)
			if !ok {
				return fmt.Errorf("max rule only supports numeric fields")
			}
			if value > limit {
				return fmt.Errorf("value %v is greater than maximum %v", derefValue(actual), trimFloat(limit))
			}
		case strings.HasPrefix(rule, "startswith="):
			prefix := strings.TrimPrefix(rule, "startswith=")
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("startswith rule only supports string fields")
			}
			if !strings.HasPrefix(value, prefix) {
				return fmt.Errorf("value %q must start with %q", value, prefix)
			}
		case strings.HasPrefix(rule, "notcontains="):
			needle := strings.TrimPrefix(rule, "notcontains=")
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("notcontains rule only supports string fields")
			}
			if strings.Contains(value, needle) {
				return fmt.Errorf("value %q must not contain %q", value, needle)
			}
		case rule == "nospace":
			value, ok := stringValue(actual)
			if !ok {
				return fmt.Errorf("nospace rule only supports string fields")
			}
			if strings.Contains(value, " ") {
				return fmt.Errorf("value %q must not contain spaces", value)
			}
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

func numericValue(value reflect.Value) (float64, bool) {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Type() == reflect.TypeOf(time.Duration(0)) {
			return float64(value.Interface().(time.Duration)), true
		}
		return float64(value.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(value.Uint()), true
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
	default:
		return 0, false
	}
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

func trimFloat(value float64) any {
	if float64(int64(value)) == value {
		return int64(value)
	}
	return value
}
