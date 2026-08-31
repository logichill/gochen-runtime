package basic

import (
	"reflect"
	"sync"
)

// Container 提供最基础的依赖注册、解析与调用能力。
type Container struct {
	typedServices map[reflect.Type]*serviceEntry
	mutex         sync.RWMutex

	// resolutionWaits 记录并发解析链之间的等待关系，用于在 cond.Wait 前识别跨 goroutine 环。
	resolutionWaitMu sync.Mutex
	resolutionWaits  map[*resolutionState]resolutionWait
}

type serviceLifetime uint8

const (
	lifetimeSingleton serviceLifetime = iota
	lifetimeTransient
)

type serviceEntry struct {
	serviceType reflect.Type
	factory     any
	lifetime    serviceLifetime
	created     bool
	creating    bool
	instance    any
	createErr   error
	creator     *resolutionState

	mu   sync.Mutex
	cond *sync.Cond
}

// newServiceEntry 创建服务Entry。
func newServiceEntry(serviceType reflect.Type, factory any, lifetime serviceLifetime) *serviceEntry {
	e := &serviceEntry{
		serviceType: serviceType,
		factory:     factory,
		lifetime:    lifetime,
	}
	e.cond = sync.NewCond(&e.mu)
	return e
}

// New 创建一个空的基础容器。
func New() *Container {
	return &Container{
		typedServices:   make(map[reflect.Type]*serviceEntry),
		resolutionWaits: make(map[*resolutionState]resolutionWait),
	}
}

// Clear 清空容器中的全部注册项。
func (c *Container) Clear() {
	c.mutex.Lock()
	c.typedServices = make(map[reflect.Type]*serviceEntry)
	c.mutex.Unlock()

	c.resolutionWaitMu.Lock()
	c.resolutionWaits = make(map[*resolutionState]resolutionWait)
	c.resolutionWaitMu.Unlock()
}
