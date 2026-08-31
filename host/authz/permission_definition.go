package authz

import (
	"sort"
	"strings"

	"gochen-runtime/internal/normalize"
)

// PermissionDefinition 定义模块级权限目录元数据。
type PermissionDefinition struct {
	Code        string   `json:"code"`
	Type        string   `json:"type,omitempty"`
	Resource    string   `json:"resource,omitempty"`
	Action      string   `json:"action,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	BuiltinOnly bool     `json:"builtin_only,omitempty"`
	RiskLevel   string   `json:"risk_level,omitempty"`
}

// PermissionDefinitionsFromCodes 将权限码列表转换为最小 definition 集合。
func PermissionDefinitionsFromCodes(codes ...string) []PermissionDefinition {
	if len(codes) == 0 {
		return nil
	}
	definitions := make([]PermissionDefinition, 0, len(codes))
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		definitions = append(definitions, PermissionDefinition{Code: code})
	}
	return normalizePermissionDefinitions(definitions)
}

// PermissionCodes 返回 definition 集合中的权限码。
func PermissionCodes(definitions ...PermissionDefinition) []string {
	if len(definitions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(definitions))
	out := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		code := strings.TrimSpace(definition.Code)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizePermissionDefinitions(definitions []PermissionDefinition) []PermissionDefinition {
	if len(definitions) == 0 {
		return nil
	}
	merged := make(map[string]PermissionDefinition, len(definitions))
	order := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		definition = normalizePermissionDefinition(definition)
		if definition.Code == "" {
			continue
		}
		current, ok := merged[definition.Code]
		if !ok {
			order = append(order, definition.Code)
			merged[definition.Code] = definition
			continue
		}
		merged[definition.Code] = mergePermissionDefinition(current, definition)
	}
	if len(order) == 0 {
		return nil
	}
	sort.Strings(order)
	out := make([]PermissionDefinition, 0, len(order))
	for _, code := range order {
		out = append(out, merged[code])
	}
	return out
}

func normalizePermissionDefinition(definition PermissionDefinition) PermissionDefinition {
	definition.Code = strings.TrimSpace(definition.Code)
	definition.Type = strings.TrimSpace(definition.Type)
	definition.Resource = strings.TrimSpace(definition.Resource)
	definition.Action = strings.TrimSpace(definition.Action)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.RiskLevel = strings.TrimSpace(definition.RiskLevel)
	if len(definition.Scopes) > 0 {
		scopes := normalize.Strings(definition.Scopes)
		sort.Strings(scopes)
		definition.Scopes = scopes
	}
	return definition
}

func mergePermissionDefinition(current PermissionDefinition, incoming PermissionDefinition) PermissionDefinition {
	if current.Code == "" {
		return incoming
	}
	if incoming.Type != "" {
		current.Type = incoming.Type
	}
	if incoming.Resource != "" {
		current.Resource = incoming.Resource
	}
	if incoming.Action != "" {
		current.Action = incoming.Action
	}
	if incoming.Name != "" {
		current.Name = incoming.Name
	}
	if incoming.Description != "" {
		current.Description = incoming.Description
	}
	if len(incoming.Scopes) > 0 {
		current.Scopes = append([]string(nil), incoming.Scopes...)
	}
	if incoming.BuiltinOnly {
		current.BuiltinOnly = true
	}
	if incoming.RiskLevel != "" {
		current.RiskLevel = incoming.RiskLevel
	}
	return current
}

// MergePermissionDefinitions 合并权限 definition 集合。
func MergePermissionDefinitions(current []PermissionDefinition, incoming []PermissionDefinition) []PermissionDefinition {
	if len(current) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make([]PermissionDefinition, 0, len(current)+len(incoming))
	out = append(out, current...)
	out = append(out, incoming...)
	return normalizePermissionDefinitions(out)
}
