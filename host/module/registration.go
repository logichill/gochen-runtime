package module

import (
	"reflect"

	"gochen-runtime/di"
)

// Lifetime 注册生命周期类型。
type Lifetime int

const (
	// SingletonLifetime 单例生命周期。
	SingletonLifetime Lifetime = iota
	// TransientLifetime 瞬态生命周期。
	TransientLifetime
)

// Registration 是模块 Init 阶段的最小 DI 注册项。
type Registration struct {
	// Lifetime 注册生命周期（单例/瞬态）。
	Lifetime Lifetime

	// ServiceType 显式 service type。
	ServiceType reflect.Type

	// Factory 工厂函数。
	// 与 Instance 互斥：两者只能设置其一。
	Factory di.Factory

	// Instance 直接实例（跳过构造器）。
	// 与 Factory 互斥：两者只能设置其一。
	Instance interface{}
}
