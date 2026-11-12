package rest

import (
	"context"

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

// WithDeletion 显式挂载同一资源的删除生命周期。
// CRUD 对软删除实体不声明 delete，删除入口完全由本能力承担。
func WithDeletion[T domain.IEntity[ID], ID comparable](deletion DeletionApplication[T, ID]) Option[T, ID] {
	return func(b *RouteBuilder[T, ID]) {
		if b.deletionSet {
			b.configError = errors.NewCode(errors.InvalidInput, "deletion capability may only be configured once")
		}
		b.deletionSet, b.deletion = true, deletion
	}
}

func (rb *RouteBuilder[T, ID]) applications() []app.IDescriptorProvider {
	applications := []app.IDescriptorProvider{rb.service}
	if rb.auditSet {
		applications = append(applications, rb.audit)
	}
	if rb.deletionSet {
		applications = append(applications, rb.deletion)
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
	for index, capability := range rb.applications()[1:] {
		isAudit := rb.auditSet && index == 0
		if isNilService(capability) {
			return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "explicit REST capability cannot be nil")
		}
		additional := capability.Descriptor()
		if err := additional.Validate(); err != nil {
			return auth.Descriptor{}, err
		}
		if additional.Resource != descriptor.Resource {
			return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "REST capabilities must describe the same resource").WithContext("resource", additional.Resource)
		}
		for _, action := range additional.Actions {
			if isAudit && (action.Name != actionAuditRead || action.Kind != auth.KindRead) {
				return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "audit capability declares an unrelated action").WithContext("action", action.Name)
			}
			if !isAudit {
				expected := map[string]auth.Kind{actionDelete: auth.KindDelete, actionRestore: auth.KindUpdate, actionPurge: auth.KindDelete, actionListDeleted: auth.KindList}
				if kind, known := expected[action.Name]; !known || kind != action.Kind {
					return auth.Descriptor{}, errors.NewCode(errors.InvalidInput, "deletion capability declares an unrelated action").WithContext("action", action.Name)
				}
			}
			if _, duplicate := rb.providers[action.Name]; duplicate {
				return auth.Descriptor{}, errors.NewCode(errors.Conflict, "REST action has multiple execution providers").WithContext("action", action.Name)
			}
			rb.providers[action.Name] = capability
			descriptor.Actions = append(descriptor.Actions, action)
		}
	}
	if rb.deletionSet {
		if maximum := rb.deletion.MaxBulkSize(); maximum > 0 && (rb.maxBulkSize == 0 || maximum < rb.maxBulkSize) {
			rb.maxBulkSize = maximum
		}
	}
	return descriptor, descriptor.Validate()
}

func (rb *RouteBuilder[T, ID]) delete(ctx context.Context, id ID) error {
	if rb.deletionSet {
		return rb.deletion.Delete(ctx, id)
	}
	return rb.write.Delete(ctx, id)
}

func (rb *RouteBuilder[T, ID]) deleteBulk(ctx context.Context, ids []ID) error {
	if rb.deletionSet {
		return rb.deletion.DeleteBulk(ctx, ids)
	}
	return rb.write.DeleteBulk(ctx, ids)
}

func hasManagedFields[T any]() bool {
	var value T
	_, timestamps := any(value).(domain.ITimestamps)
	_, attribution := any(value).(domain.IChangeInfo)
	_, deletion := any(value).(domain.ISoftDeletable)
	return timestamps || attribution || deletion
}
