package config

import (
	"reflect"
	"strconv"
	"strings"
	"time"
)

const defaultAllocateStruct = "{}"

func applyDefaultsRecursive(value reflect.Value) {
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
		if !isConfigStructField(fieldType) {
			continue
		}

		defaultTag := strings.TrimSpace(fieldType.Tag.Get("default"))
		if defaultTag == "-" {
			continue
		}

		if isStructPointerField(fieldValue) {
			if fieldValue.IsNil() && defaultTag == defaultAllocateStruct && fieldValue.CanSet() {
				fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
			}
			if !fieldValue.IsNil() {
				applyDefaultsRecursive(fieldValue)
			}
			continue
		}

		if isStructLikeField(fieldValue) {
			applyDefaultsRecursive(fieldValue)
			continue
		}

		if defaultTag == "" {
			continue
		}
		assignDefaultValue(fieldValue, defaultTag)
	}
}

func isStructPointerField(value reflect.Value) bool {
	return value.Kind() == reflect.Pointer && value.Type().Elem().Kind() == reflect.Struct
}

func assignDefaultValue(field reflect.Value, raw string) {
	if !field.CanSet() {
		return
	}

	if field.Kind() == reflect.Pointer {
		assignPointerDefaultValue(field, raw)
		return
	}
	if !isZeroValue(field) {
		return
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		if parsed, err := strconv.ParseBool(raw); err == nil {
			field.SetBool(parsed)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Type() == reflect.TypeOf(time.Duration(0)) {
			if parsed, err := time.ParseDuration(raw); err == nil {
				field.SetInt(int64(parsed))
			}
			return
		}
		if parsed, err := strconv.ParseInt(raw, 10, field.Type().Bits()); err == nil {
			field.SetInt(parsed)
		}
	case reflect.Float32, reflect.Float64:
		if parsed, err := strconv.ParseFloat(raw, field.Type().Bits()); err == nil {
			if !isFiniteFloat(parsed) {
				return
			}
			field.SetFloat(parsed)
		}
	case reflect.Slice:
		assignDefaultSliceValue(field, raw)
	}
}

func assignPointerDefaultValue(field reflect.Value, raw string) {
	if !field.IsNil() {
		return
	}

	elemType := field.Type().Elem()
	value := reflect.New(elemType)
	switch elemType.Kind() {
	case reflect.String:
		value.Elem().SetString(raw)
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return
		}
		value.Elem().SetBool(parsed)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if elemType == reflect.TypeOf(time.Duration(0)) {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return
			}
			value.Elem().SetInt(int64(parsed))
		} else {
			parsed, err := strconv.ParseInt(raw, 10, elemType.Bits())
			if err != nil {
				return
			}
			value.Elem().SetInt(parsed)
		}
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(raw, elemType.Bits())
		if err != nil || !isFiniteFloat(parsed) {
			return
		}
		value.Elem().SetFloat(parsed)
	default:
		return
	}
	field.Set(value)
}

func assignDefaultSliceValue(field reflect.Value, raw string) {
	if raw == "[]" {
		field.Set(reflect.MakeSlice(field.Type(), 0, 0))
		return
	}
	switch field.Type().Elem().Kind() {
	case reflect.String:
		field.Set(stringSliceValueForType(splitStringSlice(raw), field.Type()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		assignIntSliceValue(field, raw)
	case reflect.Float32, reflect.Float64:
		assignFloatSliceValue(field, raw)
	}
}
