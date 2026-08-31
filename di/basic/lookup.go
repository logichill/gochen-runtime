package basic

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"

	"gochen-runtime/di"
	"gochen/errors"
)

// Resolve 按类型解析依赖。
func (c *Container) Resolve(serviceType reflect.Type) (any, error) {
	return c.resolve(serviceType, &resolutionState{})
}

func (c *Container) resolve(serviceType reflect.Type, state *resolutionState) (any, error) {
	if serviceType == nil {
		return nil, errors.NewCode(errors.InvalidInput, "service type cannot be nil")
	}

	c.mutex.RLock()
	key, candidates, ok := c.findRegisteredServiceKeyLocked(serviceType)
	var entry *serviceEntry
	if ok {
		entry, ok = c.findServiceEntryByKeyLocked(key)
	}
	c.mutex.RUnlock()
	if ok {
		return c.resolveEntry(key, entry, state)
	}
	if len(candidates) > 1 {
		return nil, errors.NewCode(errors.Conflict, "multiple services match service type").
			WithContext("service_type", di.TypeKey(serviceType)).
			WithContext("matches", strings.Join(candidates, ", "))
	}

	return nil, errors.NewCode(errors.NotFound, "service not registered").WithContext("service_type", di.TypeKey(serviceType))
}

// IsRegistered 判断某个类型是否已注册。
func (c *Container) IsRegistered(serviceType reflect.Type) bool {
	if serviceType == nil {
		return false
	}

	c.mutex.RLock()
	defer c.mutex.RUnlock()
	_, _, ok := c.findRegisteredServiceKeyLocked(serviceType)
	return ok
}

func (c *Container) ListRegisteredTypes() map[string]reflect.Type {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	out := make(map[string]reflect.Type, len(c.typedServices))
	for serviceType, entry := range c.typedServices {
		if resolvedType := serviceEntryOutputType(entry); resolvedType != nil {
			out[di.TypeKey(serviceType)] = resolvedType
		}
	}
	return out
}

func serviceEntryOutputType(entry *serviceEntry) reflect.Type {
	if entry == nil {
		return nil
	}
	if entry.serviceType != nil {
		return entry.serviceType
	}
	return serviceOutputType(entry.factory)
}

func (c *Container) resolveEntry(serviceLabel string, entry *serviceEntry, state *resolutionState) (any, error) {
	if entry == nil {
		return nil, errors.NewCode(errors.NotFound, "service not registered").WithContext("service", serviceLabel)
	}

	return c.withResolutionFrame(state, serviceLabel, func() (result any, resultErr error) {
		if entry.lifetime == lifetimeTransient {
			inst, err := c.createInstance(entry.factory, state)
			if err != nil {
				return nil, wrapResolveEntryError(err, serviceLabel)
			}
			return inst, nil
		}

		entry.mu.Lock()
		if entry.created {
			inst := entry.instance
			err := entry.createErr
			entry.mu.Unlock()
			if err != nil {
				return nil, wrapResolveEntryError(err, serviceLabel)
			}
			return inst, nil
		}
		if entry.creating {
			endWait, err := c.beginResolutionWait(state, entry.creator, serviceLabel)
			if err != nil {
				entry.mu.Unlock()
				return nil, err
			}
			for !entry.created {
				entry.cond.Wait()
			}
			endWait()
			inst := entry.instance
			err = entry.createErr
			entry.mu.Unlock()
			if err != nil {
				return nil, wrapResolveEntryError(err, serviceLabel)
			}
			return inst, nil
		}
		entry.creating = true
		entry.creator = state
		entry.mu.Unlock()

		creationSettled := false
		defer func() {
			if creationSettled {
				return
			}
			if r := recover(); r != nil {
				resultErr = errors.NewCode(errors.Internal, "factory panicked").
					WithContext("service", serviceLabel).
					WithContext("panic", fmt.Sprint(r)).
					WithContext("stack", string(debug.Stack()))
			}
			entry.mu.Lock()
			if !entry.created {
				entry.instance = nil
				entry.createErr = resultErr
				entry.created = true
			}
			entry.creating = false
			entry.creator = nil
			entry.cond.Broadcast()
			entry.mu.Unlock()
		}()

		inst, err := c.createInstance(entry.factory, state)

		entry.mu.Lock()
		entry.instance = inst
		entry.createErr = err
		entry.created = true
		entry.creating = false
		entry.creator = nil
		entry.cond.Broadcast()
		entry.mu.Unlock()
		creationSettled = true

		if err != nil {
			return nil, wrapResolveEntryError(err, serviceLabel)
		}
		return inst, nil
	})
}
