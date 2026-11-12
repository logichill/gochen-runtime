package module

import "gochen/eventing/projection"

// ProjectionRegistration 是启动期投影声明；零值非法。管理器在注册时验证 ID 类型。
// 对象由调用者构造，模块仅持有引用并负责释放自身取得的注册 token。
type ProjectionRegistration struct{ instance any }

// ProjectionOf 创建类型安全的投影声明，不启动或注册投影。
func ProjectionOf[ID comparable](instance projection.IProjection[ID]) ProjectionRegistration {
	return ProjectionRegistration{instance: instance}
}

// Instance 返回用于宿主注册的原始投影对象；业务调用方不应依赖类型擦除。
func (r ProjectionRegistration) Instance() any { return r.instance }
