package host

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"gochen/auth"
	"gochen/errors"
)

// IDescriptorProvider 暴露资源的动作清单。
type IDescriptorProvider interface {
	Descriptor() auth.Descriptor
}

// IAllowAuthorizerProvider 由使用 auth.Allow() 的应用实现，用于启动期清单（D10）。
type IAllowAuthorizerProvider interface {
	UsesAllowAuthorizer() bool
}

// Registry 是 Host 启动期的应用清单。
//
// 注册表是实例状态而不是包级全局：测试可独立构造，多个 Host 互不影响。
// 未注册的应用不参与跨应用检查。
type Registry struct {
	mu         sync.Mutex
	apps       []IDescriptorProvider
	actions    map[string]map[string]auth.Action
	resources  []string
	allow      map[string]struct{}
	allowOrder []string
}

// NewRegistry 创建空的启动期清单。
func NewRegistry() *Registry {
	return &Registry{
		actions: make(map[string]map[string]auth.Action),
		allow:   make(map[string]struct{}),
	}
}

// Register 登记应用并执行跨应用不变量检查。
//
// 同一资源名出现在多个应用上时动作取并集；同名动作的权限码与 Kind 必须一致。
// 一次调用中的所有应用检查通过后，才提交注册状态。
func (r *Registry) Register(apps ...IDescriptorProvider) error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "host registry cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actions := make(map[string]map[string]auth.Action, len(r.actions))
	for resource, existing := range r.actions {
		actions[resource] = maps.Clone(existing)
	}
	resources := slices.Clone(r.resources)
	registeredApps := slices.Clone(r.apps)
	allow := maps.Clone(r.allow)
	allowOrder := slices.Clone(r.allowOrder)
	for _, app := range apps {
		if app == nil {
			return errors.NewCode(errors.InvalidInput, "application cannot be nil")
		}
		descriptor := app.Descriptor()
		if err := descriptor.Validate(); err != nil {
			return err
		}
		resource := strings.TrimSpace(descriptor.Resource)
		seen := actions[resource]
		if seen == nil {
			seen = make(map[string]auth.Action, len(descriptor.Actions))
			actions[resource] = seen
			resources = append(resources, resource)
		}
		for _, action := range descriptor.Actions {
			permission, ok := descriptor.PermissionFor(action.Name)
			if !ok {
				return errors.NewCode(errors.InvalidInput, "action has no permission code").
					WithContext("resource", resource).
					WithContext("action", action.Name)
			}
			existing, exists := seen[action.Name]
			if !exists {
				seen[action.Name] = action
				continue
			}
			existingPermission := actionPermission(resource, existing)
			if existingPermission != permission {
				return errors.NewCode(errors.InvalidInput,
					"same action name declared with different permission codes").
					WithContext("resource", resource).
					WithContext("action", action.Name).
					WithContext("permission", existingPermission).
					WithContext("conflicting_permission", permission)
			}
			if existing.Kind != action.Kind {
				return errors.NewCode(errors.InvalidInput,
					"same action name declared with different kinds").
					WithContext("resource", resource).
					WithContext("action", action.Name)
			}
		}
		registeredApps = append(registeredApps, app)
		if provider, ok := app.(IAllowAuthorizerProvider); ok && provider.UsesAllowAuthorizer() {
			if _, exists := allow[resource]; !exists {
				allow[resource] = struct{}{}
				allowOrder = append(allowOrder, resource)
			}
		}
	}
	r.actions = actions
	r.resources = resources
	r.apps = registeredApps
	r.allow = allow
	r.allowOrder = allowOrder
	return nil
}

func actionPermission(resource string, action auth.Action) string {
	if action.Permission != "" {
		return action.Permission
	}
	return strings.TrimSpace(resource) + ":api:" + action.Name
}

// Validate 校验已登记应用清单的跨应用不变量。
func (r *Registry) Validate() error {
	if r == nil {
		return errors.NewCode(errors.InvalidInput, "host registry cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, app := range r.apps {
		if err := app.Descriptor().Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Resources 返回按登记顺序排列的资源名。
func (r *Registry) Resources() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.resources...)
}

// Actions 返回指定资源的动作并集。
func (r *Registry) Actions(resource string) map[string]auth.Action {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	source := r.actions[strings.TrimSpace(resource)]
	out := make(map[string]auth.Action, len(source))
	for name, action := range source {
		out[name] = action
	}
	return out
}

// AllowResources 返回使用 auth.Allow() 的资源；生产环境可据此禁止放行策略。
func (r *Registry) AllowResources() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.allowOrder...)
}
