package rest

import (
	"encoding/json"
	"strings"

	"gochen/errors"
)

// rejectAuditedUpdateForbiddenFields 拒绝更新审计相关字段。
func rejectAuditedUpdateForbiddenFields(body map[string]json.RawMessage) error {
	if len(body) == 0 {
		return nil
	}
	forbidden := []string{
		"created_at",
		"created_by",
		"updated_at",
		"updated_by",
		"deleted_at",
		"deleted_by",
	}
	for actual := range body {
		for _, key := range forbidden {
			if strings.EqualFold(actual, key) {
				return errors.NewCode(errors.InvalidInput, "cannot update audit/delete managed fields via PUT").WithContext("field", actual)
			}
		}
	}
	return nil
}

// requireUint64JSONField 从 JSON 对象中提取必需的 uint64 字段。
func requireUint64JSONField(body map[string]json.RawMessage, field string) (uint64, error) {
	var raw json.RawMessage
	matched := ""
	for key, candidate := range body {
		if !strings.EqualFold(key, field) {
			continue
		}
		if matched != "" {
			return 0, errors.NewCode(errors.InvalidInput, "ambiguous field").WithContext("field", field)
		}
		matched = key
		raw = candidate
	}
	if matched == "" {
		return 0, errors.NewCode(errors.InvalidInput, "missing required field").WithContext("field", field)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return 0, errors.NewCode(errors.InvalidInput, "invalid field").WithContext("field", field)
	}
	var v uint64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, errors.NewCode(errors.InvalidInput, "invalid field").WithContext("field", field)
	}
	return v, nil
}
