package capability

import (
	"strconv"

	"gochen-runtime/host/internal/runtimeutil"
	"gochen/errors"
	"gochen/messaging"
)

// EventHandlerRegistration 保存实际订阅使用的处理器及冻结的消息类型。
type EventHandlerRegistration struct {
	Key         string
	MessageType string
	Handler     messaging.IMessageHandler
}

// IEventHandlerRegistrationProvider 在订阅前提供独立注册源，并参与门禁启用。
type IEventHandlerRegistrationProvider interface {
	RuntimeEventHandlers() ([]EventHandlerRegistration, error)
	SealEventHandlers() error
	ActivateEventHandlers() error
}

func (m *ModuleRuntime) eventHandler(index int) (messaging.IMessageHandler, []string, error) {
	item := &m.eventHandlers[index]
	if item.handler != nil {
		return item.handler, append([]string(nil), item.types...), nil
	}
	inst, err := item.resolve()
	if err != nil {
		return nil, nil, runtimeutil.WrapModuleErr(m.moduleID, err, "resolve event handler", errors.Internal).WithContext("index", item.index)
	}
	handler, ok := inst.(messaging.IMessageHandler)
	if !ok || runtimeutil.IsTypedNil(handler) {
		return nil, nil, errors.NewCode(errors.Internal, "event handler does not implement IMessageHandler").WithContext("module", m.moduleID).WithContext("index", item.index)
	}
	types := getHandlerMessageTypes(handler)
	if len(types) == 0 {
		return nil, nil, errors.NewCode(errors.InvalidInput, "event handler resolved no valid message types").WithContext("module", m.moduleID).WithContext("index", item.index)
	}
	item.handler, item.types = handler, append([]string(nil), types...)
	return handler, types, nil
}

// RuntimeEventHandlers 固定实际实例和订阅类型；取得清单后 Start 要求门禁启用。
func (m *ModuleRuntime) RuntimeEventHandlers() ([]EventHandlerRegistration, error) {
	if m.eventsManaged && len(m.eventHandlers) != m.eventsCount {
		return nil, errors.NewCode(errors.FailedPrecondition, "event registration changed after entry collection").WithContext("module", m.moduleID)
	}
	var out []EventHandlerRegistration
	for i, item := range m.eventHandlers {
		handler, types, err := m.eventHandler(i)
		if err != nil {
			return nil, err
		}
		for _, messageType := range types {
			out = append(out, EventHandlerRegistration{Key: m.moduleID + ":event:" + strconv.Itoa(item.index) + ":" + messageType, MessageType: messageType, Handler: handler})
		}
	}
	m.eventsManaged, m.eventsCount = true, len(m.eventHandlers)
	return out, nil
}

// SealEventHandlers 封存已取得的实际订阅集合，新增注册要求重新构造模块。
func (m *ModuleRuntime) SealEventHandlers() error {
	if !m.eventsManaged || len(m.eventHandlers) != m.eventsCount {
		return errors.NewCode(errors.FailedPrecondition, "event entries must be collected before sealing").WithContext("module", m.moduleID)
	}
	m.eventsSealed = true
	return nil
}

// ActivateEventHandlers 在全部门禁通过后允许同一集合开始订阅。
func (m *ModuleRuntime) ActivateEventHandlers() error {
	if !m.eventsSealed || len(m.eventHandlers) != m.eventsCount {
		return errors.NewCode(errors.FailedPrecondition, "event entries must be sealed before activation").WithContext("module", m.moduleID)
	}
	m.eventsActivated = true
	return nil
}
