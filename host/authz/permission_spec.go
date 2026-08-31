package authz

import (
	"fmt"
	"strings"

	"gochen/auth/action"
)

// PermissionType 描述权限定义所属的分类，例如 API、菜单或动作权限。
type PermissionType string

// PermissionAction 描述权限允许执行的动作，例如 read、write 或 delete。
type PermissionAction string

// PermissionScope 描述权限生效的作用域层级，例如 platform 或 tenant。
type PermissionScope string

// PermissionRisk 描述权限操作的风险等级，用于审计或审批场景分级。
type PermissionRisk string

const (
	PermissionTypeAPI    PermissionType = "api"
	PermissionTypeMenu   PermissionType = "menu"
	PermissionTypeAction PermissionType = "action"

	PermissionActionAny      PermissionAction = "*"
	PermissionActionRead     PermissionAction = "read"
	PermissionActionList     PermissionAction = "list"
	PermissionActionWrite    PermissionAction = "write"
	PermissionActionDelete   PermissionAction = "delete"
	PermissionActionPublish  PermissionAction = "publish"
	PermissionActionActivate PermissionAction = "activate"
	PermissionActionManage   PermissionAction = "manage"
	PermissionActionAdmin    PermissionAction = "admin"
	PermissionActionView     PermissionAction = "view"
	PermissionActionInvoke   PermissionAction = "invoke"
	PermissionActionSelfRead PermissionAction = "read_self"
	PermissionActionSelfEdit PermissionAction = "update_self"

	PermissionScopePlatform PermissionScope = "platform"
	PermissionScopeTenant   PermissionScope = "tenant"

	PermissionRiskLow      PermissionRisk = "low"
	PermissionRiskMedium   PermissionRisk = "medium"
	PermissionRiskHigh     PermissionRisk = "high"
	PermissionRiskCritical PermissionRisk = "critical"
)

// PermissionSpec 描述一条标准化权限定义。
//
// 它既可作为代码里的权限声明，也可进一步转换为对外暴露的
// `PermissionDefinition`，供菜单、路由或鉴权注册表消费。
type PermissionSpec struct {
	Code        string
	Type        PermissionType
	Resource    string
	Action      PermissionAction
	Name        string
	Description string
	Scopes      []PermissionScope
	BuiltinOnly bool
	RiskLevel   string
}

// PermissionSet 表示同一资源上一组按动作组织的权限定义。
type PermissionSet struct {
	specs    []PermissionSpec
	byAction map[PermissionAction]PermissionSpec
}

// NewPermissionSet 基于给定权限定义构造权限集合，并按动作建立索引。
func NewPermissionSet(specs ...PermissionSpec) PermissionSet {
	specs = JoinPermissionSpecs(specs)
	set := PermissionSet{
		specs:    append([]PermissionSpec(nil), specs...),
		byAction: make(map[PermissionAction]PermissionSpec, len(specs)),
	}
	for _, spec := range specs {
		action := PermissionAction(strings.TrimSpace(string(spec.Action)))
		if action == "" {
			continue
		}
		set.byAction[action] = spec
	}
	return set
}

// NewAPIPermissionSet 为给定资源构造一组 API 权限。
func NewAPIPermissionSet(resource string, actions ...PermissionAction) PermissionSet {
	return NewPermissionSet(APIPermissions(resource, actions...)...)
}

// NewMenuPermissionSet 为给定资源构造一组菜单权限。
func NewMenuPermissionSet(resource string, actions ...PermissionAction) PermissionSet {
	return NewPermissionSet(MenuPermissions(resource, actions...)...)
}

// NewActionPermissionSet 为给定资源构造一组操作权限。
func NewActionPermissionSet(resource string, actions ...PermissionAction) PermissionSet {
	return NewPermissionSet(ActionPermissions(resource, actions...)...)
}

// APIPermissions 生成给定资源的 API 权限定义列表。
func APIPermissions(resource string, actions ...PermissionAction) []PermissionSpec {
	return permissionSpecs(PermissionTypeAPI, resource, actions...)
}

// MenuPermissions 生成给定资源的菜单权限定义列表。
func MenuPermissions(resource string, actions ...PermissionAction) []PermissionSpec {
	return permissionSpecs(PermissionTypeMenu, resource, actions...)
}

