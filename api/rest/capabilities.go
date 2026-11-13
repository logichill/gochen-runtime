package rest

import (
	"gochen/app"
	"gochen/auth"
	"gochen/domain"
	"gochen/errors"
)

// WithAudit 显式挂载同一资源的独立审计读取能力。
// 审计读取使用 reader 自身的授权器，不借用资源 read 权限。
func WithAudit[T domain.IEntity[ID], ID comparable](reader AuditApplication[ID]) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if b.auditSet {
			b.configError = errors.NewCode(errors.InvalidInput, "audit capability may only be configured once")
		}
		b.auditSet, b.audit = true, reader
	}
}

func (rb *RouteBuilder[T, ID]) applications() []app.IDescriptorProvider {
	applications := []app.IDescriptorProvider{rb.service}
	if rb.auditSet {
		applications = append(applications, rb.audit)
	}
	return applications
}

func (rb *RouteBuilder[T, ID]) composeCapabilities() (auth.Descriptor, error) {
	if rb.configError != nil {
		return auth.Descriptor{}, rb.configError
	}
	descriptor := rb.service.Descriptor()
	if err := descriptor.Validate(); err != nil {
		return auth.Descriptor{}, err
	}
	descriptor.Actions = append([]auth.Action(nil), descriptor.Actions...)
	rb.providers = make(map[string]app.IDescriptorProvider, len(descriptor.Actions))
	for _, action := range descriptor.Actions {
		rb.providers[action.Name] = rb.service
	}
	if rb.auditSet {
		if isNilService(rb.audit) {
			return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "explicit audit capability cannot be nil")
		}
		additional := rb.audit.Descriptor()
		if err := additional.Validate(); err != nil {
			return auth.Descriptor{}, err
		}
		if additional.Resource != descriptor.Resource {
			return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "REST capabilities must describe the same resource").WithContext("resource", additional.Resource)
		}
		for _, action := range additional.Actions {
			if action.Name != actionAuditRead || action.Kind != auth.KindRead {
				return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "audit capability declares an unrelated action").WithContext("action", action.Name)
			}
			if _, duplicate := rb.providers[action.Name]; duplicate {
				return auth.Descriptor{}, errors.NewCode(errors.Conflict, "REST action has multiple execution providers").WithContext("action", action.Name)
			}
			rb.providers[action.Name] = rb.audit
			descriptor.Actions = append(descriptor.Actions, action)
		}
	}
	return descriptor, descriptor.Validate()
}

func hasManagedFields[T any]() bool {
	var value T
	_, timestamps := any(value).(domain.ITimestamps)
	_, attribution := any(value).(domain.IChangeInfo)
	_, deletion := any(value).(domain.ISoftDeletable)
	return timestamps || attribution || deletion
}
