package authz

import (
	"gochen-runtime/host/capability"
	"gochen/auth"
)

type eventEntries struct {
	owner  capability.IEventHandlerRegistrationProvider
	actual []capability.EventHandlerRegistration
}

func (s *eventEntries) Entries() []auth.Entry {
	out := make([]auth.Entry, 0, len(s.actual))
	for _, registration := range s.actual {
		out = append(out, auth.Entry{Kind: "event", Key: registration.Key})
	}
	return out
}
func (s *eventEntries) SealEntries() error     { return s.owner.SealEventHandlers() }
func (s *eventEntries) ActivateEntries() error { return s.owner.ActivateEventHandlers() }

func collectEventEntries(provider capability.IEventHandlerRegistrationProvider, moduleID string) (*eventEntries, []auth.EntryBinding, error) {
	actual, err := provider.RuntimeEventHandlers()
	if err != nil {
		return nil, nil, err
	}
	source := &eventEntries{owner: provider, actual: actual}
	var bindings []auth.EntryBinding
	for _, registration := range actual {
		provider, ok := registration.Handler.(interface {
			AuthorizationBinding(string) (auth.EntryBinding, error)
		})
		if !ok {
			// 缺绑定保留为独立实际入口，由统一门禁产生第三类诊断。
			continue
		}
		binding, err := provider.AuthorizationBinding(registration.MessageType)
		if err != nil {
			return nil, nil, err
		}
		binding = binding.Copy()
		binding.Entry = auth.Entry{Kind: "event", Key: registration.Key}
		binding.Module = moduleID
		bindings = append(bindings, binding)
	}
	return source, bindings, nil
}