// ActionPermissions 生成给定资源的操作权限定义列表。
func ActionPermissions(resource string, actions ...PermissionAction) []PermissionSpec {
	return permissionSpecs(PermissionTypeAction, resource, actions...)
}

// APIPermission 生成单条 API 权限定义。
func APIPermission(resource string, action PermissionAction) PermissionSpec {
	return permissionSpec(PermissionTypeAPI, resource, action)
}

// MenuPermission 生成单条菜单权限定义。
func MenuPermission(resource string, action PermissionAction) PermissionSpec {
	return permissionSpec(PermissionTypeMenu, resource, action)
}

// ActionPermission 生成单条操作权限定义。
func ActionPermission(resource string, action PermissionAction) PermissionSpec {
	return permissionSpec(PermissionTypeAction, resource, action)
}

// PermissionCode 把权限编码解析为结构化权限定义。
//
// 非法编码不会报错，而是返回仅带原始 Code 的零值定义，
// 便于调用方自行决定是否继续校验或拒绝。
func PermissionCode(code string) PermissionSpec {
	code = strings.ToLower(strings.TrimSpace(code))
	spec := PermissionSpec{Code: code}
	if !action.IsValidCode(code) {
		return spec
	}
	segments := strings.Split(code, ":")
	spec.Resource = segments[0]
	spec.Type = PermissionType(segments[1])
	spec.Action = PermissionAction(segments[2])
	return spec
}

func (s PermissionSet) Specs() []PermissionSpec {
	return append([]PermissionSpec(nil), s.specs...)
}

func (s PermissionSet) Codes() []string {
	return PermissionCodesFromSpecs(s.specs...)
}

func (s PermissionSet) Definitions() []PermissionDefinition {
	return PermissionDefinitions(s.specs...)
}

// Find 按动作查找权限定义。
func (s PermissionSet) Find(action PermissionAction) (PermissionSpec, bool) {
	action = PermissionAction(strings.TrimSpace(string(action)))
	if action == "" {
		return PermissionSpec{}, false
	}
	spec, ok := s.byAction[action]
	return spec, ok
}

// Must 返回指定动作的权限定义；不存在时返回零值。
func (s PermissionSet) Must(action PermissionAction) PermissionSpec {
	spec, ok := s.Find(action)
	if !ok {
		return PermissionSpec{}
	}
	return spec
}

func (s PermissionSet) Code(action PermissionAction) string {
	return s.Must(action).Code
}

// JoinActions 合并多组动作并去重、规范化空白。
func JoinActions(groups ...[]PermissionAction) []PermissionAction {
	if len(groups) == 0 {
		return nil
	}
	actions := make([]PermissionAction, 0)
	for _, group := range groups {
		actions = append(actions, group...)
	}
	return normalizeActions(actions)
}

func ReadWriteDeleteActions() []PermissionAction {
	return []PermissionAction{PermissionActionRead, PermissionActionWrite, PermissionActionDelete}
}

func ReadWriteActions() []PermissionAction {
	return []PermissionAction{PermissionActionRead, PermissionActionWrite}
}

func ManageActions() []PermissionAction {
	return []PermissionAction{PermissionActionManage, PermissionActionRead, PermissionActionWrite, PermissionActionDelete}
}

func SelfActions() []PermissionAction {
	return []PermissionAction{PermissionActionSelfRead, PermissionActionSelfEdit}
}

// JoinPermissionSpecs 合并多组权限定义，并按 Code 去重。
func JoinPermissionSpecs(groups ...[]PermissionSpec) []PermissionSpec {
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]PermissionSpec, 0)
	for _, group := range groups {
		for _, spec := range group {
			code := strings.TrimSpace(spec.Code)
			if code == "" {
				continue
			}
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			out = append(out, spec)
		}
	}
	return out
}

// PermissionByAction 从定义列表中按动作查找权限。
func PermissionByAction(specs []PermissionSpec, action PermissionAction) PermissionSpec {
	action = PermissionAction(strings.TrimSpace(string(action)))
	for _, spec := range specs {
		if spec.Action == action {
			return spec
		}
	}
	return PermissionSpec{}
}

