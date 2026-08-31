package repo

import (
	"math"
	"reflect"
	"strconv"
	"strings"

	"gochen/errors"
)

func readStringField(value reflect.Value, index []int) (string, bool) {
	field, ok := resolveField(value, index)
	if !ok {
		return "", false
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			return "", false
		}
		field = field.Elem()
	}
	if field.Kind() != reflect.String {
		return "", false
	}
	return field.String(), true
}

func setStringField(value reflect.Value, index []int, target string) bool {
	field, ok := resolveField(value, index)
	if !ok {
		return false
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		field = field.Elem()
	}
	if field.Kind() != reflect.String || !field.CanSet() {
		return false
	}
	field.SetString(strings.TrimSpace(target))
	return true
}

func readInt64Field(value reflect.Value, index []int) (int64, bool) {
	field, ok := resolveField(value, index)
	if !ok {
		return 0, false
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			return 0, false
		}
		field = field.Elem()
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return normalizePositiveID(field.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if field.Uint() > uint64(math.MaxInt64) {
			return 0, false
		}
		return normalizePositiveID(int64(field.Uint())), true
	case reflect.String:
		parsed, err := strconv.ParseInt(strings.TrimSpace(field.String()), 10, 64)
		if err != nil {
			return 0, false
		}
		return normalizePositiveID(parsed), true
	default:
		return 0, false
	}
}

func setInt64Field(value reflect.Value, index []int, target int64) bool {
	field, ok := resolveField(value, index)
	if !ok {
		return false
	}
	for field.Kind() == reflect.Ptr {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		field = field.Elem()
	}
	if !field.CanSet() {
		return false
	}
	target = normalizePositiveID(target)
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		field.SetInt(target)
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if target < 0 {
			return false
		}
		field.SetUint(uint64(target))
		return true
	case reflect.String:
		field.SetString(strconv.FormatInt(target, 10))
		return true
	default:
		return false
	}
}

func assignStringConstraintField(value reflect.Value, field dataScopeField, target, label string) error {
	target = strings.TrimSpace(target)
	if target == "" || field.column == "" {
		return nil
	}
	if !field.hasField() {
		return errors.NewCode(errors.InvalidInput, "configured authz column has no matching string field on entity").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	if !setStringField(value, field.index, target) {
		return errors.NewCode(errors.InvalidInput, "configured authz field is not writable string").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	return nil
}

func assignInt64ConstraintField(value reflect.Value, field dataScopeField, target int64, label string) error {
	target = normalizePositiveID(target)
	if target == 0 || field.column == "" {
		return nil
	}
	if !field.hasField() {
		return errors.NewCode(errors.InvalidInput, "configured authz column has no matching integer field on entity").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	if !setInt64Field(value, field.index, target) {
		return errors.NewCode(errors.InvalidInput, "configured authz field is not writable integer").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	return nil
}

func readRequiredStringConstraintField(value reflect.Value, field dataScopeField, label string) (string, error) {
	if field.column == "" {
		return "", errors.NewCode(errors.InvalidInput, "configured authz column is required").
			WithContext("label", label)
	}
	if !field.hasField() {
		return "", errors.NewCode(errors.InvalidInput, "configured authz column has no matching string field on entity").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	current, ok := readStringField(value, field.index)
	if !ok {
		return "", errors.NewCode(errors.InvalidInput, "configured authz field is not readable string").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	return current, nil
}

func readRequiredInt64ConstraintField(value reflect.Value, field dataScopeField, label string) (int64, error) {
	if field.column == "" {
		return 0, errors.NewCode(errors.InvalidInput, "configured authz column is required").
			WithContext("label", label)
	}
	if !field.hasField() {
		return 0, errors.NewCode(errors.InvalidInput, "configured authz column has no matching integer field on entity").
			WithContext("label", label).
			WithContext("column", field.column)
	}
	current, ok := readInt64Field(value, field.index)
	if !ok || current == 0 {
		return 0, errors.NewCode(errors.InvalidInput, label+" is required under the current data scope")
	}
	return current, nil
}

func resolveField(value reflect.Value, index []int) (reflect.Value, bool) {
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			if !value.CanSet() {
				return reflect.Value{}, false
			}
			value.Set(reflect.New(value.Type().Elem()))
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	field := value
	for _, idx := range index {
		for field.Kind() == reflect.Ptr {
			if field.IsNil() {
				if !field.CanSet() {
					return reflect.Value{}, false
				}
				field.Set(reflect.New(field.Type().Elem()))
			}
			field = field.Elem()
		}
		if field.Kind() != reflect.Struct || idx >= field.NumField() {
			return reflect.Value{}, false
		}
		field = field.Field(idx)
	}
	return field, true
}