// PermissionCodesFromSpecs 提取并去重权限编码列表。
func PermissionCodesFromSpecs(specs ...PermissionSpec) []string {
	if len(specs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(specs))
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		code := strings.ToLower(strings.TrimSpace(spec.Code))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

// PermissionDefinitions 把权限规格转换为标准权限定义列表。
func PermissionDefinitions(specs ...PermissionSpec) []PermissionDefinition {
	if len(specs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(specs))
	out := make([]PermissionDefinition, 0, len(specs))
	for _, spec := range specs {
		code := strings.ToLower(strings.TrimSpace(spec.Code))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, spec.Definition())
	}
	return out
}

func permissionSpecs(permissionType PermissionType, resource string, actions ...PermissionAction) []PermissionSpec {
	actions = normalizeActions(actions)
	if len(actions) == 0 {
		return nil
	}
	specs := make([]PermissionSpec, 0, len(actions))
	for _, action := range actions {
		specs = append(specs, permissionSpec(permissionType, resource, action))
	}
	return specs
}

func permissionSpec(permissionType PermissionType, resource string, action PermissionAction) PermissionSpec {
	spec := PermissionSpec{
		Type:     permissionType,
		Resource: strings.ToLower(strings.TrimSpace(resource)),
		Action:   action,
	}
	if spec.Resource != "" && spec.Action != "" {
		spec.Code = fmt.Sprintf("%s:%s:%s", spec.Resource, permissionType, action)
	}
	return spec
}

func normalizeActions(actions []PermissionAction) []PermissionAction {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[PermissionAction]struct{}, len(actions))
	out := make([]PermissionAction, 0, len(actions))
	for _, action := range actions {
		action = PermissionAction(strings.TrimSpace(string(action)))
		if action == "" {
			continue
		}
		if _, ok := seen[action]; ok {
			continue
		}
		seen[action] = struct{}{}
		out = append(out, action)
	}
	return out
}

// Desc 设置权限描述。
func (p PermissionSpec) Desc(description string) PermissionSpec {
	p.Description = strings.TrimSpace(description)
	return p
}

// Label 设置权限显示名称。
func (p PermissionSpec) Label(name string) PermissionSpec {
	p.Name = strings.TrimSpace(name)
	return p
}

// Scope 设置权限允许的作用域列表，并自动去重。
func (p PermissionSpec) Scope(scopes ...PermissionScope) PermissionSpec {
	if len(scopes) == 0 {
		p.Scopes = nil
		return p
	}
	seen := make(map[PermissionScope]struct{}, len(scopes))
	p.Scopes = p.Scopes[:0]
	for _, scope := range scopes {
		scope = PermissionScope(strings.TrimSpace(string(scope)))
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		p.Scopes = append(p.Scopes, scope)
	}
	return p
}

// Builtin 标记该权限仅允许框架内置定义使用。
func (p PermissionSpec) Builtin() PermissionSpec {
	p.BuiltinOnly = true
	return p
}

// Risk 设置权限操作对应的风险等级。
func (p PermissionSpec) Risk(level PermissionRisk) PermissionSpec {
	p.RiskLevel = strings.TrimSpace(string(level))
	return p
}

// Definition 把权限规格转换为对外暴露的标准定义结构。
func (p PermissionSpec) Definition() PermissionDefinition {
	def := PermissionDefinition{
		Code:        strings.ToLower(strings.TrimSpace(p.Code)),
		Type:        string(p.Type),
		Resource:    strings.ToLower(strings.TrimSpace(p.Resource)),
		Action:      strings.ToLower(strings.TrimSpace(string(p.Action))),
		Name:        strings.TrimSpace(p.Name),
		Description: strings.TrimSpace(p.Description),
		BuiltinOnly: p.BuiltinOnly,
		RiskLevel:   strings.TrimSpace(p.RiskLevel),
	}
	if len(p.Scopes) > 0 {
		def.Scopes = make([]string, 0, len(p.Scopes))
		for _, scope := range p.Scopes {
			normalized := strings.TrimSpace(string(scope))
			if normalized == "" {
				continue
			}
			def.Scopes = append(def.Scopes, normalized)
		}
	}
	return def
}
